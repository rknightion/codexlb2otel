// Package config holds the service's configuration types and defaults.
//
// The types are a frozen seam: every sink reads its own section, so they are defined
// once here rather than each sink inventing a struct the loader then has to know
// about. Loading, env overrides and file parsing live in load.go alongside; this file
// is the shape and the defaults only.
package config

import (
	"fmt"
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
		Log:    Log{Level: "info", Format: "text"},
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

	if !c.Loki.Enabled && !c.OTLP.Metrics.Enabled && !c.OTLP.Traces.Enabled && !c.AgentO11y.Enabled {
		add("every sink is disabled; the service would tail the archive and discard it")
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
