package otlpmetric

import (
	"context"
	"errors"
	"fmt"

	otelmetric "go.opentelemetry.io/otel/metric"

	"github.com/rknightion/codexlb2otel/internal/attr"
)

// instruments holds every OTel instrument this sink writes to. One struct rather than
// fields directly on Sink so newInstruments can build and error-check them in one
// place instead of eighteen repeated `if err != nil` blocks inline in the constructor.
type instruments struct {
	tokens          otelmetric.Int64Counter
	responses       otelmetric.Int64Counter
	turns           otelmetric.Int64Counter
	engineCalls     otelmetric.Int64Counter
	toolCalls       otelmetric.Int64Counter
	webSearch       otelmetric.Int64Counter
	imageGenTokens  otelmetric.Int64Counter
	errors          otelmetric.Int64Counter
	transportEvents otelmetric.Int64Counter
	safetyBuffering otelmetric.Int64Counter
	baselineResets  otelmetric.Int64Counter
	costUSD         otelmetric.Float64Counter

	// tokenUsage is the convention-named parallel to tokens - see names.go's
	// MetricTokenUsage doc comment for why both exist rather than one replacing the
	// other. toolCallsPerOperation is issue #18's addition; see MetricToolCallsPerOperation's
	// own doc comment for why it is codexlb.* despite being new.
	tokenUsage            otelmetric.Int64Histogram
	toolCallsPerOperation otelmetric.Int64Histogram

	operationDuration otelmetric.Float64Histogram
	turnDuration      otelmetric.Float64Histogram
	ttft              otelmetric.Float64Histogram
	engineWall        otelmetric.Float64Histogram
	harnessUnblocked  otelmetric.Float64Histogram
	preInference      otelmetric.Float64Histogram
	samplingStream    otelmetric.Float64Histogram
	clientToolPause   otelmetric.Float64Histogram

	// The eight below are issue #23's duration deltas - see recordEngineTimingDeltas
	// in record.go for why engine_service_* and engine_iapi_* are separate instruments
	// rather than one instrument with a domain attribute.
	engineServiceInference              otelmetric.Float64Histogram
	engineServiceSampling               otelmetric.Float64Histogram
	engineIapiInference                 otelmetric.Float64Histogram
	engineIapiSampling                  otelmetric.Float64Histogram
	responsesExclEngineAndTool          otelmetric.Float64Histogram
	responsesExclEngineWaitSampling     otelmetric.Float64Histogram
	responsesExclEngineWaitSamplingIapi otelmetric.Float64Histogram
	responsesAPIExclClientTools         otelmetric.Float64Histogram
	engineUncachedPromptTokens          otelmetric.Int64Counter

	// The three below are issue #23's TBT (time-between-tokens) running averages,
	// recorded per response rather than diffed - see recordTBT. engineServiceMinusIapiTBT
	// is built with negativeCapableTBTBoundaries below because it measures a
	// difference, not a magnitude, and does go negative.
	engineServiceTBT          otelmetric.Float64Histogram
	engineIapiTBT             otelmetric.Float64Histogram
	engineServiceMinusIapiTBT otelmetric.Float64Histogram

	rateLimitUsed     otelmetric.Float64Gauge
	rateLimitReset    otelmetric.Float64Gauge
	rateLimitUsed2    otelmetric.Float64Gauge
	rateLimitPerModel otelmetric.Float64Gauge
	creditsBalance    otelmetric.Float64Gauge
	creditsUnlimited  otelmetric.Float64Gauge
}

// negativeCapableTBTBoundaries covers MetricEngineServiceMinusIapiTBT, the one TBT
// field that measures a DIFFERENCE between two averages rather than an average or a
// magnitude, and so can and does go negative - min -6.70ms measured in the corpus
// (turn.go's own comment on EngineServiceMinusIapiTBTMs). The SDK's default explicit-
// bucket-histogram boundaries start at 0, so every negative reading would silently
// collapse into the lowest bucket and the histogram would under-report without ever
// producing a wrong-looking number on a dashboard - exactly the kind of bug nobody
// notices. Clamping to zero was rejected for the same reason issue #23 rejects it: it
// would misrepresent a genuine negative measurement as "no time elapsed", a false
// number rather than an honest one. Kept as a histogram (not a Gauge) so the
// distribution shape survives, the same as the other two TBT fields.
//
// Values are seconds, matching msToS - the corpus's own millisecond range (single
// digits negative) sits well inside +-0.1s; the wider +-10s tail is headroom for
// whatever this field does on traffic outside the sampled corpus, not a claim about
// what is normal.
var negativeCapableTBTBoundaries = []float64{
	-10, -5, -1, -0.5, -0.25, -0.1, -0.075, -0.05, -0.025, -0.01, -0.005, -0.001,
	0,
	0.001, 0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 1, 5, 10,
}

