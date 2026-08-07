package attr

import "strings"

// Attribute keys, frozen. Every emitter reads them from here; none invents its own.
//
// The split is deliberate. Where OpenTelemetry's GenAI conventions define a concept,
// this service uses their name so it lines up with genai-otel-bridge in the same
// Grafana stack. Where they do not - per-account rate-limit headroom, websocket close
// codes, prewarm and compaction, the server's own critical-path self-assessment -
// inventing a `gen_ai.*` name would claim a convention that does not exist, so those
// live under `codexlb.*`.
//
// Spec checked 2026-08-07, not recalled: the GenAI conventions have MOVED out of
// open-telemetry/semantic-conventions into open-telemetry/semantic-conventions-genai,
// and the copies left behind are marked deprecated with that note. The attribute names
// below are the current ones. `gen_ai.usage.prompt_tokens` and
// `gen_ai.usage.completion_tokens` are the superseded spellings - do not use them.
const (
	// --- OTel GenAI semantic conventions ---

	// GenAIProvider is required on every GenAI metric by the convention.
	GenAIProvider = "gen_ai.provider.name"
	// GenAIOperation is likewise required. codex-lb speaks the Responses API, so the
	// operation is a chat completion in convention terms.
	GenAIOperation = "gen_ai.operation.name"
	// GenAIRequestModel is the model asked for.
	GenAIRequestModel = "gen_ai.request.model"
	// GenAIResponseModel is the model that actually produced the response. Normally
	// equal to the request model, and deliberately NOT when safety buffering re-runs a
	// response through a different one - which is the only place the two diverge, and
	// the reason both are carried rather than one.
	GenAIResponseModel = "gen_ai.response.model"
	// GenAIConversationID is the thread. High cardinality: metadata and spans only.
	GenAIConversationID = "gen_ai.conversation.id"
	// GenAIResponseID is the resp_* id. codex-lb's UI calls this the "Request ID"; see
	// the id-collision table on issue #3 before renaming anything here.
	GenAIResponseID = "gen_ai.response.id"
	// GenAITokenType distinguishes the token counters, per the convention's own
	// gen_ai.client.token.usage attribute.
	GenAITokenType = "gen_ai.token.type"
	// GenAIErrorType is the convention's low-cardinality error discriminator.
	ErrorType = "error.type"

	// ToolName is the tool invoked. Bounded in practice but open by construction, so
	// it is capped like every other bounded-open field. RENAMED from codexlb.tool_name
	// (issue #18): the convention names this exact concept.
	ToolName = "gen_ai.tool.name"
	// ReasoningEffort is low | medium | high | xhigh. RENAMED from
	// codexlb.reasoning_effort (issue #18) to the convention's own name - which is
	// gen_ai.request.reasoning.level, NOT gen_ai.request.thinking.level as issue #18
	// proposed. Checked against the live registry at
	// github.com/open-telemetry/semantic-conventions-genai (model/gen-ai/registry.yaml,
	// 2026-08-07): "thinking.level" does not exist anywhere in the moved spec;
	// "reasoning.level" does, with brief "The reasoning or thinking effort level
	// requested for a GenAI model." The spec wins over the issue's proposed name.
	ReasoningEffort = "gen_ai.request.reasoning.level"

	// GenAIRequestTemperature and GenAIRequestTopP are Turn.Temperature/TopP (issue
	// #23). Checked against the live registry (semantic-conventions-genai,
	// 2026-08-07), same source and date as ReasoningEffort above:
	// gen_ai.request.temperature and gen_ai.request.top_p both exist, type double,
	// stability "development".
	//
	// SPAN AND GENERATION ATTRIBUTES ONLY - never routed through attr.Guard, and
	// deliberately given no Field entry in attr.go's registry (frozen; not this
	// lane's to extend). Both are measured CONSTANT across the whole corpus
	// (temperature=1.0, top_p=0.98, issue #23's own text), so promoting either to a
	// metric attribute would add a dimension carrying exactly one observed value for
	// no query benefit and a cardinality cost forever. Applied directly at the two
	// call sites that need them (otlptrace's response span, agento11y's Generation),
	// the same way GenAIOperation/GenAIProvider above are applied directly to the
	// response span without ever going through SpanAttrs/MetricAttrs.
	GenAIRequestTemperature = "gen_ai.request.temperature"
	GenAIRequestTopP        = "gen_ai.request.top_p"

	// --- OTel GenAI semantic conventions: tool execution and usage (issue #18) ---
	//
	// Added against the live registry, not recalled - same source and date as
	// ReasoningEffort above. All confirmed present in the CURRENT (post-move,
	// non-deprecated) registry, none of them enum-typed, so none needs a Cap the way
	// ToolName does.

	// ToolCallID is the tool invocation's own id (ToolCall.CallID) - one value per
	// call, not per turn, so - like ToolName - it is caller-supplied via Guard.With
	// rather than extracted from a Turn. Unlike ToolName it is NOT bounded: a call id
	// is unique per invocation by construction, so it is Identity class, never a
	// metric attribute or label.
	ToolCallID = "gen_ai.tool.call.id"
	// ToolType is the convention's classification of a tool (its examples are
	// function | extension | datastore); codex-lb's own ToolCall.Kind takes custom |
	// function, which does not match those examples verbatim but is the same concept
	// the convention names, and gen_ai.tool.type is type:string, not a closed enum.
	// Caller-supplied like ToolName, and - unlike ToolCallID - genuinely bounded, so
	// it is capped the same way.
	ToolType = "gen_ai.tool.type"
	// ToolCallArguments and ToolCallResult are ToolCall.Input and the matching
	// ToolOutput.Text (joined by call id). The spec marks both `opt_in` on the
	// execute_tool span specifically because they may carry sensitive content - this
	// service emits them unconditionally per issue #18's explicit instruction, on the
	// same footing as ToolCall.Input and ToolOutput.Text already being carried
	// unconditionally into Loki's tool_call/tool_output record types with no capture
	// toggle. If that changes, it changes here and in the Loki path together, not one
	// without the other.
	ToolCallArguments = "gen_ai.tool.call.arguments"
	ToolCallResult    = "gen_ai.tool.call.result"
	// GenAIAgentName is ToolCall.TaskName - "populated for spawn_agent, describing the
	// child agent" (turn.ToolCall's own doc comment) - carried only on the tool_call
	// span this service classifies as invoke_agent (see otlptrace/spans.go's
	// toolOperationName), matching the spec's own span-naming guidance for that
	// operation ("invoke_agent {gen_ai.agent.name}").
	GenAIAgentName = "gen_ai.agent.name"

	// GenAIUsage* are Turn's six token fields as SPAN attributes. Before issue #18 the
	// only place token usage existed was internal/sink/otlpmetric's codexlb.tokens
	// counter, split by GenAITokenType - so a decoded span carried no usage at all.
	// input/output/cache_read/cache_creation/reasoning are exactly the five names the
	// registry defines; there is no gen_ai.usage.total_tokens in the current spec (see
	// the MetricTokens comment below for why that is not a gap worth filling under a
	// codexlb name either - input+output already gives the total).
	GenAIUsageInputTokens      = "gen_ai.usage.input_tokens"
	GenAIUsageOutputTokens     = "gen_ai.usage.output_tokens"
	GenAIUsageCacheReadTokens  = "gen_ai.usage.cache_read.input_tokens"
	GenAIUsageCacheWriteTokens = "gen_ai.usage.cache_creation.input_tokens"
	GenAIUsageReasoningTokens  = "gen_ai.usage.reasoning.output_tokens"

	// --- codexlb.* : concepts the convention has no notion of ---

	// Family is websocket | http | probe. NOT the record's own transport field, which
	// reads "websocket" for every record including the HTTP ones and the health checks.
	Family = "codexlb.family"
	// RequestKind is turn | prewarm | compaction. Prewarm does no engine work and
	// compaction is the server compacting context; neither is a user turn, and counting
	// them as one overstates turn rates and understates cost per turn.
	RequestKind = "codexlb.request_kind"
	// Status is the response outcome: completed | incomplete | transport | error.
	//
	// STAYS codexlb.*, NOT renamed to gen_ai.response.finish_reasons - decided, not
	// left open, despite issue #18 raising it as a question. Measured against the
	// full corpus signature (corpus.sig.json, 1.84M records): response.status takes
	// exactly ONE value, "completed"; finish_reason and stop_reason appear nowhere in
	// the capture; response.incomplete_details is always null. There is no
	// finish-reason data on the wire at all - the convention's own vocabulary
	// (stop, length, content_filter, tool_calls, ...) describes something OpenAI's
	// Responses API here simply does not report.
	//
	// The four values this field actually takes are not a coarser version of that
	// vocabulary - issue #18's own framing was wrong on this point. "completed" is the
	// wire constant. "incomplete", "transport" and "error" are verdicts THIS SERVICE
	// reaches about its own pipeline (a response that never got a completion event, a
	// websocket that died mid-response, a request the server rejected before any
	// model turn happened) - none of them is the model's own reason for stopping.
	// Status answers "did codexlb's pipeline observe this response finish", a
	// pipeline-health question; gen_ai.response.finish_reasons answers "why did the
	// model stop generating", a model-behaviour question. They are different axes
	// entirely, not two granularities of the same one. Emitting the standard name
	// over data that answers a different question would make a convention-aware
	// dashboard display a fabricated finish reason in place of an honest "we don't
	// have this" - worse than a plainly non-standard key.
	Status = "codexlb.status"
	// AccountID is which of the load-balanced accounts served the request. The headline
	// grouping for a load balancer: averaging rate-limit headroom across accounts hides
	// exactly the exhaustion the metric exists to show.
	//
	// ARCHIVE FORM - a bare UUID. Postgres stores the same account with an 8-hex suffix
	// (`...-20b1994b9ac5_1369f6ad`); enrichment must strip it, or one account appears as
	// two label values depending on which source produced the record. See issue #3.
	AccountID = "codexlb.account_id"
	// PlanType is the subscription tier the rate limits are measured against.
	PlanType = "codexlb.plan_type"
	// ServiceTier is default | auto.
	ServiceTier = "codexlb.service_tier"
	// ThreadSource is user | subagent.
	ThreadSource = "codexlb.thread_source"
	// SubagentKind describes how a subagent thread was created.
	SubagentKind = "codexlb.subagent_kind"
	// Originator is the client binary: codex-tui, codex_exec, codex_cli_rs (the probe).
	Originator = "codexlb.originator"
	// ErrorCode is the provider's own error code - e.g. websocket_connection_limit_reached.
	ErrorCode = "codexlb.error_code"
	// CloseCode is the websocket close code: 1000 clean, 1012 service restart.
	CloseCode = "codexlb.close_code"
	// FrameType is the websocket frame class for transport records: close | error.
	FrameType = "codexlb.frame_type"
	// CriticalPathCoverage is the server's own verdict on whether its timing breakdown
	// is trustworthy: complete | missing_harness_boundary. Roughly 4.6% of responses are
	// the latter, and a dashboard that wants accuracy has to be able to exclude them.
	CriticalPathCoverage = "codexlb.critical_path.coverage"
	// BaselineReset marks deltas computed with no prior cumulative baseline. Those are
	// upper bounds that absorb unobserved work, not measurements.
	BaselineReset = "codexlb.baseline_reset"
	// RecordType is what the Loki line holds. A default stream label, because it is the
	// axis every query filters on first and it is fixed by this package.
	RecordType = "codexlb.record_type"
	// ServiceName is the Loki service label.
	ServiceName = "service_name"

	// --- identity: metadata and spans only, never a metric attribute or a label ---

	RequestID      = "codexlb.request_id"
	SessionID      = "codexlb.session_id"
	ThreadID       = "codexlb.thread_id"
	ParentThreadID = "codexlb.parent_thread_id"
	// PrevResponseID chains responses into a DAG, and is issue #15's "walk the
	// response chain" requirement.
	//
	// The standard name, NOT a codexlb.* invention: semantic-conventions-genai
	// defines gen_ai.request.previous_response.id and its note names OpenAI's
	// previous_response_id as the example, which is exactly this field. Checked
	// against the live registry rather than recalled - three of five names proposed
	// on #18 turned out not to exist, and a codexlb.* key where the spec has a
	// standard one is precisely what TestNoUnjustifiedCodexlbKey exists to catch.
	PrevResponseID = "gen_ai.request.previous_response.id"
	// ForkedFromThreadID has no GenAI equivalent - forking a conversation is a
	// codex-lb concept, not a model-API one - so it keeps a codexlb.* name.
	ForkedFromThreadID = "codexlb.forked_from_thread_id"
	TurnID             = "codexlb.turn_id"
	ParentTurnID       = "codexlb.parent_turn_id"
	LogicalTurnID      = "codexlb.logical_turn_id"
	WindowID           = "codexlb.window_id"
	InstallationID     = "codexlb.installation_id"
	PromptCacheKey     = "codexlb.prompt_cache_key"
	EngineIDs          = "codexlb.engine_ids"
	InstructionsHash   = "codexlb.instructions_hash"
	ErrorMessage       = "codexlb.error_message"
	SafetyID           = "codexlb.safety_identifier"
	// TransportEvent is the server's plain-text reason for a websocket lifecycle event
	// - "no close frame received or sent", "received 1012 (service restart)". Prose,
	// so it is identity-class; FrameType is the bounded classification of the same
	// event and is what a metric groups by.
	TransportEvent = "codexlb.transport_event"
)

