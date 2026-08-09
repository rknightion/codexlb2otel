// Package config holds the service's configuration types and defaults.
//
// The types are a frozen seam: every sink reads its own section, so they are defined
// once here rather than each sink inventing a struct the loader then has to know
// about. Loading, env overrides and file parsing live in load.go alongside; this file
// is the shape and the defaults only.
package config

import (
	"fmt"
	"net"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/rknightion/codexlb2otel/internal/attr"
)

// Secret is a configuration value that must never be printed.
//
// It resolves indirections rather than holding the value inline, and its String and
// MarshalJSON deliberately return a mask - so a secret cannot reach a log line or the
// health endpoint through the ordinary act of formatting a struct, which is how
// secrets normally escape.
type Secret string

// Mask is what a Secret formats as.
const Mask = "[redacted]"

// String returns the mask. Use Resolve to obtain the value.
func (s Secret) String() string { return Mask }

// GoString returns the mask, so %#v does not leak either.
func (s Secret) GoString() string { return Mask }

// MarshalJSON returns the mask, so a config dump is safe to serve.
func (s Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + Mask + `"`), nil }

// Resolve returns the actual value, following one level of indirection:
//
//	${NAME}     the NAME environment variable
//	file:PATH   the contents of PATH, trailing whitespace trimmed
//	anything else is the literal value
//
// An unset ${NAME} or an unreadable file: is an ERROR, not an empty string. A service
// that starts with silently-empty credentials fails later, at push time, against a
// remote that answers 401 - which is a far worse place to discover it than startup.
func (s Secret) Resolve() (string, error) {
	v := string(s)
	switch {
	case strings.HasPrefix(v, "${") && strings.HasSuffix(v, "}"):
		name := v[2 : len(v)-1]
		got, ok := os.LookupEnv(name)
		if !ok {
			return "", fmt.Errorf("environment variable %s is not set", name)
		}
		if got == "" {
			return "", fmt.Errorf("environment variable %s is empty", name)
		}
		return got, nil
	case strings.HasPrefix(v, "file:"):
		b, err := os.ReadFile(strings.TrimPrefix(v, "file:"))
		if err != nil {
			return "", fmt.Errorf("reading secret: %w", err)
		}
		got := strings.TrimRight(string(b), " \t\r\n")
		if got == "" {
			return "", fmt.Errorf("secret file %s is empty", strings.TrimPrefix(v, "file:"))
		}
		return got, nil
	case v == "":
		return "", fmt.Errorf("not set")
	default:
		return v, nil
	}
}

// Config is the whole service configuration.
type Config struct {
	Service   Service   `yaml:"service" json:"service"`
	Archive   Archive   `yaml:"archive" json:"archive"`
	Loki      Loki      `yaml:"loki" json:"loki"`
	OTLP      OTLP      `yaml:"otlp" json:"otlp"`
	AgentO11y AgentO11y `yaml:"agento11y" json:"agento11y"`
	Health    Health    `yaml:"health" json:"health"`
	Live      Live      `yaml:"live" json:"live"`
	Summarize Summarize `yaml:"summarize" json:"summarize"`
	Log       Log       `yaml:"log" json:"log"`
}

// Service identifies this deployment.
type Service struct {
	// Name is the service_name Loki label and the OTel service.name resource attribute.
	Name string `yaml:"name" json:"name"`
	// Environment is an optional resource attribute, e.g. "lab".
	Environment string `yaml:"environment" json:"environment"`
}