var agentO11yDurationBoundaries = []float64{
	0.01, 0.02, 0.04, 0.08, 0.16, 0.32, 0.64, 1.28,
	2.56, 5.12, 10.24, 20.48, 40.96, 81.92,
}

var agentO11yTokenBoundaries = []float64{
	1, 4, 16, 64, 256, 1024, 4096, 16384,
	65536, 262144, 1048576, 4194304, 16777216, 67108864,
}

// newInstruments creates every instrument up front, at Sink construction, rather than
// lazily on first use. An instrument name typo would otherwise surface as a silent
// no-op the first time a rare code path (say, safety buffering, 4 in 4000) finally
// executes in production - creating them all eagerly turns that into a startup error.
func newInstruments(meter otelmetric.Meter, guard *attr.Guard) (instruments, error) {
	var errs []error
	must := func(name string, err error) { // collects rather than aborting on first miss
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}

	var i instruments
	var err error

	i.tokens, err = meter.Int64Counter(attr.MetricTokens,
		otelmetric.WithDescription("Tokens consumed, by gen_ai.token.type."),
		otelmetric.WithUnit("{token}"))
	must(attr.MetricTokens, err)

	i.responses, err = meter.Int64Counter(attr.MetricResponses,
		otelmetric.WithDescription("Model responses received."),
		otelmetric.WithUnit("{response}"))
	must(attr.MetricResponses, err)

	i.turns, err = meter.Int64Counter(attr.MetricTurns,
		otelmetric.WithDescription("User turns. Excludes prewarm and compaction, which do "+
			"real engine work but are not turns a user asked for."),
		otelmetric.WithUnit("{turn}"))
	must(attr.MetricTurns, err)

	i.engineCalls, err = meter.Int64Counter(attr.MetricEngineCalls,
		otelmetric.WithDescription("Engine calls per response."),
		otelmetric.WithUnit("{call}"))
	must(attr.MetricEngineCalls, err)

	i.toolCalls, err = meter.Int64Counter(attr.MetricToolCalls,
		otelmetric.WithDescription("Tool invocations, by gen_ai.tool.name."),
		otelmetric.WithUnit("{call}"))
	must(attr.MetricToolCalls, err)

	i.tokenUsage, err = meter.Int64Histogram(attr.MetricTokenUsage,
		otelmetric.WithDescription("Convention-named parallel to codexlb.tokens - same "+
			"values, same gen_ai.token.type breakdown, recorded a second time under the "+
			"standard histogram name so anything querying the convention's own instrument "+
			"sees this service at all."),
		otelmetric.WithUnit("{token}"),
		otelmetric.WithExplicitBucketBoundaries(agentO11yTokenBoundaries...))
	must(attr.MetricTokenUsage, err)

	i.toolCallsPerOperation, err = meter.Int64Histogram(attr.MetricToolCallsPerOperation,
		otelmetric.WithDescription("Tool calls per response - len(Turn.ToolCalls), "+
			"recorded once per response including zero, so the distribution reflects every "+
			"operation and not only the ones that used a tool."),
		otelmetric.WithUnit("count"))
	must(attr.MetricToolCallsPerOperation, err)

	i.webSearch, err = meter.Int64Counter(attr.MetricWebSearch,
		otelmetric.WithDescription("Web-search requests issued by the model."),
		otelmetric.WithUnit("{request}"))
	must(attr.MetricWebSearch, err)

	i.imageGenTokens, err = meter.Int64Counter(attr.MetricImageGenTokens,
		otelmetric.WithDescription("Tokens billed for image generation, separate from the "+
			"response's own token usage."),
		otelmetric.WithUnit("{token}"))
	must(attr.MetricImageGenTokens, err)

	i.errors, err = meter.Int64Counter(attr.MetricErrors,
		otelmetric.WithDescription("Responses that ended in an error, by error.type / "+
			"codexlb.error_code."),
		otelmetric.WithUnit("{error}"))
	must(attr.MetricErrors, err)

	i.transportEvents, err = meter.Int64Counter(attr.MetricTransportEvents,
		otelmetric.WithDescription("Websocket-level events belonging to no response: "+
			"closes and connection errors."),
		otelmetric.WithUnit("{event}"))
	must(attr.MetricTransportEvents, err)

	i.safetyBuffering, err = meter.Int64Counter(attr.MetricSafetyBuffering,
		otelmetric.WithDescription("Responses re-run through a different model for safety "+
			"reasons. Rare (about 4 in 4000) and worth alerting on, not just recording."),
		otelmetric.WithUnit("{event}"))
	must(attr.MetricSafetyBuffering, err)

	i.baselineResets, err = meter.Int64Counter(attr.MetricBaselineResets,
		otelmetric.WithDescription("Responses whose deltas were computed with no prior "+
			"cumulative baseline - upper bounds, not measurements. Aggregations wanting "+
			"accuracy should exclude codexlb.baseline_reset=true series everywhere, but "+
			"this also counts them directly so the exclusion's own size is visible."),
		otelmetric.WithUnit("{response}"))
	must(attr.MetricBaselineResets, err)

	i.costUSD, err = meter.Float64Counter(attr.MetricCostUSD,
		otelmetric.WithDescription("Computed response cost in USD. Recorded only when "+
			"codex-lb enrichment supplied an explicit cost value."),
		otelmetric.WithUnit("{USD}"))
	must(attr.MetricCostUSD, err)

	// MetricAttrsRejected is intentionally an ObservableCounter, not something Emit
	// increments: the guard already keeps a monotonic running total per field (see
	// attr.Guard.Rejected), so the natural way to expose it is to read that total at
	// each collection rather than re-derive a delta ourselves. It carries NO
	// attributes: names.go documents every other counter's "by X" breakdown
	// explicitly and does not document one for this instrument, and the field name a
	// rejection happened against is not itself one of the attribute keys the
	// contract defines - inventing one here would be exactly the kind of
	// attribute-set-of-its-own this package exists to avoid. It is a single process-
	// wide health number: nonzero means some field's cap is too small for reality.
	_, err = meter.Int64ObservableCounter(attr.MetricAttrsRejected,
		otelmetric.WithDescription("Attribute values collapsed to _other because a bounded "+
			"field's cap was reached. Nonzero means the guard is silently rewriting real "+
			"values and a cap in internal/attr needs raising."),
		otelmetric.WithUnit("{attribute}"),
		otelmetric.WithInt64Callback(func(_ context.Context, o otelmetric.Int64Observer) error {
			var total int64
			for _, n := range guard.Rejected() {
				total += n
			}
			o.Observe(total)
			return nil
		}))
	must(attr.MetricAttrsRejected, err)

	i.operationDuration, err = meter.Float64Histogram(attr.MetricOperationDuration,
		otelmetric.WithDescription("Server-side response duration: response.created to "+
			"response.completed. Convention-compliant name and unit."),
		otelmetric.WithUnit("s"),
		otelmetric.WithExplicitBucketBoundaries(agentO11yDurationBoundaries...))
	must(attr.MetricOperationDuration, err)

	i.turnDuration, err = meter.Float64Histogram(attr.MetricTurnDuration,
		otelmetric.WithDescription("End-to-end turn latency: the client's own "+
			"turn_started_at_unix_ms to server completion. The only figure that measures "+
			"what the user actually waited for."),
		otelmetric.WithUnit("s"))
	must(attr.MetricTurnDuration, err)

	i.ttft, err = meter.Float64Histogram(attr.MetricTTFT,
		otelmetric.WithDescription("Time to first token, server-reported. Named "+
			"gen_ai.client.time_to_first_token, which is agent-observability's name rather "+
			"than the GenAI registry's - see the MetricTTFT doc comment for why that trade "+
			"was made and re-made."),
		otelmetric.WithUnit("s"),
		otelmetric.WithExplicitBucketBoundaries(agentO11yDurationBoundaries...))
	must(attr.MetricTTFT, err)

	i.engineWall, err = meter.Float64Histogram(attr.MetricEngineWall,
		otelmetric.WithDescription("Engine wall-clock time, from critical_path (per-response, "+
			"not the cumulative logical-turn figure)."),
		otelmetric.WithUnit("s"))
	must(attr.MetricEngineWall, err)

	i.harnessUnblocked, err = meter.Float64Histogram(attr.MetricHarnessUnblocked,
		otelmetric.WithDescription("Time the harness spent unblocked: the server-side share "+
			"of the response as opposed to time waiting on the client."),
		otelmetric.WithUnit("s"))
	must(attr.MetricHarnessUnblocked, err)

	i.preInference, err = meter.Float64Histogram(attr.MetricPreInference,
		otelmetric.WithDescription("Time spent before inference began."),
		otelmetric.WithUnit("s"))
	must(attr.MetricPreInference, err)

	i.samplingStream, err = meter.Float64Histogram(attr.MetricSamplingStream,
		otelmetric.WithDescription("Time spent sampling and streaming the response."),
		otelmetric.WithUnit("s"))
	must(attr.MetricSamplingStream, err)

	i.clientToolPause, err = meter.Float64Histogram(attr.MetricClientToolPause,
		otelmetric.WithDescription("Time paused waiting on a client-side tool call."),
		otelmetric.WithUnit("s"))
	must(attr.MetricClientToolPause, err)

	// The eight below (issue #23) are per-response deltas off the same cumulative
	// logical-turn counters MetricEngineCalls already diffs - see recordEngineTimingDeltas.
	i.engineServiceInference, err = meter.Float64Histogram(attr.MetricEngineServiceInference,
		otelmetric.WithDescription("Engine-reported inference time, service vantage point."),
		otelmetric.WithUnit("s"))
	must(attr.MetricEngineServiceInference, err)

	i.engineServiceSampling, err = meter.Float64Histogram(attr.MetricEngineServiceSampling,
		otelmetric.WithDescription("Engine-reported sampling time, service vantage point."),
		otelmetric.WithUnit("s"))
	must(attr.MetricEngineServiceSampling, err)

	i.engineIapiInference, err = meter.Float64Histogram(attr.MetricEngineIapiInference,
		otelmetric.WithDescription("Engine-reported inference time, iapi vantage point - "+
			"compare against engine_service_inference to see how much time diverges "+
			"between the two reporting domains."),
		otelmetric.WithUnit("s"))
	must(attr.MetricEngineIapiInference, err)

	i.engineIapiSampling, err = meter.Float64Histogram(attr.MetricEngineIapiSampling,
		otelmetric.WithDescription("Engine-reported sampling time, iapi vantage point."),
		otelmetric.WithUnit("s"))
	must(attr.MetricEngineIapiSampling, err)

	i.responsesExclEngineAndTool, err = meter.Float64Histogram(attr.MetricResponsesExclEngineAndTool,
		otelmetric.WithDescription("Response time excluding both engine and client-tool time."),
		otelmetric.WithUnit("s"))
	must(attr.MetricResponsesExclEngineAndTool, err)

	i.responsesExclEngineWaitSampling, err = meter.Float64Histogram(attr.MetricResponsesExclEngineWaitSampling,
		otelmetric.WithDescription("Response time excluding engine wait and sampling time."),
		otelmetric.WithUnit("s"))
	must(attr.MetricResponsesExclEngineWaitSampling, err)

	i.responsesExclEngineWaitSamplingIapi, err = meter.Float64Histogram(attr.MetricResponsesExclEngineWaitSamplingIapi,
		otelmetric.WithDescription("Response time excluding engine wait and sampling time, iapi vantage point."),
		otelmetric.WithUnit("s"))
	must(attr.MetricResponsesExclEngineWaitSamplingIapi, err)

	i.responsesAPIExclClientTools, err = meter.Float64Histogram(attr.MetricResponsesAPIExclClientTools,
		otelmetric.WithDescription("Responses API time excluding client-tool time."),
		otelmetric.WithUnit("s"))
	must(attr.MetricResponsesAPIExclClientTools, err)

	// MetricEngineUncachedPromptTokens (issue #23) is its own instrument rather than a
	// sixth gen_ai.token.type value - see recordEngineTimingDeltas' doc comment for why.
	i.engineUncachedPromptTokens, err = meter.Int64Counter(attr.MetricEngineUncachedPromptTokens,
		otelmetric.WithDescription("Uncached prompt tokens, per the engine's own cumulative "+
			"counter (input minus cached, a nested breakdown of the engine's prompt-token "+
			"total - deliberately NOT a gen_ai.token.type value; see recordEngineTimingDeltas)."),
		otelmetric.WithUnit("{token}"))
	must(attr.MetricEngineUncachedPromptTokens, err)

	// The three TBT (time-between-tokens) histograms below (issue #23) are per-response
	// running averages, recorded as reported rather than diffed - see recordTBT.
	i.engineServiceTBT, err = meter.Float64Histogram(attr.MetricEngineServiceTBT,
		otelmetric.WithDescription("Time between tokens, averaged across this response's "+
			"engine calls, service vantage point."),
		otelmetric.WithUnit("s"))
	must(attr.MetricEngineServiceTBT, err)

	i.engineIapiTBT, err = meter.Float64Histogram(attr.MetricEngineIapiTBT,
		otelmetric.WithDescription("Time between tokens, averaged across this response's "+
			"engine calls, iapi vantage point."),
		otelmetric.WithUnit("s"))
	must(attr.MetricEngineIapiTBT, err)

	i.engineServiceMinusIapiTBT, err = meter.Float64Histogram(attr.MetricEngineServiceMinusIapiTBT,
		otelmetric.WithDescription("Service TBT minus iapi TBT. Goes NEGATIVE (measured min "+
			"-6.70ms in the corpus) - this instrument uses explicit bucket boundaries "+
			"straddling zero (see negativeCapableTBTBoundaries) specifically so a negative "+
			"reading is not silently absorbed into the default histogram's zero-floored "+
			"first bucket."),
		otelmetric.WithUnit("s"),
		otelmetric.WithExplicitBucketBoundaries(negativeCapableTBTBoundaries...))
	must(attr.MetricEngineServiceMinusIapiTBT, err)

	// Rate-limit and credit gauges. All of them are meaningless unless grouped by
	// account_id - see attr.AccountID's doc comment - and record() enforces that by
	// never calling these without it, rather than trusting every call site to
	// remember.
	i.rateLimitUsed, err = meter.Float64Gauge(attr.MetricRateLimitUsed,
		otelmetric.WithDescription("Primary rate-limit window headroom used, per account."),
		otelmetric.WithUnit("%"))
	must(attr.MetricRateLimitUsed, err)

	i.rateLimitReset, err = meter.Float64Gauge(attr.MetricRateLimitReset,
		otelmetric.WithDescription("Seconds until the primary rate-limit window resets, "+
			"per account."),
		otelmetric.WithUnit("s"))
	must(attr.MetricRateLimitReset, err)

	i.rateLimitUsed2, err = meter.Float64Gauge(attr.MetricRateLimitUsed2,
		otelmetric.WithDescription("Secondary rate-limit window headroom used, per account, "+
			"where the plan has a secondary window."),
		otelmetric.WithUnit("%"))
	must(attr.MetricRateLimitUsed2, err)

	i.rateLimitPerModel, err = meter.Float64Gauge(attr.MetricRateLimitPerModel,
		otelmetric.WithDescription("Rate-limit headroom used for a specific model's separate "+
			"quota, per account and gen_ai.request.model."),
		otelmetric.WithUnit("%"))
	must(attr.MetricRateLimitPerModel, err)

	i.creditsBalance, err = meter.Float64Gauge(attr.MetricCreditsBalance,
		otelmetric.WithDescription("Remaining credit balance, per account."))
	must(attr.MetricCreditsBalance, err)

	i.creditsUnlimited, err = meter.Float64Gauge(attr.MetricCreditsUnlimited,
		otelmetric.WithDescription("Whether the account's credits are unlimited (1) or "+
			"metered (0), per account."),
		otelmetric.WithUnit("1"))
	must(attr.MetricCreditsUnlimited, err)

	if len(errs) > 0 {
		return instruments{}, errors.Join(errs...)
	}
	return i, nil
}