// LokiKey converts an attribute key into a Loki label or structured-metadata name.
//
// The keys above are OpenTelemetry attribute names, and OTel's convention is dotted -
// gen_ai.request.model, codexlb.family. Loki's is not: its label grammar is
// Prometheus's, [a-zA-Z_][a-zA-Z0-9_]*, and a dot inside braces is a PARSE ERROR.
//
// Live-verified against Grafana Cloud on 2026-08-07, because this cost a silent
// delivery failure. A push carrying `codexlb.record_type` returns
//
//	400  couldn't parse labels: 1:9: parse error: unexpected character inside braces: '.'
//
// and the sink's own rule - a permanent 4xx is counted and dropped so the checkpoint
// can advance past unfixable data - then discarded every line without an error. The
// service ran to completion, consumed a whole archive, wrote a checkpoint, and
// delivered nothing. The same push with underscores returns 204.
//
// So: dots become underscores, here and in exactly one place, so a LogQL query and
// the emitter cannot disagree about what a label is called. Metric and span
// attributes keep their dotted OTel names, which is correct for those backends.
func LokiKey(key string) string { return strings.ReplaceAll(key, ".", "_") }

// GenAIProviderValue is emitted on every GenAI metric, as the convention requires it.
// codex-lb proxies OpenAI, so the provider is openai regardless of which account
// served the request.
const GenAIProviderValue = "openai"