// Archive configures what is tailed.
type Archive struct {
	// Dir is the conversation-archive directory codex-lb writes into.
	Dir string `yaml:"dir" json:"dir"`
	// Checkpoint is where byte offsets are persisted so a restart resumes exactly.
	Checkpoint string `yaml:"checkpoint" json:"checkpoint"`
	// PollInterval is how often the directory is rechecked.
	PollInterval time.Duration `yaml:"poll_interval" json:"poll_interval"`
	// ChunkBytes bounds how much compressed data is read per pass.
	ChunkBytes int `yaml:"chunk_bytes" json:"chunk_bytes"`
	// DeleteAfter reclaims archives older than this. ZERO MEANS NEVER, and that is the
	// default deliberately: the archive is the only copy of the raw capture, and a
	// reclaim that runs by accident is not recoverable.
	DeleteAfter time.Duration `yaml:"delete_after" json:"delete_after"`
	// RetainDays keeps that many UTC calendar days of archive, counting today, and
	// reclaims fully ingested files from before that. 1 means today only. Zero
	// disables it, on the same reasoning as DeleteAfter.
	//
	// A calendar rule rather than a second duration because the two are not the same
	// thing: "delete yesterday and older" at 09:00 is not "delete anything over 24h
	// old", which would keep half of yesterday. The day comes from the FILENAME,
	// which codex-lb builds in UTC - see tail.archiveDay.
	RetainDays int `yaml:"retain_days" json:"retain_days"`
}

// Loki configures the log sink. Native push, not OTLP: the label and structured
// metadata model is the entire reason this path exists, and OTLP's log model has no
// equivalent of either. There is deliberately no OTLP logs option.
type Loki struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
	// URL is the full push endpoint, including /loki/api/v1/push.
	URL string `yaml:"url" json:"url"`
	// User is the Grafana Cloud Loki user id, sent as the basic-auth username.
	User string `yaml:"user" json:"user"`
	// Token is the access-policy token with logs:write.
	Token Secret `yaml:"token" json:"token"`
	// Labels are the stream keys. Validated against internal/attr at startup: only
	// bounded fields may be promoted, because a label is a stream per distinct value.
	Labels []string `yaml:"labels" json:"labels"`
	// MaxLineBytes is the per-line budget, kept under Loki's max_line_size. Loki
	// DISCARDS an oversized line rather than truncating it, so this must bite first.
	MaxLineBytes int `yaml:"max_line_bytes" json:"max_line_bytes"`
	// BatchSize and BatchWait bound how much is held before a push.
	BatchSize int           `yaml:"batch_size" json:"batch_size"`
	BatchWait time.Duration `yaml:"batch_wait" json:"batch_wait"`
	// Timeout bounds one push.
	Timeout time.Duration `yaml:"timeout" json:"timeout"`
	// MaxRetries bounds retries of a RETRYABLE failure. A permanent 4xx is counted and
	// dropped rather than retried forever, which would wedge the checkpoint.
	MaxRetries int `yaml:"max_retries" json:"max_retries"`
	// RecordTypes selects which line kinds are emitted. Empty means all of them.
	RecordTypes []string `yaml:"record_types" json:"record_types"`
	// MaxLineAge drops lines older than this before pushing, counting them.
	//
	// Grafana Cloud silently discards over-age samples: measured 2026-08-07, a 3h-old
	// push returns 204 and is queryable, and 4h and older return 204 and are queryable
	// nowhere - no error, no rejection body, nothing. Without a local check, replaying
	// an old archive looks like a clean run that delivered everything and in fact
	// delivered none of it.
	//
	// Zero disables the check, which is right only against a tenant with no such limit.
	MaxLineAge time.Duration `yaml:"max_line_age" json:"max_line_age"`
}

// OTLP configures the metric and trace sinks. One endpoint serves both.
type OTLP struct {
	// Endpoint is the OTLP gateway base URL.
	Endpoint string `yaml:"endpoint" json:"endpoint"`
	// InstanceID is the Grafana Cloud instance, sent as the basic-auth username.
	InstanceID string `yaml:"instance_id" json:"instance_id"`
	// Token is the access-policy token.
	Token Secret `yaml:"token" json:"token"`
	// Metrics and Traces are independently switchable: traces are the expensive one and
	// the first thing to turn off if Tempo ingest becomes a problem.
	Metrics OTLPSignal `yaml:"metrics" json:"metrics"`
	Traces  OTLPSignal `yaml:"traces" json:"traces"`
	// Timeout bounds one export.
	Timeout time.Duration `yaml:"timeout" json:"timeout"`
}

