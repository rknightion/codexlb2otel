package otlpmetric

import (
	"context"
	"math"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"

	"github.com/rknightion/codexlb2otel/internal/attr"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

// requestKindTurn is the RequestKind value that means "a user asked for this turn".
// codex-lb also emits "prewarm" (a speculative warmup that does no engine work) and
// "compaction" (the server compacting context) under the same field - see attr.go's
// Observed list for attr.RequestKind, which is where this value comes from. Counting
// either of the other two as a turn overstates turn rates and understates cost per
// turn (issue #7's acceptance criterion), so every turn-shaped instrument below
// filters on this constant rather than "not empty".
const requestKindTurn = "turn"

// InstrumentAttributeSet is one emitted metric series key before the SDK aggregates
// measurements with identical attributes. It is exported for clbstat's corpus survey:
// the command counts these sets without having to know each recordX function's
// narrowing rules itself.
type InstrumentAttributeSet struct {
	Instrument string
	Attributes []attr.KV
}

// record turns one reduced Turn into every measurement it carries.
//
// base is fetched once via the shared guard and reused - every instrument below
// either takes it as-is or narrows/extends it with attr.Only and attr.With, per the
// package doc comment on those functions. Nothing here reads a Turn field straight
// into an attribute.KeyValue: if a dimension is not already on the contract, it does
// not become one just because this sink would find it convenient.
func (s *Sink) record(ctx context.Context, t *turn.Turn) {
	base := s.guard.MetricAttrs(t)

	s.recordCounts(ctx, t, base)
	s.recordTokens(ctx, t, base)
	s.recordCost(ctx, t, base)
	s.recordEngineCalls(ctx, t, base)
	s.recordToolCalls(ctx, t, base)
	s.recordToolCallsPerOperation(ctx, t, base)
	s.recordDurations(ctx, t, base)
	s.recordEngineTimingDeltas(ctx, t, base)
	s.recordTBT(ctx, t, base)
	s.recordRateLimits(ctx, t, base)
}

// recordCounts handles every plain per-response counter that is not tokens, engine
// calls or tool calls - each of those has enough of its own logic (fan-out, a
// preferred-source fallback) to earn its own function.
func (s *Sink) recordCounts(ctx context.Context, t *turn.Turn, base []attr.KV) {
	s.inst.responses.Add(ctx, 1, otelmetric.WithAttributes(toOtel(responseCounterAttrs(base))...))

	if t.RequestKind == requestKindTurn {
		s.inst.turns.Add(ctx, 1, otelmetric.WithAttributes(toOtel(responseCounterAttrs(base))...))
	}

	if t.WebSearchRequests > 0 {
		attrs := webSearchAttrs(base)
		s.inst.webSearch.Add(ctx, int64(t.WebSearchRequests), otelmetric.WithAttributes(toOtel(attrs)...))
	}
	if t.ImageGenTokens > 0 {
		attrs := imageGenTokenAttrs(base)
		s.inst.imageGenTokens.Add(ctx, int64(t.ImageGenTokens), otelmetric.WithAttributes(toOtel(attrs)...))
	}

	// StatusError is the SERVER rejecting a request with a reason; StatusTransport is
	// the connection itself failing. Both are "errors" for alerting purposes but the
	// two want different runbooks, which is exactly why error.type/codexlb.error_code
	// (for StatusError) and codexlb.close_code (for StatusTransport) are carried
	// separately rather than folded into one undifferentiated total.
	if t.Status == turn.StatusError {
		attrs := errorCounterAttrs(base)
		s.inst.errors.Add(ctx, 1, otelmetric.WithAttributes(toOtel(attrs)...))
	}
	if t.Status == turn.StatusTransport || t.CloseCode != nil || t.FrameErrors > 0 {
		// FrameType is what makes this counter readable, and issue #17 is the reason:
		// 18 of the corpus's 20 transport events are an error frame with NO close code
		// at all ("no close frame received or sent"), 1 is a 1012 service restart and 1
		// is a clean 1000. Attributed by close_code alone the 18 collapse into a single
		// bucket whose only distinguishing feature is an absent attribute, which reads
		// on a dashboard as missing data rather than as the thing it actually is - a
		// connection dropped without a handshake. close|error is the axis that separates
		// "OpenAI restarted the backend under us" from "the connection died", and those
		// two want different runbooks.
		attrs := transportEventAttrs(base)
		s.inst.transportEvents.Add(ctx, 1, otelmetric.WithAttributes(toOtel(attrs)...))
	}
	if t.SafetyBuffering {
		// GenAIResponseModel differs from GenAIRequestModel exactly when safety
		// buffering re-ran the response through a different model (see attr.go's
		// GenAIResponseModel.Of) - both are worth keeping on this one.
		attrs := safetyBufferingAttrs(base)
		s.inst.safetyBuffering.Add(ctx, 1, otelmetric.WithAttributes(toOtel(attrs)...))
	}
	if t.BaselineReset {
		attrs := baselineResetAttrs(base)
		s.inst.baselineResets.Add(ctx, 1, otelmetric.WithAttributes(toOtel(attrs)...))
	}
}

// tokenTypes is every value the "total" candidate in issue #7 is deliberately NOT
// among: names.go's TokenInput/Output/Reasoning/Cached/CacheWrite constants are the
// whole set the contract defines for gen_ai.token.type, and there is no TokenTotal.
// Emitting a sixth "total" value on this same axis would double-count under any
// dashboard that naively sums codexlb_tokens_total across every value of
// gen_ai.token.type - reasoning and cached are already breakdowns NESTED inside
// output and input respectively, not additive siblings, so that trap exists even
// among the five real values and adding a sixth would make it worse, not better.
// input+output already gives the total this service bills against.
var tokenTypes = []struct {
	kind  string
	value func(*turn.Turn) int
}{
	{attr.TokenInput, func(t *turn.Turn) int { return t.InputTokens }},
	{attr.TokenOutput, func(t *turn.Turn) int { return t.OutputTokens }},
	{attr.TokenReasoning, func(t *turn.Turn) int { return t.ReasoningTokens }},
	{attr.TokenCacheRead, func(t *turn.Turn) int { return t.CachedTokens }},
	{attr.TokenCacheWrite, func(t *turn.Turn) int { return t.CacheWriteTokens }},
}

// recordTokens sums each token-type breakdown. These are per-response usage figures,
// already safe to sum as-is (turn.go's own comment on InputTokens etc.) - unlike
// engine calls or tool-pause time, there is no cumulative-logical-turn source to
// prefer here and no delta arithmetic involved.
//
// Records into BOTH tokens (the codexlb.* counter) and tokenUsage (the
// gen_ai.client.token.usage histogram), with the same value - see names.go's
// MetricTokenUsage doc comment for why this is a deliberate parallel instrument rather
// than a replacement.
//
// The two instruments took IDENTICAL attribute sets until issue #32 and now
// deliberately do not. Three attributes still go on the convention-named histogram
// alone:
//
//   - gen_ai.agent.name / gen_ai.agent.version. gen_ai.client.token.usage is one of
//     the three instruments Grafana agent observability reads, and its Agents table
//     groups by the agent name - without it, every series this service emitted sat in
//     that UI's "anonymous" bucket, which is the bug #32 opened on.
//   - gen_ai.token.semantics. Absent, the same dashboard classifies these series as
//     provider-raw and adds cache_read and cache_write ON TOP of input, over-counting
//     tokens and cost on every cached prompt. See attr.TokenSemanticsInclusive.
//
// CXO-0003 adds codexlb.family to both token instruments because it is the only probe
// discriminator, and adds reasoning effort, thread source and API key name to the two
// token instruments because token and cost shape are the panels that query them.
// codexlb.tokens still deliberately drops the agent labels and token semantics marker
// that only the convention-named histogram's consumer needs: agent version multiplies
// by system-prompt hash, and token semantics has no counter-side query.
//
// The instruments stay parallel in what they MEASURE, which is what
// names.go's MetricTokenUsage doc comment means by a deliberate parallel: same value,
// same fan-out over token types, different instrument type. Not the same attributes.
func (s *Sink) recordTokens(ctx context.Context, t *turn.Turn, base []attr.KV) {
	narrowed := tokenCounterAttrs(base)
	forSigil := tokenUsageAttrs(base)
	for _, tt := range tokenTypes {
		v := tt.value(t)
		if v <= 0 {
			continue
		}
		kind := attr.KV{Key: attr.GenAITokenType, Value: tt.kind}
		s.inst.tokens.Add(ctx, int64(v),
			otelmetric.WithAttributes(toOtel(s.guard.With(narrowed, kind))...))
		s.inst.tokenUsage.Record(ctx, int64(v),
			otelmetric.WithAttributes(toOtel(s.guard.With(forSigil, kind,
				attr.KV{Key: attr.GenAITokenSemantics, Value: attr.TokenSemanticsInclusive},
			))...))
	}
}

// recordCost records codex-lb's own computed charge as the aggregation surface for
// cost. It deliberately uses the same model/family/effort/thread/API-key shape as the
// token instruments, minus gen_ai.token.type because a response cost is not split by
// token bucket. Duration-only labels such as service tier and critical-path coverage
// are dropped because they would multiply cost series without changing the accounting
// question this counter answers.
func (s *Sink) recordCost(ctx context.Context, t *turn.Turn, base []attr.KV) {
	if t.CostUSD == nil || *t.CostUSD < 0 || math.IsNaN(*t.CostUSD) || math.IsInf(*t.CostUSD, 0) || !s.firstCostResponse(t.ResponseID) {
		return
	}
	s.inst.costUSD.Add(ctx, *t.CostUSD, otelmetric.WithAttributes(toOtel(costAttrs(base))...))
}

// recordEngineCalls prefers critical_path.engine_calls, which is genuinely
// per-response, over the cumulative-delta arithmetic - see turn.CriticalPath's doc
// comment. It falls back to the delta only when critical_path was not populated for
// this response at all (Coverage == ""), which on the corpus is about 4.6% of
// responses (critical_path.coverage = missing_harness_boundary, or absent entirely).
func (s *Sink) recordEngineCalls(ctx context.Context, t *turn.Turn, base []attr.KV) {
	calls := t.EngineCallsDelta
	if t.CriticalPath.Coverage != "" {
		calls = t.CriticalPath.EngineCalls
	}
	if calls <= 0 {
		return
	}
	attrs := engineCallAttrs(base)
	s.inst.engineCalls.Add(ctx, int64(calls), otelmetric.WithAttributes(toOtel(attrs)...))
}

// recordToolCalls fans one Turn out into one Add per tool invocation, tagged with the
// tool's own name (gen_ai.tool.name, renamed from codexlb.tool_name by issue #18).
// ToolName is a names.go constant, capped via Guard.With exactly as attr.With's own
// doc comment describes ("a tool name on a tool counter") - the gap this comment used
// to flag (ToolName registered but not actually capped) was closed when it gained a
// registry entry; see internal/attr/attr.go's registry.
func (s *Sink) recordToolCalls(ctx context.Context, t *turn.Turn, base []attr.KV) {
	if len(t.ToolCalls) == 0 {
		return
	}
	narrowed := toolCallAttrs(base)
	for _, tc := range t.ToolCalls {
		if tc.Name == "" {
			continue
		}
		attrs := s.guard.With(narrowed, attr.KV{Key: attr.ToolName, Value: tc.Name})
		s.inst.toolCalls.Add(ctx, 1, otelmetric.WithAttributes(toOtel(attrs)...))
	}
}

// recordToolCallsPerOperation records len(Turn.ToolCalls) once per response,
// for every model-backed response, including the zero case, unlike recordToolCalls'
// own early return. Model-less transport records cannot be SDK Generations and are
// excluded. The two answer different questions: recordToolCalls counts invocations, by
// tool name, and a response with none has nothing to fan out; this one measures the
// SHAPE of tool use across every response, and excluding the (common) zero-tool-call
// case would bias that distribution upward and hide how much of the traffic never
// touches a tool at all.
func (s *Sink) recordToolCallsPerOperation(ctx context.Context, t *turn.Turn, base []attr.KV) {
	if strings.TrimSpace(t.Model) == "" {
		return
	}
	attrs := toolCallsPerOperationAttrs(base)
	s.inst.toolCallsPerOperation.Record(ctx, int64(len(t.ToolCalls)), otelmetric.WithAttributes(toOtel(attrs)...))
}

// recordDurations converts every millisecond field this sink emits into seconds
// before recording it - names.go documents each of these histograms with unit "s"
// per the GenAI convention (gen_ai.client.operation.duration is defined in seconds),
// and the Turn/CriticalPath structs carry milliseconds throughout. Silently recording
// the millisecond value under a seconds-named instrument would be wrong by 1000x in a
// way that is not obviously wrong on a dashboard - a histogram of plausible-looking
// numbers, just the wrong unit.
func (s *Sink) recordDurations(ctx context.Context, t *turn.Turn, base []attr.KV) {
	// operationDuration: convention-compliant gen_ai.client.operation.duration - the
	// server's own round trip, independent of how long the client waited.
	if !t.ServerCreatedAt.IsZero() && !t.ServerCompletedAt.IsZero() {
		if d := t.ServerCompletedAt.Sub(t.ServerCreatedAt).Seconds(); d >= 0 {
			// error.type joins the set in issue #32. It was already a registry field
			// and already on spans and Loki metadata, but narrowed out HERE - and this
			// is the one instrument agent-observability splits success from failure on
			// (its error-rate, error-count and error-by-type panels all select
			// error_type!="" on gen_ai.client.operation.duration), so every one of them
			// read zero. Field.Of returns "" for a turn that did not error, so the
			// label is simply absent there, which is what error_type="" matches.
			//
			// gen_ai.agent.name/version for the same reason as recordTokens above -
			// this is the instrument that drives the Agents table's request, error and
			// latency columns.
			// ServiceTierRequested joins the three latency sets (here, turnDuration
			// and ttft below) and nowhere else. It is the only place the tier can pay
			// for itself: the whole reason to ask for priority is latency, and both
			// tier attributes were narrowed out of every duration instrument, so the
			// contract could say which tier a response ran under while no instrument
			// could say whether it made any difference. Its value set is at most one
			// per deployment at a time, so it costs ~nothing beyond the changeover.
			attrs := operationDurationAttrs(base)
			s.inst.operationDuration.Record(ctx, d, otelmetric.WithAttributes(toOtel(attrs)...))
		}
	}

	// turnDuration: client turn_started_at_unix_ms to server completion - "the only
	// figure that measures what the user actually waited for" (issue #7). Restricted
	// to real turns: prewarm and compaction have no user waiting on them.
	if t.RequestKind == requestKindTurn && !t.TurnStart.IsZero() && !t.ServerCompletedAt.IsZero() {
		if d := t.ServerCompletedAt.Sub(t.TurnStart).Seconds(); d >= 0 {
			attrs := turnDurationAttrs(base)
			s.inst.turnDuration.Record(ctx, d, otelmetric.WithAttributes(toOtel(attrs)...))
		}
	}

	if t.TTFTMs > 0 {
		attrs := ttftAttrs(base)
		s.inst.ttft.Record(ctx, msToS(t.TTFTMs), otelmetric.WithAttributes(toOtel(attrs)...))
	}

	// The remaining four are critical_path's per-response breakdown, preferred over
	// any point-in-time or delta equivalent on Turn itself wherever critical_path was
	// populated (Coverage != "") - same rule as recordEngineCalls. CriticalPathCoverage
	// rides along on these specifically (not on every instrument) because it is a
	// verdict on critical_path's OWN trustworthiness, and these are the numbers it is
	// a verdict about; a dashboard wanting only trustworthy numbers filters on it.
	cpAttrs := criticalPathDurationAttrs(base)

	if t.CriticalPath.EngineWallMs > 0 {
		s.inst.engineWall.Record(ctx, msToS(t.CriticalPath.EngineWallMs), otelmetric.WithAttributes(toOtel(cpAttrs)...))
	}
	if t.CriticalPath.HarnessUnblockedMs > 0 {
		s.inst.harnessUnblocked.Record(ctx, msToS(t.CriticalPath.HarnessUnblockedMs), otelmetric.WithAttributes(toOtel(cpAttrs)...))
	}
	if t.CriticalPath.SamplingStreamMs > 0 {
		s.inst.samplingStream.Record(ctx, msToS(t.CriticalPath.SamplingStreamMs), otelmetric.WithAttributes(toOtel(cpAttrs)...))
	}

	preInference := t.PreInferenceMs
	if t.CriticalPath.Coverage != "" {
		preInference = t.CriticalPath.PreInferenceMs
	}
	if preInference > 0 {
		s.inst.preInference.Record(ctx, msToS(preInference), otelmetric.WithAttributes(toOtel(cpAttrs)...))
	}

	clientToolPause := t.ClientToolPauseMsDelta
	if t.CriticalPath.Coverage != "" {
		clientToolPause = t.CriticalPath.ClientToolPauseMs
	}
	if clientToolPause > 0 {
		s.inst.clientToolPause.Record(ctx, msToS(clientToolPause), otelmetric.WithAttributes(toOtel(cpAttrs)...))
	}
}

// recordEngineTimingDeltas emits issue #23's eight millisecond deltas plus the
// uncached-prompt-token delta - all cumulative-logical-turn counters from the same
// timing_metrics block as EngineCallsDelta, diffed the identical way (reducer.go's own
// comment on the nine fields), so they share recordEngineCalls' attribute set and its
// BaselineReset caveat: a response with no prior baseline reports an upper bound that
// absorbed unobserved turn history, not a clean per-response measurement.
//
// engine_service_* and engine_iapi_* are TWO SEPARATE PAIRS OF INSTRUMENTS, not one
// instrument with a "domain" attribute distinguishing service from iapi - issue #23's
// own acceptance criteria rule out a new attribute key, and this package's existing
// style is already one instrument per measurement (every CriticalPath field above has
// its own histogram, never a shared one keyed by "which phase"). Comparing
// engine_service_inference against engine_iapi_inference in one PromQL query is no
// harder with two metric names than with one name and a label.
//
// Every field here is gated `> 0`, matching every other delta-sourced field in this
// file (e.g. recordEngineCalls, the critical-path fallbacks in recordDurations): the
// reducer's own test (TestReducer_NoNegativeDeltas, internal/turn) proves these never
// go negative, so a zero can only mean "the json:omitempty tag dropped it because
// nothing happened in this delta window", not a real negative reading - unlike the TBT
// fields below, which get `!= 0` for exactly the opposite reason.
func (s *Sink) recordEngineTimingDeltas(ctx context.Context, t *turn.Turn, base []attr.KV) {
	attrs := engineTimingDurationAttrs(base)
	opt := otelmetric.WithAttributes(toOtel(attrs)...)

	for _, d := range []struct {
		inst otelmetric.Float64Histogram
		ms   float64
	}{
		{s.inst.engineServiceInference, t.EngineServiceInferenceMsDelta},
		{s.inst.engineServiceSampling, t.EngineServiceSamplingMsDelta},
		{s.inst.engineIapiInference, t.EngineIapiInferenceMsDelta},
		{s.inst.engineIapiSampling, t.EngineIapiSamplingMsDelta},
		{s.inst.responsesExclEngineAndTool, t.ResponsesExclEngineAndToolMsDelta},
		{s.inst.responsesExclEngineWaitSampling, t.ResponsesExclEngineWaitSamplingMsDelta},
		{s.inst.responsesExclEngineWaitSamplingIapi, t.ResponsesExclEngineWaitSamplingIapiMsDelta},
		{s.inst.responsesAPIExclClientTools, t.ResponsesAPIExclClientToolsMsDelta},
	} {
		if d.ms > 0 {
			d.inst.Record(ctx, msToS(d.ms), opt)
		}
	}

	// EngineUncachedPromptTokensDelta deliberately does NOT join gen_ai.token.type as a
	// sixth value, for two independent reasons - either alone would be enough:
	//
	//  1. Same nested-breakdown trap tokenTypes' own doc comment already flags for
	//     "cached": uncached = input - cached is a breakdown NESTED inside the input
	//     bucket, not an additive sibling of it. A naive `sum by () (codexlb_tokens)`
	//     across every gen_ai.token.type value already only works because the five
	//     real values are input+output+reasoning+cached+cache_write with
	//     reasoning/cached/cache_write being sub-buckets a careful query excludes;
	//     adding uncached (= input - cached) on the same axis would double-count
	//     against input for anyone who does NOT know to exclude it, exactly like a
	//     naive "cached" inclusion already would.
	//  2. It is not even the SAME MEASUREMENT FAMILY as the other five. TokenInput,
	//     TokenCached etc. are built from Turn.InputTokens/CachedTokens/... - the
	//     per-response response.completed usage fields recordTokens' own doc comment
	//     calls "already safe to sum as-is". EngineUncachedPromptTokensDelta instead
	//     comes from timing_metrics' cumulative engine_uncached_prompt_tokens_total,
	//     diffed against a baseline exactly like EngineCallsDelta - it is
	//     BaselineReset-affected and can be an upper bound, which none of the other
	//     five gen_ai.token.type values are. Mixing a baseline-affected engine-side
	//     count into an axis whose other five values are all clean per-response usage
	//     numbers would let one bad baseline-reset response corrupt an aggregate a
	//     reader has no reason to think is baseline-sensitive at all.
	//
	// So: its own counter, entirely off the token.type axis - MetricEngineUncachedPromptTokens.
	if t.EngineUncachedPromptTokensDelta > 0 {
		tokenOpt := otelmetric.WithAttributes(toOtel(engineUncachedPromptTokenAttrs(base))...)
		s.inst.engineUncachedPromptTokens.Add(ctx, int64(t.EngineUncachedPromptTokensDelta), tokenOpt)
	}
}

// recordTBT records the three time-between-tokens fields exactly as reported, per
// response - never diffed, per turn.go's own comment on why delta-ing a running average
// would corrupt the arithmetic (unlike every field recordEngineTimingDeltas handles
// above, all of which ARE deltas).
//
// EngineServiceTBTMs and EngineIapiTBTMs are gated `> 0`, same as every other
// non-negative duration field in this file. EngineServiceMinusIapiTBTMs is gated `!= 0`
// instead - the one deliberate exception in this package - because it is a difference
// of two averages and DOES go negative (measured min -6.70ms in the corpus), so `> 0`
// would silently drop every negative reading before it ever reached the histogram built
// specifically to hold them (instruments.go's negativeCapableTBTBoundaries). `!= 0`
// still cannot distinguish a true zero from an unpopulated response (Turn's
// json:omitempty collapses both to the Go zero value the same way every float64 field
// here does), but that ambiguity already exists on every gated field in this file;
// what matters here is that a NEGATIVE reading is never mistaken for "no data" the way
// `> 0` would mistake it.
func (s *Sink) recordTBT(ctx context.Context, t *turn.Turn, base []attr.KV) {
	attrs := tbtAttrs(base)
	opt := otelmetric.WithAttributes(toOtel(attrs)...)

	if t.EngineServiceTBTMs > 0 {
		s.inst.engineServiceTBT.Record(ctx, msToS(t.EngineServiceTBTMs), opt)
	}
	if t.EngineIapiTBTMs > 0 {
		s.inst.engineIapiTBT.Record(ctx, msToS(t.EngineIapiTBTMs), opt)
	}
	if t.EngineServiceMinusIapiTBTMs != 0 {
		s.inst.engineServiceMinusIapiTBT.Record(ctx, msToS(t.EngineServiceMinusIapiTBTMs), opt)
	}
}

// recordRateLimits records the rate-limit and credit gauges.
//
// Every one of them is gated on t.AccountID != "" and never called without it: this
// is the enforcement point for issue #7's acceptance criterion that these gauges
// never carry any meaning aggregated across accounts. Grouping them by anything else
// alone would average away exactly the exhaustion a load balancer needs to see - see
// attr.AccountID's doc comment.
func (s *Sink) recordRateLimits(ctx context.Context, t *turn.Turn, base []attr.KV) {
	if t.AccountID == "" {
		return
	}
	accountAttrs := rateLimitAttrs(base)

	// Presence, not value, is what has to be inferred here: the reducer leaves these
	// fields at their Go zero value when the response carried no rate-limit block at
	// all (see reducer.go's applyRateLimits), which is indistinguishable from a
	// genuine "0 minutes" or "0%" reading field-by-field. WindowMinutes is the
	// reliable signal - it is a real plan configuration value the server would never
	// send as literally zero - so it gates whether the window's own gauges fire.
	if t.RateLimitWindowMin > 0 {
		s.inst.rateLimitUsed.Record(ctx, t.RateLimitUsedPercent, otelmetric.WithAttributes(toOtel(accountAttrs)...))
		s.inst.rateLimitReset.Record(ctx, t.RateLimitResetSeconds, otelmetric.WithAttributes(toOtel(accountAttrs)...))
	}
	if t.RateLimit2WindowMin > 0 {
		s.inst.rateLimitUsed2.Record(ctx, t.RateLimit2UsedPercent, otelmetric.WithAttributes(toOtel(accountAttrs)...))
	}

	// ExtraRateLimits is keyed by model name and, per turn.go's own doc comment,
	// "bounded by the models on the plan" - already asserted safe to fan out, so
	// unlike gen_ai.tool.name (recordToolCalls, capped at 64 with no such upstream
	// guarantee) this one genuinely needs no extra cap here. gen_ai.request.model is
	// dropped from accountAttrs before adding it back from the map key, so each
	// measurement carries exactly one value for that key rather than two.
	for model, pct := range t.ExtraRateLimits {
		if model == "" {
			continue
		}
		attrs := s.guard.With(accountAttrs, attr.KV{Key: attr.GenAIRequestModel, Value: model})
		s.inst.rateLimitPerModel.Record(ctx, pct, otelmetric.WithAttributes(toOtel(attrs)...))
	}

	if !t.HasCredits && !t.CreditsUnlimited && t.CreditsBalance == "" {
		return // no credits block on this response at all
	}
	s.inst.creditsUnlimited.Record(ctx, boolToFloat(t.CreditsUnlimited), otelmetric.WithAttributes(toOtel(accountAttrs)...))
	if bal, ok := parseCreditsBalance(t.CreditsBalance); ok {
		s.inst.creditsBalance.Record(ctx, bal, otelmetric.WithAttributes(toOtel(accountAttrs)...))
	}
}

// AttributeSetsForTurn returns the metric attribute sets this package would emit for
// t, without recording any measurement values. clbstat uses it to measure corpus
// cardinality against the sink's own selector contract rather than a hand-copied
// approximation.
func AttributeSetsForTurn(t *turn.Turn, guard *attr.Guard) []InstrumentAttributeSet {
	base := guard.MetricAttrs(t)
	return attributeSetsForTurn(t, guard, base)
}

func attributeSetsForTurn(t *turn.Turn, guard *attr.Guard, base []attr.KV) []InstrumentAttributeSet {
	var out []InstrumentAttributeSet
	add := func(name string, attrs []attr.KV) {
		out = append(out, InstrumentAttributeSet{Instrument: name, Attributes: attrs})
	}

	add(attr.MetricResponses, responseCounterAttrs(base))
	if t.RequestKind == requestKindTurn {
		add(attr.MetricTurns, responseCounterAttrs(base))
	}
	if t.WebSearchRequests > 0 {
		add(attr.MetricWebSearch, webSearchAttrs(base))
	}
	if t.ImageGenTokens > 0 {
		add(attr.MetricImageGenTokens, imageGenTokenAttrs(base))
	}
	if t.Status == turn.StatusError {
		add(attr.MetricErrors, errorCounterAttrs(base))
	}
	if t.Status == turn.StatusTransport || t.CloseCode != nil || t.FrameErrors > 0 {
		add(attr.MetricTransportEvents, transportEventAttrs(base))
	}
	if t.SafetyBuffering {
		add(attr.MetricSafetyBuffering, safetyBufferingAttrs(base))
	}
	if t.BaselineReset {
		add(attr.MetricBaselineResets, baselineResetAttrs(base))
	}

	tokenBase := tokenCounterAttrs(base)
	usageBase := tokenUsageAttrs(base)
	for _, tt := range tokenTypes {
		if tt.value(t) <= 0 {
			continue
		}
		kind := attr.KV{Key: attr.GenAITokenType, Value: tt.kind}
		add(attr.MetricTokens, guard.With(tokenBase, kind))
		add(attr.MetricTokenUsage, guard.With(usageBase, kind,
			attr.KV{Key: attr.GenAITokenSemantics, Value: attr.TokenSemanticsInclusive},
		))
	}
	if t.CostUSD != nil {
		add(attr.MetricCostUSD, costAttrs(base))
	}

	calls := t.EngineCallsDelta
	if t.CriticalPath.Coverage != "" {
		calls = t.CriticalPath.EngineCalls
	}
	if calls > 0 {
		add(attr.MetricEngineCalls, engineCallAttrs(base))
	}

	if len(t.ToolCalls) > 0 {
		narrowed := toolCallAttrs(base)
		for _, tc := range t.ToolCalls {
			if tc.Name == "" {
				continue
			}
			add(attr.MetricToolCalls, guard.With(narrowed, attr.KV{Key: attr.ToolName, Value: tc.Name}))
		}
	}
	if strings.TrimSpace(t.Model) != "" {
		add(attr.MetricToolCallsPerOperation, toolCallsPerOperationAttrs(base))
	}

	if !t.ServerCreatedAt.IsZero() && !t.ServerCompletedAt.IsZero() && t.ServerCompletedAt.Sub(t.ServerCreatedAt).Seconds() >= 0 {
		add(attr.MetricOperationDuration, operationDurationAttrs(base))
	}
	if t.RequestKind == requestKindTurn && !t.TurnStart.IsZero() && !t.ServerCompletedAt.IsZero() && t.ServerCompletedAt.Sub(t.TurnStart).Seconds() >= 0 {
		add(attr.MetricTurnDuration, turnDurationAttrs(base))
	}
	if t.TTFTMs > 0 {
		add(attr.MetricTTFT, ttftAttrs(base))
	}

	cpAttrs := criticalPathDurationAttrs(base)
	if t.CriticalPath.EngineWallMs > 0 {
		add(attr.MetricEngineWall, cpAttrs)
	}
	if t.CriticalPath.HarnessUnblockedMs > 0 {
		add(attr.MetricHarnessUnblocked, cpAttrs)
	}
	if t.CriticalPath.SamplingStreamMs > 0 {
		add(attr.MetricSamplingStream, cpAttrs)
	}
	preInference := t.PreInferenceMs
	if t.CriticalPath.Coverage != "" {
		preInference = t.CriticalPath.PreInferenceMs
	}
	if preInference > 0 {
		add(attr.MetricPreInference, cpAttrs)
	}
	clientToolPause := t.ClientToolPauseMsDelta
	if t.CriticalPath.Coverage != "" {
		clientToolPause = t.CriticalPath.ClientToolPauseMs
	}
	if clientToolPause > 0 {
		add(attr.MetricClientToolPause, cpAttrs)
	}

	timingAttrs := engineTimingDurationAttrs(base)
	for _, d := range []struct {
		name string
		ms   float64
	}{
		{attr.MetricEngineServiceInference, t.EngineServiceInferenceMsDelta},
		{attr.MetricEngineServiceSampling, t.EngineServiceSamplingMsDelta},
		{attr.MetricEngineIapiInference, t.EngineIapiInferenceMsDelta},
		{attr.MetricEngineIapiSampling, t.EngineIapiSamplingMsDelta},
		{attr.MetricResponsesExclEngineAndTool, t.ResponsesExclEngineAndToolMsDelta},
		{attr.MetricResponsesExclEngineWaitSampling, t.ResponsesExclEngineWaitSamplingMsDelta},
		{attr.MetricResponsesExclEngineWaitSamplingIapi, t.ResponsesExclEngineWaitSamplingIapiMsDelta},
		{attr.MetricResponsesAPIExclClientTools, t.ResponsesAPIExclClientToolsMsDelta},
	} {
		if d.ms > 0 {
			add(d.name, timingAttrs)
		}
	}
	if t.EngineUncachedPromptTokensDelta > 0 {
		add(attr.MetricEngineUncachedPromptTokens, engineUncachedPromptTokenAttrs(base))
	}

	tbt := tbtAttrs(base)
	if t.EngineServiceTBTMs > 0 {
		add(attr.MetricEngineServiceTBT, tbt)
	}
	if t.EngineIapiTBTMs > 0 {
		add(attr.MetricEngineIapiTBT, tbt)
	}
	if t.EngineServiceMinusIapiTBTMs != 0 {
		add(attr.MetricEngineServiceMinusIapiTBT, tbt)
	}

	if t.AccountID != "" {
		accountAttrs := rateLimitAttrs(base)
		if t.RateLimitWindowMin > 0 {
			add(attr.MetricRateLimitUsed, accountAttrs)
			add(attr.MetricRateLimitReset, accountAttrs)
		}
		if t.RateLimit2WindowMin > 0 {
			add(attr.MetricRateLimitUsed2, accountAttrs)
		}
		for model := range t.ExtraRateLimits {
			if model == "" {
				continue
			}
			add(attr.MetricRateLimitPerModel, guard.With(accountAttrs, attr.KV{Key: attr.GenAIRequestModel, Value: model}))
		}
		if t.HasCredits || t.CreditsUnlimited || t.CreditsBalance != "" {
			add(attr.MetricCreditsUnlimited, accountAttrs)
			if _, ok := parseCreditsBalance(t.CreditsBalance); ok {
				add(attr.MetricCreditsBalance, accountAttrs)
			}
		}
	}

	return out
}

// responseCounterAttrs keeps the historical request/response shape plus API key name,
// but deliberately drops reasoning effort and thread source because CXO-0003 reserves
// those for token and cost shape. It also drops the bounded proxy_status/error_code/
// failure_phase fields introduced in wave 0: those are response-span and Loki
// diagnostics, not response-count dimensions.
func responseCounterAttrs(base []attr.KV) []attr.KV {
	return attr.Only(base, attr.GenAIProvider, attr.GenAIOperation, attr.GenAIRequestModel,
		attr.GenAIResponseModel, attr.Status, attr.RequestKind, attr.Family, attr.AccountID,
		attr.PlanType, attr.ServiceTier, attr.ServiceTierRequested, attr.APIKeyName,
		attr.SubagentKind, attr.Originator, attr.GenAIAgentName, attr.GenAIAgentVersion,
		attr.ErrorType, attr.ErrorCategory, attr.ErrorCode, attr.CriticalPathCoverage,
		attr.CloseCode, attr.BaselineReset, attr.FrameType)
}

// tokenCounterAttrs is codexlb.tokens' query shape: model/account/request kind, plus
// family for probe exclusion and effort/thread/API-key for cost-shape panels. It
// deliberately drops agent version and token semantics, which only the convention
// token-usage histogram's consumer needs.
func tokenCounterAttrs(base []attr.KV) []attr.KV {
	return attr.Only(base, attr.GenAIProvider, attr.GenAIOperation,
		attr.GenAIRequestModel, attr.GenAIResponseModel, attr.AccountID, attr.RequestKind,
		attr.Family, attr.ReasoningEffort, attr.ThreadSource, attr.APIKeyName)
}

// tokenUsageAttrs starts from tokenCounterAttrs and adds the two agent labels required
// by Grafana agent observability. It still drops duration-only dimensions such as
// service tier and critical-path coverage because this instrument measures token
// distribution, not latency.
func tokenUsageAttrs(base []attr.KV) []attr.KV {
	return attr.Only(base, attr.GenAIProvider, attr.GenAIOperation,
		attr.GenAIRequestModel, attr.GenAIResponseModel, attr.AccountID, attr.RequestKind,
		attr.Family, attr.ReasoningEffort, attr.ThreadSource, attr.APIKeyName,
		attr.GenAIAgentName, attr.GenAIAgentVersion)
}

// costAttrs mirrors the token shape, minus gen_ai.token.type: cost is response-level,
// but needs the same family, effort, thread-source and API-key filters as tokens.
func costAttrs(base []attr.KV) []attr.KV {
	return attr.Only(base, attr.GenAIProvider, attr.GenAIOperation,
		attr.GenAIRequestModel, attr.GenAIResponseModel, attr.AccountID, attr.RequestKind,
		attr.Family, attr.ReasoningEffort, attr.ThreadSource, attr.APIKeyName)
}

// imageGenTokenAttrs is a token counter, so it carries family and API key name for
// probe and key-cost exclusion. It deliberately drops reasoning effort/thread source
// because those only describe text-model token and cost shape in this wave.
func imageGenTokenAttrs(base []attr.KV) []attr.KV {
	return attr.Only(base, attr.GenAIProvider, attr.GenAIOperation,
		attr.GenAIRequestModel, attr.AccountID, attr.RequestKind, attr.Family, attr.APIKeyName)
}

// engineUncachedPromptTokenAttrs is baseline-sensitive engine-token data, so it keeps
// response model and baseline_reset, adds family/API key as required for token
// counters, and deliberately leaves out effort/thread source per CXO-0003's narrower
// token-shape decision.
func engineUncachedPromptTokenAttrs(base []attr.KV) []attr.KV {
	return attr.Only(base, attr.GenAIProvider, attr.GenAIOperation, attr.GenAIRequestModel,
		attr.GenAIResponseModel, attr.AccountID, attr.RequestKind, attr.Family,
		attr.APIKeyName, attr.BaselineReset)
}

// operationDurationAttrs is the agent-observability latency shape. It adds family for
// probe exclusion and keeps error and agent labels for the SDK UI, while deliberately
// dropping effort/thread source and API key name to keep duration histograms out of
// token/cost-only dimensions.
func operationDurationAttrs(base []attr.KV) []attr.KV {
	return attr.Only(base, attr.GenAIProvider, attr.GenAIOperation, attr.GenAIRequestModel,
		attr.GenAIResponseModel, attr.AccountID, attr.RequestKind, attr.Status,
		attr.ErrorType, attr.ErrorCategory, attr.GenAIAgentName, attr.GenAIAgentVersion,
		attr.ServiceTierRequested, attr.Family)
}

// turnDurationAttrs measures user-visible latency only. It carries family and the
// requested service tier because those answer latency questions, and drops effort,
// thread source, API key and agent version because they are not duration dimensions.
func turnDurationAttrs(base []attr.KV) []attr.KV {
	return attr.Only(base, attr.GenAIProvider, attr.GenAIOperation,
		attr.GenAIRequestModel, attr.AccountID, attr.ServiceTierRequested, attr.Family)
}

// ttftAttrs follows the agent-observability duration shape for first-token latency,
// with family added for probe exclusion. It deliberately drops effort/thread/API key
// because CXO-0003 keeps those off duration histograms.
func ttftAttrs(base []attr.KV) []attr.KV {
	return attr.Only(base, attr.GenAIProvider, attr.GenAIOperation,
		attr.GenAIRequestModel, attr.AccountID, attr.RequestKind,
		attr.GenAIAgentName, attr.GenAIAgentVersion, attr.ServiceTierRequested, attr.Family)
}

// criticalPathDurationAttrs is specific to critical-path measurements: coverage and
// baseline_reset describe whether these duration values are trustworthy. Family is
// added for probe exclusion; effort/thread/API key are deliberately dropped.
func criticalPathDurationAttrs(base []attr.KV) []attr.KV {
	return attr.Only(base, attr.GenAIProvider, attr.GenAIOperation, attr.GenAIRequestModel,
		attr.AccountID, attr.RequestKind, attr.CriticalPathCoverage, attr.BaselineReset,
		attr.Family)
}

// engineTimingDurationAttrs is for the eight cumulative engine timing deltas. It
// matches the engine-call shape, adds family for probe exclusion, and drops
// effort/thread/API key because these are duration histograms.
func engineTimingDurationAttrs(base []attr.KV) []attr.KV {
	return attr.Only(base, attr.GenAIProvider, attr.GenAIOperation, attr.GenAIRequestModel,
		attr.GenAIResponseModel, attr.AccountID, attr.RequestKind, attr.BaselineReset,
		attr.Family)
}

// tbtAttrs is the TBT duration shape: model/account/request kind plus family for
// probe exclusion. It deliberately drops effort/thread/API key like the other duration
// histograms, and drops baseline_reset because these TBT fields are per-response
// running averages, not cumulative deltas.
func tbtAttrs(base []attr.KV) []attr.KV {
	return attr.Only(base, attr.GenAIProvider, attr.GenAIOperation, attr.GenAIRequestModel,
		attr.AccountID, attr.RequestKind, attr.Family)
}

// webSearchAttrs is not a token or duration instrument, so CXO-0003 does not add
// family, effort, thread source or API key here. It keeps the small model/account
// request shape that answers "how many web searches did this model issue".
func webSearchAttrs(base []attr.KV) []attr.KV {
	return attr.Only(base, attr.GenAIProvider, attr.GenAIOperation,
		attr.GenAIRequestModel, attr.AccountID, attr.RequestKind)
}

// errorCounterAttrs keeps the error runbook dimensions and deliberately drops family,
// effort, thread source and API key because this counter is for rejection type, not
// cost or latency attribution.
func errorCounterAttrs(base []attr.KV) []attr.KV {
	return attr.Only(base, attr.GenAIProvider, attr.GenAIOperation, attr.GenAIRequestModel,
		attr.AccountID, attr.ErrorType, attr.ErrorCode, attr.Status)
}

// transportEventAttrs is the websocket lifecycle shape. It carries family because
// transport events may belong to probe traffic, and drops request model/effort/thread
// source because dropped connections are diagnosed by close code and frame type.
func transportEventAttrs(base []attr.KV) []attr.KV {
	return attr.Only(base, attr.Family, attr.AccountID, attr.CloseCode, attr.FrameType)
}

// safetyBufferingAttrs keeps only the model pair and account: this rare counter asks
// which model rerouted to which safety retry model. It deliberately drops family,
// effort, thread source and API key because none distinguishes that event.
func safetyBufferingAttrs(base []attr.KV) []attr.KV {
	return attr.Only(base, attr.GenAIRequestModel, attr.GenAIResponseModel, attr.AccountID)
}

// baselineResetAttrs carries family because reset size must be comparable with probe
// exclusion, and otherwise only request kind and account. Model, effort and thread
// source would multiply a hygiene counter whose job is simply to size the reset caveat.
func baselineResetAttrs(base []attr.KV) []attr.KV {
	return attr.Only(base, attr.Family, attr.RequestKind, attr.AccountID)
}

// engineCallAttrs intentionally remains a call-count shape, not a latency shape:
// baseline_reset matters to cumulative deltas, while family/effort/thread/API key do
// not answer the engine-call question in this wave.
func engineCallAttrs(base []attr.KV) []attr.KV {
	return attr.Only(base, attr.GenAIProvider, attr.GenAIOperation, attr.GenAIRequestModel,
		attr.GenAIResponseModel, attr.AccountID, attr.RequestKind, attr.BaselineReset)
}

// toolCallAttrs fans out by tool name only after keeping the model/account/request
// shape. It deliberately drops family, effort, thread source and API key because this
// instrument counts tool invocations, not token/cost/latency.
func toolCallAttrs(base []attr.KV) []attr.KV {
	return attr.Only(base, attr.GenAIProvider, attr.GenAIOperation,
		attr.GenAIRequestModel, attr.AccountID, attr.RequestKind)
}

// toolCallsPerOperationAttrs is the agent-observability tool-use histogram. It keeps
// agent labels for that UI, and deliberately drops family/effort/thread/API key
// because it is neither a duration histogram nor a token/cost instrument.
func toolCallsPerOperationAttrs(base []attr.KV) []attr.KV {
	return attr.Only(base, attr.GenAIProvider, attr.GenAIRequestModel,
		attr.GenAIAgentName, attr.GenAIAgentVersion, attr.AccountID, attr.RequestKind)
}

// rateLimitAttrs is intentionally account-first. Plan type qualifies the quota; every
// other bounded field, including family/API key, would make the gauges easier to
// misaggregate and harder to read.
func rateLimitAttrs(base []attr.KV) []attr.KV {
	return attr.Only(base, attr.AccountID, attr.PlanType)
}

// msToS converts a millisecond field to the seconds every histogram in this sink is
// named and documented in - see recordDurations' comment for why this matters.
func msToS(ms float64) float64 { return ms / 1000 }

func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// parseCreditsBalance best-effort parses turn.Turn.CreditsBalance, which the wire
// protocol sends as a string (observed forms include a leading currency symbol).
// Never seen populated anywhere in the corpus this sink was built against - zero
// matches across every archive - so this is defensive rather than corpus-verified;
// an unparseable value is skipped rather than guessed at.
func parseCreditsBalance(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "$")
	s = strings.ReplaceAll(s, ",", "")
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// toOtel converts the attribute contract's own KV type to otel/attribute's, which is
// what every instrument call needs. Kept as one tiny function rather than inlined
// everywhere so there is exactly one place that ever does this conversion.
func toOtel(kvs []attr.KV) []attribute.KeyValue {
	out := make([]attribute.KeyValue, len(kvs))
	for i, kv := range kvs {
		out[i] = attribute.String(kv.Key, kv.Value)
	}
	return out
}