// GenAIOperationValue is the convention's operation name for a chat-style completion.
//
// GenAIOperationExecuteTool and GenAIOperationInvokeAgent (issue #18) are the two
// other operation values this service emits, on tool_call spans only - see
// otlptrace/spans.go's emitToolCalls for which of the two a given tool call gets and
// why, and its comment on the double-count trap fixed in 228c717 for why adding
// these does not reopen it: that fix constrains how many spans per RESPONSE may
// claim "chat" (the inference itself); execute_tool and invoke_agent describe a
// different event (one tool call) on a different span, counted once each already,
// not a second claim on the same inference.
const (
	GenAIOperationValue       = "chat"
	GenAIOperationExecuteTool = "execute_tool"
	GenAIOperationInvokeAgent = "invoke_agent"
)

// Record types - the values RecordType takes, and the set of Loki line kinds.
//
// Each is a SEPARATE line by construction. A turn record that bundled its tool output
// inline would lose the whole record to Loki's max_line_size, taking the token counts
// and model attribution down with the stdout that overflowed it (issue #6).
const (
	RecordTurn         = "turn"
	RecordPrompt       = "prompt"
	RecordMessage      = "message"
	RecordToolCall     = "tool_call"
	RecordToolOutput   = "tool_output"
	RecordAgentMessage = "agent_message"
	RecordTransport    = "transport"
	RecordError        = "error"
	RecordInstructions = "instructions"
)