// OTLPSignal switches one signal on and sets its export cadence.
type OTLPSignal struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
	// Interval is the metric export period. Ignored for traces, which export on batch.
	Interval time.Duration `yaml:"interval" json:"interval"`
	// SampleRatio is the trace head-sampling ratio in [0,1]. Ignored for metrics.
	SampleRatio float64 `yaml:"sample_ratio" json:"sample_ratio"`
}

// AgentO11y configures the Grafana agent-observability (sigil) generation sink - a
// separate, additive destination from OTLP.Traces. It speaks sigil's own
// ExportGenerations wire contract, not OTLP: sigil's product surface (conversations,
// generations, agent catalog) is populated ONLY by that ingest endpoint or by its
// tolerant OTLP-span decoder, and forwarding plain spans to a generic trace backend -
// which is exactly what OTLP.Traces already does, unchanged, to Tempo - lands nowhere
// in sigil's database. See issue #19.
type AgentO11y struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
	// URL is the full ExportGenerations HTTP endpoint
	// (.../api/v1/generations:export), not just a host.
	URL string `yaml:"url" json:"url"`
	// User is the Grafana Cloud instance id, sent as the basic-auth username - same
	// credentials already used for Loki and OTLP, per instance 654321.
	User string `yaml:"user" json:"user"`
	// Token is the access-policy token.
	Token Secret `yaml:"token" json:"token"`
	// BatchSize and BatchWait bound how many generations are held before a push, same
	// shape as Loki's batching.
	BatchSize int           `yaml:"batch_size" json:"batch_size"`
	BatchWait time.Duration `yaml:"batch_wait" json:"batch_wait"`
	// Timeout bounds one export call.
	Timeout time.Duration `yaml:"timeout" json:"timeout"`
	// MaxRetries bounds retries of a retryable failure (429/5xx/transport). A
	// permanent 4xx that indicts the request itself (bad_request, unauthorized) is
	// never retried - see agento11y.configFault.
	MaxRetries int `yaml:"max_retries" json:"max_retries"`
}

// Health configures the readiness endpoint. Loopback by default: it reports config
// including endpoint URLs, and nothing about it wants to be reachable off the box.
type Health struct {
	Enabled bool   `yaml:"enabled" json:"enabled"`
	Listen  string `yaml:"listen" json:"listen"`
}

// Live configures the in-process web view of recent conversation activity.
//
// Off by default, and loopback by default when on. Unlike Health, which leaks endpoint
// URLs, this one serves CONVERSATION CONTENT - prompts, assistant messages and whole
// command output. The README's content warning applies to a browser exactly as it
// applies to Loki, which is why Validate refuses to bind it off-loopback without either
// a token or an explicit acknowledgement.
type Live struct {
	Enabled bool   `yaml:"enabled" json:"enabled"`
	Listen  string `yaml:"listen" json:"listen"`
	// Token, when set, is required on every request as `Authorization: Bearer <token>`
	// or as a `token` query parameter. Secret, so a config dump never echoes it.
	Token Secret `yaml:"token" json:"token"`
	// RetainTurns bounds the in-memory ring. Everything the view serves derives from it,
	// so it is the feature's whole memory cost.
	RetainTurns int `yaml:"retain_turns" json:"retain_turns"`
	// RetainWindow hides threads idle for longer than this.
	//
	// It is an ADAPTIVE window, not a plain age cutoff: a thread with a response still
	// open is kept however long it has been quiet, and so is any ancestor of a thread
	// that is kept. Otherwise the view evicts, with priority, the wedged agent that is
	// the most useful thing on it - and orphans the children of anything it drops.
	RetainWindow time.Duration `yaml:"retain_window" json:"retain_window"`
	// StallAfter is how long an open response may go without producing a frame before
	// it is flagged stalled. Measured on the ARCHIVE's clock rather than wall clock, so
	// ingestion falling behind does not mark everything stalled at once. Zero disables.
	StallAfter time.Duration `yaml:"stall_after" json:"stall_after"`
	// Content serves message bodies. False gives a structural view - models, tool names,
	// subagent kinds, timings, token counts - with no prose anywhere.
	Content bool `yaml:"content" json:"content"`
	// IncludeProbe keeps this service's own synthetic health traffic in the view.
	IncludeProbe bool `yaml:"include_probe" json:"include_probe"`
	// AllowInsecure permits a non-loopback listen with no token. Required explicitly,
	// because the alternative - a warning - is a line in a log nobody reads on a
	// listener serving every prompt and every command output to anyone who finds it.
	AllowInsecure bool `yaml:"allow_insecure" json:"allow_insecure"`
}

