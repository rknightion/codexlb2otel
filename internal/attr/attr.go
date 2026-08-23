// Package attr owns the one mapping from a reduced Turn to telemetry attributes.
//
// It exists because three emitters - Loki, OTLP metrics, OTLP traces - would otherwise
// each decide independently which of Turn's ~90 fields become metric attributes, which
// become Loki stream labels, which become structured metadata and which become span
// attributes. Deciding separately, they would disagree; and a field promoted to a
// metric attribute in error is not a cosmetic problem but a cardinality incident that
// is expensive to undo once the series exist.
//
// Two rules the emitters cannot opt out of:
//
//  1. Nothing becomes a metric attribute or a Loki label unless this package says so.
//     Sinks call the builders here; they never assemble attribute sets of their own.
//
//  2. Bounded is enforced at RUNTIME, not assumed from a comment. Every bounded field
//     carries a cap on distinct values; past that cap, further unseen values collapse
//     to OtherValue and are counted. The corpus says `model` takes six values today,
//     but the guard is what stops it becoming a series-per-value if the field ever
//     starts carrying something id-shaped.
package attr

import (
	"fmt"
	"maps"
	"sort"
	"strconv"
	"sync"

	"github.com/rknightion/codexlb2otel/internal/correlation"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

// KV is one emitted attribute. Deliberately not otel's attribute.KeyValue: the Loki
// sink has no reason to depend on the OpenTelemetry SDK, and this package is imported
// by all three sinks.
type KV struct {
	Key   string
	Value string
}

// OtherValue replaces a value that would push a field past its cap.
//
// Substitution rather than dropping the attribute, and rather than dropping the whole
// datapoint: the measurement is still true and still belongs in the total. Losing it
// would make a cardinality explosion look like a traffic drop, which is the worst of
// both outcomes.
const OtherValue = "_other"

// Class decides where a field is allowed to go.
type Class uint8

const (
	// Bounded fields take an enum-like set of values. Safe as metric attributes, and
	// the only fields config may promote to Loki stream labels.
	Bounded Class = iota
	// Identity fields are ids: unbounded, rotating, or both. Structured metadata and
	// span attributes only - both store per record rather than indexing per value.
	//
	// Also covers free-text CONTENT values with the same routing requirement, not
	// just ids in the literal sense (issue #18's ToolCallArguments and
	// ToolCallResult): both are unbounded, neither belongs anywhere near a metric
	// attribute or a Loki label, and "never aggregated, never indexed by value" is
	// exactly the Identity contract regardless of whether the value happens to be an
	// identifier or a blob of tool output.
	Identity
	// Sensitive fields map 1:1 to a human. Structured metadata only: not a metric
	// attribute, not a Loki label, and not a span attribute either, because Tempo
	// indexes span attributes for search.
	Sensitive
)

// Field is one attribute this service knows how to emit.
type Field struct {
	Key   string
	Class Class
	// Cap bounds distinct values at runtime. Ignored for Identity and Sensitive, which
	// are never indexed by value in the first place.
	Cap int
	// Observed is the measured value set as of 2026-08-07, across 1.32M records and
	// 6,936 turns. It is DOCUMENTATION and the corpus test's expectation - deliberately
	// not enforced, so that a genuinely new model or error code flows through with its
	// real name instead of silently becoming OtherValue until someone edits this file.
	Observed []string
	// IDLike marks a bounded field whose values are id-shaped by nature, so the corpus
	// test's "does this look like an identifier" heuristic does not fire on it.
	//
	// Exactly one field qualifies, and it is a deliberate exception: account_id is a
	// UUID, but codex-lb balances across three accounts and rate-limit headroom
	// averaged across them hides the exhaustion it exists to show. The Cap is what
	// keeps the exception safe - if the field ever stops meaning "which of my accounts"
	// the cap contains it, whatever it starts to look like.
	IDLike bool
	// Of extracts the value. Empty means the field does not apply to this turn and is
	// omitted rather than emitted blank.
	//
	// NIL means the field is caller-supplied rather than Turn-derived: a tool name
	// belongs to one ToolCall and a token type to one counter, so neither can be
	// extracted from the Turn as a whole. They are still registered here, and still
	// routed through Guard.With, because that is the one path every sink uses instead
	// of assembling its own attribute set - which is the hole this closes. Both were
	// name constants with no registry entry once, so every value flowed through
	// uncapped, and gen_ai.tool.name is genuinely open-ended.
	//
	// A caller-supplied field may be Bounded (ToolName, GenAITokenType, ToolType -
	// Guard.With applies the same cap it would if the value came from a Turn) or
	// Identity (ToolCallID, ToolCallArguments, ToolCallResult - per-invocation,
	// unbounded by nature, so Guard.With passes them through uncapped, same as it
	// would for any other Identity field). It must never be Sensitive: With has no
	// per-class gate of its own the way SpanAttrs/MetricAttrs/Labels do, so a
	// Sensitive caller-supplied field would be the one path around the "never a span
	// attribute" guarantee - see TestRegistryIsWellFormed, which is what closes that
	// off at registration time rather than trusting every call site to remember.
	Of func(t *turn.Turn) string
}

// registry is the whole contract, in emit order.
//
// Cardinality of the bounded set as measured: 6 models x 4 status x 3 request kinds x
// 3 families x 4 efforts x 3 accounts x 2 plans x 2 tiers x 2 thread sources x 3
// originators is a large theoretical product, but the combinations that actually occur
// are sparse - a probe is never a subagent turn, xhigh never appears on a prewarm. The
// caps below are what make the theoretical product irrelevant.
var registry = []Field{
	// --- bounded: metric attributes, and promotable to labels ---
	{Key: GenAIRequestModel, Class: Bounded, Cap: 32,
		Observed: []string{"gpt-5.6-terra", "gpt-5.6-sol", "gpt-5.3-codex-spark", "gpt-5.6-sol-wm", "gpt-5.4-mini", "gpt-5.6-luna"},
		Of:       func(t *turn.Turn) string { return t.Model }},
	{Key: GenAIResponseModel, Class: Bounded, Cap: 32,
		Of: func(t *turn.Turn) string {
			// They differ only when safety buffering re-ran the response through a
			// different model, which is the one case worth being able to see.
			if t.SafetyRetryModel != "" {
				return t.SafetyRetryModel
			}
			return t.Model
		}},
	{Key: Status, Class: Bounded, Cap: 16,
		Observed: []string{"completed", "incomplete", "transport", "error"},
		Of:       func(t *turn.Turn) string { return t.Status }},
	{Key: RequestKind, Class: Bounded, Cap: 8,
		Observed: []string{"turn", "prewarm", "compaction", "memory"},
		Of:       func(t *turn.Turn) string { return t.RequestKind }},
	{Key: Family, Class: Bounded, Cap: 8,
		Observed: []string{"websocket", "http", "probe", "unknown"},
		Of:       func(t *turn.Turn) string { return t.Family }},
	{Key: ReasoningEffort, Class: Bounded, Cap: 8,
		Observed: []string{"low", "medium", "high", "xhigh"},
		Of:       func(t *turn.Turn) string { return t.Effort }},
	{Key: AccountID, Class: Bounded, Cap: 16, IDLike: true,
		Of: func(t *turn.Turn) string { return t.AccountID }},
	{Key: PlanType, Class: Bounded, Cap: 8,
		Observed: []string{"pro", "business"},
		Of:       func(t *turn.Turn) string { return t.PlanType }},
	{Key: ServiceTier, Class: Bounded, Cap: 8,
		Observed: []string{"default", "auto"},
		Of:       func(t *turn.Turn) string { return t.ServiceTier }},
	// Observed is one value, not two: before the 2026-08-08 cutover the field was
	// absent from response.create entirely, and an absent field yields "" and is
	// omitted rather than emitted blank. So the corpus shows priority alone even
	// though most of it predates priority - which is exactly the "off" state, not a
	// gap in the sample.
	{Key: ServiceTierRequested, Class: Bounded, Cap: 8,
		Observed: []string{"priority"},
		Of:       func(t *turn.Turn) string { return t.ServiceTierRequested }},
	{Key: ThreadSource, Class: Bounded, Cap: 8,
		Observed: []string{"user", "subagent"},
		Of:       func(t *turn.Turn) string { return t.ThreadSource }},
	{Key: SubagentKind, Class: Bounded, Cap: 8,
		Observed: []string{"collab_spawn", "thread_spawn", "memory_consolidation"},
		Of:       func(t *turn.Turn) string { return t.SubagentKind }},
	{Key: Originator, Class: Bounded, Cap: 16,
		Observed: []string{"codex-tui", "codex_exec", "codex_cli_rs"},
		Of:       func(t *turn.Turn) string { return t.Originator }},
	// GenAIAgentName is Originator wearing agent-observability's name: its value set is
	// Originator's with a prefix and nothing else, so the cap matches Originator's.
	//
	// On any attribute set that already carries Originator the two are perfectly
	// correlated and this adds no series at all. On the three narrowed gen_ai.client.*
	// sets it is a genuinely new dimension (they do not carry Originator), so it costs
	// its value count there - three today. That is the price of the fix, not a free
	// lunch: without it those series have no agent at all.
	{Key: GenAIAgentName, Class: Bounded, Cap: 16,
		Observed: []string{"codexlb-codex-tui", "codexlb-codex_exec", "codexlb-codex_cli_rs"},
		Of:       AgentName},
	// A value set that turns over rather than accumulates: the hash changes only when
	// codex ships a new system prompt, and the guard's state is per-process, so a
	// long-lived process sees a handful. It is IDLike because a truncated sha256 is
	// exactly what the corpus test's "does this look like an identifier" heuristic is
	// built to catch - here that shape is the point, not an accident, and the cap is
	// what keeps the exception safe.
	//
	// Cap raised 32 -> 96 on 2026-08-08, when the corpus reached 35 distinct hashes
	// over 10,290 turns and the old cap failed. The premise still holds and this is
	// not a slow leak: the corpus is an ACCUMULATION of every archive ever synced
	// (26 hours across three days by then), while the guard only ever sees one
	// process's lifetime. The two count different things, and the corpus number is
	// always the larger one - so the cap has to be sized against the corpus, which
	// grows, rather than against the runtime, which does not. 96 is roughly the
	// observed rate (~12/day of retained corpus) times two months of headroom; if it
	// fails again, check the growth rate before raising it, because a JUMP rather
	// than a drift would mean the hash has stopped tracking system-prompt identity.
	{Key: GenAIAgentVersion, Class: Bounded, Cap: 96, IDLike: true,
		Of: func(t *turn.Turn) string { return t.InstructionsHash }},
	{Key: ErrorType, Class: Bounded, Cap: 32,
		Of: func(t *turn.Turn) string { return t.ErrorType }},
	{Key: ErrorCategory, Class: Bounded, Cap: 8,
		Observed: []string{"sdk_error"},
		Of: func(t *turn.Turn) string {
			if t.Status == turn.StatusError || t.ErrorType != "" || t.ErrorCode != "" {
				return "sdk_error"
			}
			return ""
		}},
	{Key: ErrorCode, Class: Bounded, Cap: 64,
		Observed: []string{"websocket_connection_limit_reached"},
		Of:       func(t *turn.Turn) string { return t.ErrorCode }},
	{Key: CriticalPathCoverage, Class: Bounded, Cap: 8,
		Observed: []string{"complete", "missing_harness_boundary"},
		Of:       func(t *turn.Turn) string { return t.CriticalPath.Coverage }},
	{Key: CloseCode, Class: Bounded, Cap: 32,
		Observed: []string{"1000", "1012"},
		Of: func(t *turn.Turn) string {
			if t.CloseCode == nil {
				return ""
			}
			return strconv.Itoa(*t.CloseCode)
		}},
	{Key: BaselineReset, Class: Bounded, Cap: 4,
		Observed: []string{"true", "false"},
		Of:       func(t *turn.Turn) string { return strconv.FormatBool(t.BaselineReset) }},
	{Key: FrameType, Class: Bounded, Cap: 8,
		Observed: []string{"close", "error"},
		// Derived rather than carried. Turn.TransportEvent holds the server's
		// plain-text reason ("no close frame received or sent"), which is prose, not an
		// enum - so the frame class is reconstructed from what the reducer did record.
		// Error wins over close: a connection that errored and then closed is an error.
		Of: func(t *turn.Turn) string {
			switch {
			case t.FrameErrors > 0:
				return "error"
			case t.CloseCode != nil:
				return "close"
			}
			return ""
		}},

	// --- caller-supplied: registered so Guard.With caps them, see Field.Of ---
	// The full measured catalogue as of the 2026-08-07 corpus: nine names across
	// 1.84M records, against a cap of 64. The margin is the point - a tool name is
	// whatever the model's catalogue happens to contain, so this is the one bounded
	// field with no upstream guarantee of being bounded at all.
	{Key: ToolName, Class: Bounded, Cap: 64,
		Observed: []string{"exec", "spawn_agent", "wait_agent", "list_agents", "send_message",
			"followup_task", "interrupt_agent", "request_user_input", "wait"}},
	{Key: GenAITokenType, Class: Bounded, Cap: 8,
		Observed: []string{TokenInput, TokenOutput, TokenReasoning, TokenCacheRead, TokenCacheWrite}},
	// One value, ever - but registered rather than hardcoded at the call site so it
	// goes through the same Guard.With path as every other caller-supplied attribute,
	// which is the rule this package exists to enforce. Cap 4 leaves room for the
	// enum's other member (UNSPECIFIED) should a future path ever be unable to
	// positively identify the shape.
	{Key: GenAITokenSemantics, Class: Bounded, Cap: 4,
		Observed: []string{TokenSemanticsInclusive}},
	// ToolType is ToolCall.Kind: custom | function today, per turn.go's own comment on
	// that field. Cap matches ToolName's caution about a field with no upstream
	// guarantee of staying small, even though only two values have ever been seen.
	{Key: ToolType, Class: Bounded, Cap: 16},

	// --- identity: structured metadata and span attributes ---
	{Key: GenAIResponseID, Class: Identity, Of: func(t *turn.Turn) string { return t.ResponseID }},
	{Key: GenAIConversationID, Class: Identity, Of: func(t *turn.Turn) string { return t.ThreadID }},
	{Key: RequestID, Class: Identity, Of: func(t *turn.Turn) string { return t.RequestID }},
	{Key: SessionID, Class: Identity, Of: func(t *turn.Turn) string { return t.SessionID }},
	{Key: ThreadID, Class: Identity, Of: func(t *turn.Turn) string { return t.ThreadID }},
	{Key: ParentThreadID, Class: Identity, Of: func(t *turn.Turn) string { return t.ParentThreadID }},
	// Both were captured by the reducer and reached the turn line's JSON body, but
	// had no registry entry - so Guard.Metadata never emitted them and they were
	// invisible to a LogQL metadata filter and absent from content lines entirely.
	// Issue #15 wants the response chain walkable and wants the routing fields that
	// explain WHY a turn used the parameters it did.
	{Key: PrevResponseID, Class: Identity, Of: func(t *turn.Turn) string { return t.PrevResponseID }},
	{Key: ForkedFromThreadID, Class: Identity, Of: func(t *turn.Turn) string { return t.ForkedFromThreadID }},
	{Key: TurnID, Class: Identity, Of: func(t *turn.Turn) string { return t.TurnID }},
	{Key: ParentTurnID, Class: Identity, Of: func(t *turn.Turn) string { return t.ParentTurnID }},
	{Key: LogicalTurnID, Class: Identity, Of: func(t *turn.Turn) string { return t.LogicalTurnID }},
	{Key: WindowID, Class: Identity, Of: func(t *turn.Turn) string { return t.WindowID }},
	{Key: InstallationID, Class: Identity, Of: func(t *turn.Turn) string { return t.InstallationID }},
	{Key: PromptCacheKey, Class: Identity, Of: func(t *turn.Turn) string { return t.PromptCacheKey }},
	{Key: EngineIDs, Class: Identity, Of: func(t *turn.Turn) string { return t.EngineIDs }},
	{Key: InstructionsHash, Class: Identity, Of: func(t *turn.Turn) string { return t.InstructionsHash }},
	{Key: ErrorMessage, Class: Identity, Of: func(t *turn.Turn) string { return t.ErrorMessage }},
	// Identity rather than bounded: it is the server's own prose reason for a transport
	// event, and prose is not an enum however few distinct strings a capture happens to
	// hold. FrameType above is the bounded classification of the same event.
	{Key: TransportEvent, Class: Identity, Of: func(t *turn.Turn) string { return t.TransportEvent }},

	// Token usage as span attributes (issue #18) - Turn already carries all six
	// fields; before this they existed only as the codexlb.tokens counter, split by
	// GenAITokenType, so a decoded span carried no usage figures at all. Identity, not
	// Bounded: these are measurements, not dimensions - nothing should ever group a
	// metric BY a token count. positiveItoa omits a non-positive count rather than
	// emitting "0", matching Field.Of's own contract that empty means "does not apply"
	// (a turn with no cached tokens is not the same fact as a turn with a "0" recorded
	// for cached tokens - the former never had a cache to hit).
	{Key: GenAIUsageInputTokens, Class: Identity, Of: func(t *turn.Turn) string { return positiveItoa(t.InputTokens) }},
	{Key: GenAIUsageOutputTokens, Class: Identity, Of: func(t *turn.Turn) string { return positiveItoa(t.OutputTokens) }},
	{Key: GenAIUsageReasoningTokens, Class: Identity, Of: func(t *turn.Turn) string { return positiveItoa(t.ReasoningTokens) }},
	{Key: GenAIUsageCacheReadTokens, Class: Identity, Of: func(t *turn.Turn) string { return positiveItoa(t.CachedTokens) }},
	{Key: GenAIUsageCacheWriteTokens, Class: Identity, Of: func(t *turn.Turn) string { return positiveItoa(t.CacheWriteTokens) }},

	// --- caller-supplied identity: per tool call, not per Turn (see ToolType above
	// for why the analogous per-call BOUNDED field lives in the earlier block) ---
	{Key: ToolCallID, Class: Identity},
	{Key: ToolCallArguments, Class: Identity},
	{Key: ToolCallResult, Class: Identity},
	// SubagentTask took over what GenAIAgentName carried here before issue #32 - see
	// that constant's own doc comment. Identity, not Bounded, and that is the whole
	// argument for moving it: a spawn_agent task label is caller-authored free text
	// with no upstream bound, so under the capped standard key everything past the cap
	// would have collapsed to _other.
	{Key: SubagentTask, Class: Identity},

	// --- sensitive ---
	{Key: SafetyID, Class: Sensitive, Of: func(t *turn.Turn) string { return t.SafetyID }},
}

// AgentName is GenAIAgentName's value for one turn: "codexlb-<originator>", or a bare
// "codexlb" when the originator is absent.
//
// Never empty, unlike almost every other Field.Of in the registry. An agent name that
// sometimes disappears is worse than a coarse one: a turn with no originator header
// would drop out of every per-agent series and reappear in agent-observability's
// anonymous bucket - the exact failure issue #32 fixed - so the fallback keeps it in
// the right agent at a coarser granularity instead.
//
// Exported because internal/sink/agento11y sets the same value on Generation.agent_name
// and the two must not be able to disagree.
func AgentName(t *turn.Turn) string {
	if t.Originator == "" {
		return AgentNameFallback
	}
	return AgentNamePrefix + t.Originator
}

// GenerationID is the id of the Generation representing this response - the value
// internal/sink/agento11y pushes as Generation.id and internal/sink/otlptrace stamps
// onto the response span as agento11y.generation.id.
//
// It lives HERE, in the package both sinks already depend on, because the two must be
// byte-identical or the span/generation link resolves to nothing in agent
// observability and the conversation view silently shows a turn with no trace - a
// failure with no error anywhere. It was a private helper in the agento11y sink until
// issue #32 gave the trace sink a reason to need the same value.
//
// ResponseID (resp_*) is the natural choice: the id of the exact model response this
// Generation represents, one-to-one with the Turn, and stable across a retry or a
// checkpoint replay so a repeated push dedups rather than duplicating. It is empty on
// a response that never completed, so RequestID is the first fallback. The shared
// correlation key then uses LogicalTurnID and the server TurnID defensively; a record
// with none of those identities is not a valid Generation and the sink skips it.
func GenerationID(t *turn.Turn) string {
	return correlation.ResponseKey(t)
}

// OperationName is gen_ai.operation.name for one response: streamText when the
// response delivered any text deltas over the websocket, generateText otherwise.
//
// The same test internal/sink/agento11y's mode() uses to pick between
// GENERATION_MODE_STREAM and GENERATION_MODE_SYNC, and deliberately so: sigil's own
// ingest defaults operation_name from mode with exactly this mapping
// (defaultOperationNameForMode), so a Generation whose mode and operation name
// disagreed would be self-contradictory on arrival. Exported for the same reason
// AgentName is.
func OperationName(t *turn.Turn) string {
	if t.TextDeltaCount > 0 {
		return GenAIOperationStreamText
	}
	return GenAIOperationGenerateText
}

// positiveItoa renders n, or "" for n <= 0 - see the token-usage Field.Of comments
// above for why zero and absent must not collapse to the same emitted value.
func positiveItoa(n int) string {
	if n <= 0 {
		return ""
	}
	return strconv.Itoa(n)
}

var byKey = func() map[string]Field {
	m := make(map[string]Field, len(registry))
	for _, f := range registry {
		if _, dup := m[f.Key]; dup {
			panic("attr: duplicate field key " + f.Key)
		}
		m[f.Key] = f
	}
	return m
}()

// Fields returns the whole contract, for tests and for documentation generation.
func Fields() []Field { return append([]Field(nil), registry...) }

// Lookup returns a field by key.
func Lookup(key string) (Field, bool) {
	f, ok := byKey[key]
	return f, ok
}

// DefaultLabels are the Loki stream keys shipped out of the box.
//
// Minimal on purpose. Every label is a stream multiplier, and per_stream_rate_limit is
// 3MB/s per stream - so the cost of a label is paid twice, once in series and once in
// how quickly a burst of large tool outputs gets rejected. Everything not listed here
// is structured metadata, which LogQL still filters on without keying a stream.
var DefaultLabels = []string{ServiceName, Family, RecordType}

// ValidateLabels reports whether a configured label set may be promoted to Loki stream
// keys. Only bounded fields qualify; promoting an identity field would create a stream
// per thread.
func ValidateLabels(keys []string) error {
	for _, k := range keys {
		switch k {
		case ServiceName, RecordType:
			continue // owned by this package, always bounded
		}
		f, ok := Lookup(k)
		if !ok {
			return fmt.Errorf("loki label %q is not an attribute this service emits; "+
				"see internal/attr for the full set", k)
		}
		if f.Class != Bounded {
			return fmt.Errorf("loki label %q is %s, not bounded: promoting it would key "+
				"a Loki stream per distinct value", k, f.Class)
		}
	}
	return nil
}

func (c Class) String() string {
	switch c {
	case Bounded:
		return "bounded"
	case Identity:
		return "identity"
	case Sensitive:
		return "sensitive"
	}
	return "unknown"
}

// Guard enforces the caps at emit time and counts what it rejected.
//
// Safe for concurrent use: sinks run on their own goroutines and all share one guard,
// which is the point - the cap is per field across the whole process, not per sink.
type Guard struct {
	mu       sync.Mutex
	seen     map[string]map[string]struct{}
	rejected map[string]int64
}

// NewGuard returns a guard with empty state. A restart forgets which values it has
// seen, which is correct: the cap protects against unbounded growth within a process,
// and a fresh process re-learns the real value set within seconds of traffic.
func NewGuard() *Guard {
	return &Guard{
		seen:     map[string]map[string]struct{}{},
		rejected: map[string]int64{},
	}
}

// value applies the cap, returning the value to emit.
func (g *Guard) value(f Field, v string) string {
	if v == "" || f.Class != Bounded || f.Cap <= 0 {
		return v
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	vals := g.seen[f.Key]
	if vals == nil {
		vals = map[string]struct{}{}
		g.seen[f.Key] = vals
	}
	if _, known := vals[v]; known {
		return v
	}
	if len(vals) >= f.Cap {
		g.rejected[f.Key]++
		return OtherValue
	}
	vals[v] = struct{}{}
	return v
}

// Rejected reports how many values were collapsed to OtherValue, by field. Exposed as
// a metric: a guard that is quietly rewriting every model name is a broken pipeline,
// and without this it looks like a healthy one.
func (g *Guard) Rejected() map[string]int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make(map[string]int64, len(g.rejected))
	maps.Copy(out, g.rejected)
	return out
}

// Distinct reports how many values each bounded field has taken, for self-observability.
func (g *Guard) Distinct() map[string]int {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make(map[string]int, len(g.seen))
	for k, v := range g.seen {
		out[k] = len(v)
	}
	return out
}

// MetricAttrs builds the attribute set for a metric datapoint: every bounded field
// that applies, plus the two the GenAI convention requires on all its instruments.
//
// Emitters that want fewer - a high-frequency counter that does not need the client
// binary, say - narrow with Only. They must not widen: anything not returned here is
// not on the contract.
func (g *Guard) MetricAttrs(t *turn.Turn) []KV {
	out := make([]KV, 0, len(registry))
	out = append(out,
		KV{GenAIProvider, GenAIProviderValue},
		// Turn-derived since issue #32 (generateText vs streamText), but still
		// injected here rather than registered as a Field: it is not one of Turn's own
		// attributes, it is this emitter's classification of the response, and the
		// tool-call spans override it with their own operation value. A registry entry
		// would put it in SpanAttrs and so onto every span, where those overrides
		// would then be appending a second, contradictory copy of the same key.
		KV{GenAIOperation, OperationName(t)},
	)
	for _, f := range registry {
		if f.Class != Bounded || f.Of == nil {
			continue
		}
		if v := g.value(f, f.Of(t)); v != "" {
			out = append(out, KV{f.Key, v})
		}
	}
	return out
}

// Labels builds the Loki stream labels for one record.
//
// promoted is the configured label set; anything in it that is not bounded was already
// rejected by ValidateLabels at startup, so a mistake here fails the process rather
// than quietly creating a stream per thread.
func (g *Guard) Labels(t *turn.Turn, serviceName, recordType string, promoted []string) []KV {
	want := make(map[string]bool, len(promoted))
	for _, k := range promoted {
		want[k] = true
	}
	out := make([]KV, 0, len(promoted))
	if want[ServiceName] {
		out = append(out, KV{ServiceName, serviceName})
	}
	if want[RecordType] {
		out = append(out, KV{RecordType, recordType})
	}
	for _, f := range registry {
		if f.Class != Bounded || f.Of == nil || !want[f.Key] {
			continue
		}
		if v := g.value(f, f.Of(t)); v != "" {
			out = append(out, KV{f.Key, v})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Metadata builds Loki structured metadata: everything not promoted to a label,
// including the identity fields that make a single response findable.
//
// This is what makes "why did this call use this model" answerable - the response id,
// the thread, the turn and its parent are all here, queryable, without any of them
// keying a stream.
func (g *Guard) Metadata(t *turn.Turn, promoted []string) []KV {
	label := make(map[string]bool, len(promoted))
	for _, k := range promoted {
		label[k] = true
	}
	out := make([]KV, 0, len(registry))
	for _, f := range registry {
		if label[f.Key] || f.Of == nil {
			continue // a stream label already, or not Turn-derived at all
		}
		v := f.Of(t)
		if v == "" {
			continue
		}
		if f.Class == Bounded {
			v = g.value(f, v)
		}
		out = append(out, KV{f.Key, v})
	}
	return out
}

// SpanAttrs builds span attributes: bounded and identity fields, never sensitive ones.
//
// Sensitive is excluded here and not only from metrics because Tempo indexes span
// attributes for search, so a span attribute is a lookup key in a way structured
// metadata is not.
func (g *Guard) SpanAttrs(t *turn.Turn) []KV {
	out := make([]KV, 0, len(registry))
	for _, f := range registry {
		if f.Class == Sensitive || f.Of == nil {
			continue
		}
		v := f.Of(t)
		if v == "" {
			continue
		}
		if f.Class == Bounded {
			v = g.value(f, v)
		}
		out = append(out, KV{f.Key, v})
	}
	return out
}

// Only narrows an attribute set to the named keys, preserving order.
func Only(kvs []KV, keys ...string) []KV {
	want := make(map[string]bool, len(keys))
	for _, k := range keys {
		want[k] = true
	}
	out := make([]KV, 0, len(keys))
	for _, kv := range kvs {
		if want[kv.Key] {
			out = append(out, kv)
		}
	}
	return out
}

// With appends one-off attributes that are not Turn-derived - a token type on a token
// counter, a tool name on a tool counter.
//
// A method on Guard, not a bare function, because these values need capping just as
// much as the Turn-derived ones and are in fact the likelier source of an explosion:
// a tool name is whatever the model's tool catalogue happens to contain. As a bare
// function this was the one path around the guard, and it took two of the three sinks
// with it. A key that is not on the contract at all is dropped and counted.
func (g *Guard) With(kvs []KV, extra ...KV) []KV {
	out := make([]KV, 0, len(kvs)+len(extra))
	out = append(out, kvs...)
	for _, kv := range extra {
		if kv.Value == "" {
			continue
		}
		f, known := byKey[kv.Key]
		// Sensitive is rejected here too, not only omitted from SpanAttrs/MetricAttrs/
		// Labels: those three enforce the class by construction (they walk the
		// registry and skip Sensitive themselves), but With takes an arbitrary caller
		// KV and would otherwise happily cap-and-forward a Sensitive key straight past
		// that enforcement - the one path a sink could use to put safety_identifier on
		// a span. Nothing does that today; this closes the path before something does.
		if !known || f.Class == Sensitive {
			g.mu.Lock()
			g.rejected[kv.Key]++
			g.mu.Unlock()
			continue
		}
		out = append(out, KV{kv.Key, g.value(f, kv.Value)})
	}
	return out
}