// RecordTypes is every value RecordType may take, for config validation and tests.
var RecordTypes = []string{
	RecordTurn, RecordPrompt, RecordMessage, RecordToolCall, RecordToolOutput,
	RecordAgentMessage, RecordTransport, RecordError, RecordInstructions,
}

// Metric instrument names.
//
// Deviation from the convention, stated rather than hidden: it defines
// `gen_ai.client.token.usage` as a HISTOGRAM. MetricTokens is a counter instead.
// The question this service answers is "how many tokens did which model burn on whose
// account", which is a sum; a histogram would multiply every attribute combination by
// its bucket count to deliver a distribution nobody queries. The convention's histogram
// name is therefore deliberately not reused for a differently-typed instrument - it
// carries a codexlb.* name so nothing downstream mistakes it for the standard one.
//
// MetricTokenUsage (issue #18) is the standard-named histogram added ALONGSIDE
// MetricTokens, not instead of it - the deviation above stays, re-affirmed on the
// issue: "the question... is a sum". This one exists purely so anything querying the
// convention's own instrument name sees us at all. It carries the same five
// GenAITokenType values as MetricTokens (input/output/reasoning/cached/cache_write),
// not only the two the registry's gen_ai.token.type enum lists as canonical
// (input/output) - same reasoning as GenAITokenType's own doc comment: the cache and
// reasoning breakdowns extend the axis rather than becoming separate instruments, and
// that reasoning does not change just because this particular instrument now has a
// standard name too.
const (
	MetricTokens          = "codexlb.tokens"                  // counter, {token}, by GenAITokenType
	MetricResponses       = "codexlb.responses"               // counter, {response}
	MetricTurns           = "codexlb.turns"                   // counter, {turn} - excludes prewarm and compaction
	MetricEngineCalls     = "codexlb.engine_calls"            // counter, {call}
	MetricToolCalls       = "codexlb.tool_calls"              // counter, {call}
	MetricErrors          = "codexlb.errors"                  // counter, {error}
	MetricTransportEvents = "codexlb.transport_events"        // counter, {event}
	MetricSafetyBuffering = "codexlb.safety_buffering_events" // counter, {event}
	MetricBaselineResets  = "codexlb.baseline_resets"         // counter, {response}
	MetricAttrsRejected   = "codexlb.attributes_rejected"     // counter, {attribute} - the guard's own output
	MetricImageGenTokens  = "codexlb.image_gen_tokens"        // counter, {token}

	// MetricWebSearch: issue #18 proposed renaming this to "the convention's
	// server-tool-use name". Checked against the live registry
	// (semantic-conventions-genai, 2026-08-07): there is no such name. No
	// gen_ai.usage.server_tool_use.* attribute, no web-search-specific metric, and no
	// mention of "web_search" or "server_tool_use" anywhere in the moved spec's
	// registry, metrics or provider docs (including docs/gen-ai/openai.md). The
	// concept this counts - web-search calls the model issued as a server-side tool -
	// simply is not modelled by the current spec. Spec wins over the issue: NOT
	// renamed.
	MetricWebSearch = "codexlb.web_search_requests" // counter, {request}

	// MetricToolCallsPerOperation (issue #18) answers "how many tool calls did one
	// operation make" - len(Turn.ToolCalls) per response. The issue asked for
	// gen_ai.client.tool_calls_per_operation; that name does not exist in the current
	// spec either (checked 2026-08-07). The closest defined metric,
	// gen_ai.invoke_agent.tool_calls, is explicitly scoped to a single agent
	// invocation ("distribution is scoped to a single agent invocation"), which does
	// not generalize to every chat operation this service reduces - most turns are not
	// agent invocations. Kept under codexlb.* rather than misapplying a
	// narrower-scoped standard name to a broader-scoped measurement.
	MetricToolCallsPerOperation = "codexlb.tool_calls_per_operation" // histogram, {tool_call}

	// Durations are seconds, as the convention requires for gen_ai.client.operation.duration.
	MetricOperationDuration = "gen_ai.client.operation.duration" // histogram, s - convention-compliant
	MetricTurnDuration      = "codexlb.turn.duration"            // histogram, s - client turn start to server completion
	// MetricTTFT: issue #18 proposed gen_ai.client.time_to_first_token. Checked
	// against the live registry (2026-08-07): that name does not exist. The current
	// spec has gen_ai.server.time_to_first_token ("Time to generate first token for
	// successful responses") and a differently-scoped
	// gen_ai.client.operation.time_to_first_chunk ("measured from when the client
	// issues the generation request"). Turn.TTFTMs is the server's own reported
	// figure (from timing_metrics, not measured client-side), so
	// gen_ai.server.time_to_first_token is the correct match - the issue had the
	// right concept and the wrong namespace. Spec wins: renamed to the server.* form,
	// not the client.* form the issue proposed.
	MetricTTFT             = "gen_ai.server.time_to_first_token" // histogram, s - convention-compliant
	MetricEngineWall       = "codexlb.engine_wall"               // histogram, s
	MetricHarnessUnblocked = "codexlb.harness_unblocked"         // histogram, s
	MetricPreInference     = "codexlb.pre_inference"             // histogram, s
	MetricSamplingStream   = "codexlb.sampling_and_stream"       // histogram, s
	MetricClientToolPause  = "codexlb.client_tool_pause"         // histogram, s
	// MetricTokenUsage is the convention-compliant histogram - see the const block's
	// own doc comment above.
	MetricTokenUsage = "gen_ai.client.token.usage" // histogram, {token}, by GenAITokenType - convention-compliant

	// The eight durations below (issue #23) are per-response deltas off the same
	// cumulative logical-turn counters as MetricEngineCalls (see
	// otlpmetric/record.go's recordEngineCalls and turn.go's own comment on the nine
	// fields the reducer diffs identically). engine_service_* and engine_iapi_* are
	// TWO SEPARATE PAIRS OF INSTRUMENTS by name, not one instrument with a
	// service|iapi domain attribute - the issue's own acceptance criteria rule out a
	// new attribute key, and this package's existing style is already one instrument
	// per measurement (MetricEngineWall, MetricPreInference, ... above are each their
	// own field, never merged behind a "which phase" label). Comparing
	// engine_service_inference against engine_iapi_inference in one PromQL query
	// costs nothing extra with two names over one name and a label.
	MetricEngineServiceInference              = "codexlb.engine_service_inference"                 // histogram, s
	MetricEngineServiceSampling               = "codexlb.engine_service_sampling"                  // histogram, s
	MetricEngineIapiInference                 = "codexlb.engine_iapi_inference"                    // histogram, s
	MetricEngineIapiSampling                  = "codexlb.engine_iapi_sampling"                     // histogram, s
	MetricResponsesExclEngineAndTool          = "codexlb.responses_excl_engine_and_tool"           // histogram, s
	MetricResponsesExclEngineWaitSampling     = "codexlb.responses_excl_engine_wait_sampling"      // histogram, s
	MetricResponsesExclEngineWaitSamplingIapi = "codexlb.responses_excl_engine_wait_sampling_iapi" // histogram, s
	MetricResponsesAPIExclClientTools         = "codexlb.responsesapi_excl_client_tools"           // histogram, s

	// MetricEngineUncachedPromptTokens (issue #23) is EngineUncachedPromptTokensDelta,
	// deliberately its OWN instrument rather than a sixth value on gen_ai.token.type -
	// see recordEngineTimingDeltas' doc comment in otlpmetric/record.go for the full
	// reasoning (a nested-breakdown double-count identical to the cached/input trap
	// tokenTypes already documents, PLUS a different measurement family: this is a
	// delta off timing_metrics' own cumulative counter, baseline-reset affected like
	// EngineCallsDelta, not one of the already-safe-to-sum response.completed usage
	// fields the token.type axis is built from).
	MetricEngineUncachedPromptTokens = "codexlb.engine_uncached_prompt_tokens" // counter, {token}

	// The three TBT (time-between-tokens) histograms below (issue #23) are Turn's own
	// per-response running averages, recorded exactly as reported - never diffed, per
	// turn.go's own comment on why delta-ing them would corrupt the arithmetic.
	// MetricEngineServiceMinusIapiTBT is the one of the three that goes NEGATIVE
	// (measured min -6.70ms) and is built with explicit bucket boundaries straddling
	// zero for exactly that reason - see otlpmetric/instruments.go's
	// negativeCapableTBTBoundaries.
	MetricEngineServiceTBT          = "codexlb.engine_service_tbt"            // histogram, s
	MetricEngineIapiTBT             = "codexlb.engine_iapi_tbt"               // histogram, s
	MetricEngineServiceMinusIapiTBT = "codexlb.engine_service_minus_iapi_tbt" // histogram, s - accepts negative values

	// Rate-limit gauges. Meaningless unless grouped by AccountID - see that constant.
	MetricRateLimitUsed     = "codexlb.rate_limit.used_percent"           // gauge, %
	MetricRateLimitReset    = "codexlb.rate_limit.reset_after"            // gauge, s
	MetricRateLimitUsed2    = "codexlb.rate_limit.secondary_used_percent" // gauge, %
	MetricRateLimitPerModel = "codexlb.rate_limit.model_used_percent"     // gauge, %, by GenAIRequestModel
	MetricCreditsBalance    = "codexlb.credits.balance"                   // gauge
	MetricCreditsUnlimited  = "codexlb.credits.unlimited"                 // gauge, 0|1
)