// Summarize configures clbsum, which sends conversation content to an LLM to be told
// what work a session accomplished.
//
// It is the only part of this project that sends captured content to a THIRD PARTY.
// Loki and the OTLP gateway are endpoints the operator chose and controls; OpenRouter
// routes to whichever provider serves the model. That is why it is off by default and
// why the two routing preferences below are set the way they are rather than left to
// OpenRouter's own defaults.
type Summarize struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
	// APIKey is the OpenRouter credential. Secret, so a config dump never echoes it.
	APIKey Secret `yaml:"api_key" json:"api_key"`
	// Model is an OpenRouter model slug.
	//
	// The default is the FLOATING alias - the "~" prefix makes OpenRouter resolve to the
	// newest model in that family, and the response reports which concrete model served
	// it. The undated "deepseek/deepseek-v4-flash" looks like an alias and is not: it is
	// the Apr 2026 0423 release, and pinning it silently freezes the tool on an old model.
	//
	// A "~latest" slug also carries a compatibility contract that a concrete slug does
	// not: if the alias retargets to a model that rejects a reasoning parameter sent here,
	// OpenRouter remaps it to the nearest supported value rather than returning 400.
	Model string `yaml:"model" json:"model"`
	// ReasoningEffort is how much of the token budget goes to reasoning.
	//
	// "high" allocates roughly 80% against "max"/"xhigh"'s ~95%. Summarising is reading a
	// transcript and reporting what happened, which does not repay the extra budget - and
	// the first live run against an 8h session timed out on its final combining pass, so
	// the cost is not hypothetical. Empty sends nothing and takes the model's own default.
	ReasoningEffort string `yaml:"reasoning_effort" json:"reasoning_effort"`
	// BaseURL overrides the OpenRouter endpoint, for a proxy. Empty uses the SDK default.
	BaseURL string `yaml:"base_url" json:"base_url"`

	// MaxCharsPerSession bounds one thread's digest. Over it, the digest is chunked at
	// turn boundaries and summarised in several passes.
	MaxCharsPerSession int `yaml:"max_chars_per_session" json:"max_chars_per_session"`
	// MaxCharsPerToolInput bounds one tool call's arguments. Generous: a call's
	// arguments are the file paths, commands and patch bodies that say what actually
	// changed, which is the whole question being asked.
	MaxCharsPerToolInput int `yaml:"max_chars_per_tool_input" json:"max_chars_per_tool_input"`
	// MaxCharsPerToolOutput bounds one tool result, kept as a head AND a tail. Tight:
	// tool output is mostly console noise. Head-only would be worse than tight - a
	// command's verdict is usually its last line, so head-only truncation reliably cuts
	// exactly the part that mattered.
	MaxCharsPerToolOutput int `yaml:"max_chars_per_tool_output" json:"max_chars_per_tool_output"`

	// Concurrency bounds in-flight LLM calls.
	Concurrency int `yaml:"concurrency" json:"concurrency"`
	// MaxRetries bounds the backoff on 429 and 5xx.
	MaxRetries int `yaml:"max_retries" json:"max_retries"`
	// Timeout bounds one LLM call.
	Timeout time.Duration `yaml:"timeout" json:"timeout"`

	// ZDR restricts routing to zero-data-retention endpoints. DataCollection is
	// "allow" or "deny"; deny uses only providers that do not store user data. Both
	// default to the restrictive setting - this ships real prompts and real command
	// output, and OpenRouter's own default for data_collection is "allow".
	ZDR            bool   `yaml:"zdr" json:"zdr"`
	DataCollection string `yaml:"data_collection" json:"data_collection"`

	// ResponseCache asks OpenRouter to serve a byte-identical request from its own
	// response cache, which is free and never reaches a provider.
	//
	// It earns its default from this tool's worst failure mode: a run of ~87 calls that
	// dies on the last one has already paid for 86. Re-running re-sends those 86
	// identical requests, and with this on they cost nothing. Editing a prompt changes
	// every request and invalidates the lot, which is correct rather than unfortunate.
	ResponseCache bool `yaml:"response_cache" json:"response_cache"`
}

