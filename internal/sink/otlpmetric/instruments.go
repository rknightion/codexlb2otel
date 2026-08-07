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

	rateLimitUsed     otelmetric.Float64Gauge
	rateLimitReset    otelmetric.Float64Gauge
	rateLimitUsed2    otelmetric.Float64Gauge
	rateLimitPerModel otelmetric.Float64Gauge
	creditsBalance    otelmetric.Float64Gauge
	creditsUnlimited  otelmetric.Float64Gauge
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
		otelmetric.WithUnit("{token}"))
	must(attr.MetricTokenUsage, err)

	i.toolCallsPerOperation, err = meter.Int64Histogram(attr.MetricToolCallsPerOperation,
		otelmetric.WithDescription("Tool calls per response - len(Turn.ToolCalls), "+
			"recorded once per response including zero, so the distribution reflects every "+
			"operation and not only the ones that used a tool."),
		otelmetric.WithUnit("{tool_call}"))
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
		otelmetric.WithUnit("s"))
	must(attr.MetricOperationDuration, err)

	i.turnDuration, err = meter.Float64Histogram(attr.MetricTurnDuration,
		otelmetric.WithDescription("End-to-end turn latency: the client's own "+
			"turn_started_at_unix_ms to server completion. The only figure that measures "+
			"what the user actually waited for."),
		otelmetric.WithUnit("s"))
	must(attr.MetricTurnDuration, err)

	i.ttft, err = meter.Float64Histogram(attr.MetricTTFT,
		otelmetric.WithDescription("Time to first token, server-reported. Convention-compliant "+
			"name (gen_ai.server.time_to_first_token) - see the MetricTTFT doc comment for why "+
			"this is the server.* form and not client.*."),
		otelmetric.WithUnit("s"))
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