// Self-observability metrics (issue #8): this service's own operational state, not
// the GenAI traffic flowing through it. Built by internal/sink/otlpmetric's
// RegisterSelfObs rather than at Sink construction time - see that file's own doc
// comment for why - but delivered over the SAME OTLP pipeline as every metric above,
// per the issue's explicit requirement: there is no second exporter and no second
// collection interval, so nothing here can silently stop shipping while turn metrics
// keep flowing.
//
// None of these carry GenAIProvider/GenAIOperation, the two attributes every
// instrument above this comment carries. They are not about one GenAI operation, so
// claiming that convention over them would be exactly the mistake this package's own
// doc comment warns against elsewhere: inventing a convention membership that does
// not exist. Their own attribute keys (a file name, a sink name, a rejection reason)
// are declared in otlpmetric/selfobs.go, not here - attr.go's registry is keyed by a
// Field.Of func(*turn.Turn) string, and none of these describe a Turn at all.
const (
	// MetricSelfIngestLag is issue #8's "one metric that matters": wall-clock now
	// minus the newest record timestamp the watcher has seen. Every other failure
	// mode of this service - a stopped watcher, a checkpoint that stopped advancing, a
	// sink rejecting everything - shows up here first, before any of them has its own
	// dedicated signal. See internal/selfobs.Snapshot.IngestLagSeconds for the one
	// wall-clock subtraction this whole self-observability path performs, and
	// internal/tail's Poll for why EVICTION reads the same watermark but never
	// subtracts time.Now() from it - two different clocks, kept deliberately apart.
	MetricSelfIngestLag = "codexlb.selfobs.ingest_lag_seconds" // gauge, s

	MetricSelfFilesWatched      = "codexlb.selfobs.files_watched"        // gauge, {file}
	MetricSelfCurrentFileOffset = "codexlb.selfobs.current_file_offset"  // gauge, By, by codexlb.selfobs.file
	MetricSelfBytesRead         = "codexlb.selfobs.bytes_read"           // counter, By
	MetricSelfMembersDecoded    = "codexlb.selfobs.gzip_members_decoded" // counter, {member}
	MetricSelfLinesDecoded      = "codexlb.selfobs.lines_decoded"        // counter, {line}
	MetricSelfUndecodableLines  = "codexlb.selfobs.undecodable_lines"    // counter, {line}

	// MetricSelfPartialMemberReads is the NORMAL steady state of tailing a file
	// codex-lb is still appending to - a gzip member cut off mid-write, resumed next
	// poll. Deliberately its own counter, never folded into MetricSelfDecodeErrors:
	// issue #8's explicit acceptance criterion is that the two stay distinguishable in
	// both metrics and logs (see internal/tail's readFile for the log side).
	MetricSelfPartialMemberReads = "codexlb.selfobs.partial_member_reads" // counter, {read}
	// MetricSelfDecodeErrors is genuine corruption - a complete-but-unreadable gzip
	// member - never a partial trailing one. See MetricSelfPartialMemberReads above.
	MetricSelfDecodeErrors = "codexlb.selfobs.decode_errors" // counter, {error}

	MetricSelfFileReplacements = "codexlb.selfobs.file_replacements" // counter, {file}
	MetricSelfFilesReclaimed   = "codexlb.selfobs.files_reclaimed"   // counter, {file}
	MetricSelfTurnsEmitted     = "codexlb.selfobs.turns_emitted"     // counter, {turn}
	// MetricSelfTurnsEvicted is turn.Reducer.Evict's own output: a response that never
	// saw response.completed, forced closed after EvictAfter measured against the
	// watermark - never against time.Now(); see MetricSelfIngestLag's own comment on
	// the two clocks.
	MetricSelfTurnsEvicted = "codexlb.selfobs.turns_evicted" // counter, {turn}

	// MetricSelfOpenResponses is the reducer's open map size - turn.Reducer.Open's own
	// doc comment calls a steadily growing value "worth alerting on". Flush/Evict
	// exist specifically to prevent this from leaking; this is what makes a leak
	// visible before it becomes an outage (issue #8's explicit acceptance criterion).
	MetricSelfOpenResponses = "codexlb.selfobs.open_responses" // gauge, {response}

	// MetricSelfReducerSeries and MetricSelfReducerThreads are the sizes of the two
	// maps turn.State persists across a restart (issue #8's "reducer state size after
	// snapshot") - the cumulative-baseline series count and the distinct-thread
	// turn-sequence count. Neither is ever pruned (turn/state.go's own comment on why
	// in-flight responses are dropped on restart but these are not), so unbounded
	// growth here is the persisted-state twin of MetricSelfOpenResponses' in-flight
	// leak.
	MetricSelfReducerSeries  = "codexlb.selfobs.reducer_series"  // gauge, {series}
	MetricSelfReducerThreads = "codexlb.selfobs.reducer_threads" // gauge, {thread}

	// MetricSelfSinkRejections is exactly sink.Rejection, reused rather than
	// duplicated (issue #8's explicit instruction): "dropped records" and "failures by
	// reason" from the issue's scope list are the SAME number.
	MetricSelfSinkRejections = "codexlb.selfobs.sink_rejections" // counter, {record}, by codexlb.selfobs.sink, codexlb.selfobs.reason
	// MetricSelfSinkPending is sink.Reporter.Pending() - how much a sink is holding
	// undelivered. The closest available proxy to the issue's "sink batches": neither
	// a batch count nor a retry count has an accessor anywhere in internal/sink (see
	// this feature's own delivery notes for what was left out and why).
	MetricSelfSinkPending = "codexlb.selfobs.sink_pending" // gauge, {record}, by codexlb.selfobs.sink
)

// Token type values, per the convention's gen_ai.token.type.
//
// input and output are the convention's own; the cache and reasoning breakdowns are
// not in it but are the numbers that explain a bill, so they extend the same axis
// rather than becoming separate instruments.
const (
	TokenInput      = "input"
	TokenOutput     = "output"
	TokenReasoning  = "reasoning"
	TokenCached     = "cached"
	TokenCacheWrite = "cache_write"
)