// Log configures the service's own logging.
type Log struct {
	// Level is debug | info | warn | error.
	Level string `yaml:"level" json:"level"`
	// Format is text | json.
	Format string `yaml:"format" json:"format"`
}

// Default returns a configuration that is valid, safe, and does nothing surprising:
// no deletion, loopback health, minimal Loki labels, both OTLP signals off until an
// endpoint is configured.
func Default() Config {
	return Config{
		Service: Service{Name: "codexlb2otel"},
		Archive: Archive{
			Dir:          "/opt/codex-lb/data/codex-lb/conversation-archive",
			Checkpoint:   "/var/lib/codexlb2otel/checkpoint.json",
			PollInterval: 5 * time.Second,
			ChunkBytes:   16 << 20,
			DeleteAfter:  0, // never; see the field comment
		},
		Loki: Loki{
			URL:    "https://logs-prod-035.grafana.net/loki/api/v1/push",
			Labels: append([]string(nil), attr.DefaultLabels...),
			// Loki's max_line_size default is 256KB and an oversized line is DISCARDED,
			// not truncated. 192KB leaves room for the structured metadata and the
			// stream labels that ride alongside the body in the push payload.
			MaxLineBytes: 192 << 10,
			// Measured boundary on this tenant is between 3h and 4h. 3h leaves headroom
			// for a batch that waits, without discarding anything Loki would have taken.
			MaxLineAge: 3 * time.Hour,
			BatchSize:  1000,
			BatchWait:  2 * time.Second,
			Timeout:    30 * time.Second,
			MaxRetries: 5,
		},
		OTLP: OTLP{
			Endpoint: "https://otlp-gateway-prod-gb-south-1.grafana.net/otlp",
			Metrics:  OTLPSignal{Interval: 30 * time.Second},
			Traces:   OTLPSignal{SampleRatio: 1},
			Timeout:  30 * time.Second,
		},
		AgentO11y: AgentO11y{
			URL: "https://agento11y-prod-gb-south-1.grafana.net/api/v1/generations:export",
			// Generations carry full message content, not just metadata - smaller
			// batches than Loki's 1000 line-per-push default so one push failure
			// re-sends less on retry.
			BatchSize:  200,
			BatchWait:  5 * time.Second,
			Timeout:    30 * time.Second,
			MaxRetries: 5,
		},
		Health: Health{Enabled: true, Listen: "127.0.0.1:9464"},
		// Its own port, deliberately not shared with health: /healthz serves the whole
		// config dump, and exposing the conversation view on a tailnet must not drag
		// that along with it.
		Live: Live{
			Enabled: false,
			Listen:  "127.0.0.1:9465",
			// The ring is a MEMORY backstop, not the retention policy - retain_window is.
			// At 500 it was the thing actually evicting: measured against a real fan-out on
			// camden, 5 threads produced 238 turns in 19 minutes, so 500 turns held roughly
			// 41 minutes and a wider fan-out would have held far less. Retention has to mean
			// what it says, so the ring is set high enough not to bind first.
			RetainTurns:  20000,
			RetainWindow: 30 * time.Minute,
			StallAfter:   5 * time.Minute,
			Content:      true,
		},
		Summarize: Summarize{
			Enabled: false,
			// 1,048,576-token context on this family. The budget below is ~375k tokens,
			// so a whole session normally fits in one call and chunking stays rare.
			Model:                 "~deepseek/deepseek-v4-flash-latest",
			ReasoningEffort:       "high",
			MaxCharsPerSession:    1_500_000,
			MaxCharsPerToolInput:  20_000,
			MaxCharsPerToolOutput: 2_000,
			Concurrency:           4,
			MaxRetries:            5,
			// 5m was not enough: the final combining pass of an 8h session, folding 81
			// sub-agent summaries together, exceeded it while reading the response body.
			Timeout:        20 * time.Minute,
			ZDR:            true,
			DataCollection: "deny",
			ResponseCache:  true,
		},
		Log: Log{Level: "info", Format: "text"},
	}
}

// Validate reports every problem it finds rather than only the first, so a misconfigured
// deployment is fixed in one pass instead of one restart per mistake. Every message
// names the field.
func (c Config) Validate() error {
	var errs []string
	add := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }

	if c.Service.Name == "" {
		add("service.name is empty; it is the service_name Loki label and the OTel resource attribute")
	}
	if c.Archive.Dir == "" {
		add("archive.dir is empty; there is nothing to tail")
	}
	if c.Archive.Checkpoint == "" {
		add("archive.checkpoint is empty; a restart would re-ship the whole archive")
	}
	if c.Archive.PollInterval <= 0 {
		add("archive.poll_interval must be positive, got %s", c.Archive.PollInterval)
	}
	if c.Archive.RetainDays < 0 {
		add("archive.retain_days cannot be negative, got %d", c.Archive.RetainDays)
	}
	if c.Archive.ChunkBytes <= 0 {
		add("archive.chunk_bytes must be positive, got %d", c.Archive.ChunkBytes)
	}

	if c.Loki.Enabled {
		if c.Loki.URL == "" {
			add("loki.url is empty but loki.enabled is true")
		}
		if c.Loki.User == "" {
			add("loki.user is empty; Grafana Cloud uses it as the basic-auth username")
		}
		if _, err := c.Loki.Token.Resolve(); err != nil {
			add("loki.token: %v", err)
		}
		if err := attr.ValidateLabels(c.Loki.Labels); err != nil {
			add("loki.labels: %v", err)
		}
		if c.Loki.MaxLineBytes <= 0 || c.Loki.MaxLineBytes > 256<<10 {
			add("loki.max_line_bytes must be in (0, 262144]; Loki DISCARDS a line over its "+
				"max_line_size rather than truncating it, got %d", c.Loki.MaxLineBytes)
		}
		if c.Loki.BatchSize <= 0 {
			add("loki.batch_size must be positive, got %d", c.Loki.BatchSize)
		}
		for _, rt := range c.Loki.RecordTypes {
			if !validRecordType(rt) {
				add("loki.record_types contains %q, which is not a record type this service "+
					"emits (%s)", rt, strings.Join(attr.RecordTypes, ", "))
			}
		}
	}

	if c.OTLP.Metrics.Enabled || c.OTLP.Traces.Enabled {
		if c.OTLP.Endpoint == "" {
			add("otlp.endpoint is empty but a signal is enabled")
		}
		if c.OTLP.InstanceID == "" {
			add("otlp.instance_id is empty; Grafana Cloud uses it as the basic-auth username")
		}
		if _, err := c.OTLP.Token.Resolve(); err != nil {
			add("otlp.token: %v", err)
		}
	}
	if c.OTLP.Metrics.Enabled && c.OTLP.Metrics.Interval <= 0 {
		add("otlp.metrics.interval must be positive, got %s", c.OTLP.Metrics.Interval)
	}
	if c.OTLP.Traces.Enabled && (c.OTLP.Traces.SampleRatio < 0 || c.OTLP.Traces.SampleRatio > 1) {
		add("otlp.traces.sample_ratio must be in [0,1], got %v", c.OTLP.Traces.SampleRatio)
	}

	if c.AgentO11y.Enabled {
		if c.AgentO11y.URL == "" {
			add("agento11y.url is empty but agento11y.enabled is true")
		}
		if c.AgentO11y.User == "" {
			add("agento11y.user is empty; Grafana Cloud uses it as the basic-auth username")
		}
		if _, err := c.AgentO11y.Token.Resolve(); err != nil {
			add("agento11y.token: %v", err)
		}
		if c.AgentO11y.BatchSize <= 0 {
			add("agento11y.batch_size must be positive, got %d", c.AgentO11y.BatchSize)
		}
	}

	// Live counts as a destination here even though it is a volatile view: with it on,
	// the archive is being tailed FOR something, and refusing to start would block the
	// legitimate "just show me what is happening" deployment.
	if !c.Loki.Enabled && !c.OTLP.Metrics.Enabled && !c.OTLP.Traces.Enabled && !c.AgentO11y.Enabled && !c.Live.Enabled {
		add("every sink is disabled; the service would tail the archive and discard it")
	}

	if c.Live.Enabled {
		if c.Live.Listen == "" {
			add("live.listen is empty but live.enabled is true")
		}
		if c.Live.RetainTurns <= 0 {
			add("live.retain_turns must be positive, got %d", c.Live.RetainTurns)
		}
		if c.Live.RetainWindow <= 0 {
			add("live.retain_window must be positive, got %s", c.Live.RetainWindow)
		}
		if c.Live.StallAfter < 0 {
			add("live.stall_after cannot be negative, got %s", c.Live.StallAfter)
		}
		// Unlike every other Secret here, this one is OPTIONAL - a loopback view needs no
		// token - so an empty value must not be run through Resolve, which correctly
		// treats "not set" as an error for a credential that is required.
		var token string
		if c.Live.Token != "" {
			var err error
			if token, err = c.Live.Token.Resolve(); err != nil {
				add("live.token: %v", err)
			}
		}
		if c.Live.Listen != "" && !loopbackListen(c.Live.Listen) && token == "" && !c.Live.AllowInsecure {
			add("live.listen %q is not loopback and live.token is empty; this endpoint serves conversation content, "+
				"so set live.token or set live.allow_insecure: true to say you meant it", c.Live.Listen)
		}
	}

	// Only validated when enabled. The block is present in every default config, so
	// validating it unconditionally would fail every deployment that never asked for it.
	if c.Summarize.Enabled {
		for _, e := range c.Summarize.problems() {
			add("%s", e)
		}
	}

	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		add("log.level must be debug, info, warn or error, got %q", c.Log.Level)
	}
	switch c.Log.Format {
	case "text", "json":
	default:
		add("log.format must be text or json, got %q", c.Log.Format)
	}

	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("invalid configuration:\n  - %s", strings.Join(errs, "\n  - "))
}

func validRecordType(rt string) bool { return slices.Contains(attr.RecordTypes, rt) }

// loopbackListen reports whether a host:port binds only to the local machine.
//
// The empty host is the case that matters and the one an eyeball misreads: ":9465" and
// "0.0.0.0:9465" both bind every interface, and the first looks far more innocent than
// the second. Anything it cannot parse is treated as NOT loopback, so an unfamiliar
// form fails closed.
func loopbackListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
