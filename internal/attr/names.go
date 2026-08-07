package attr

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

	// --- codexlb.* : concepts the convention has no notion of ---

	// Family is websocket | http | probe. NOT the record's own transport field, which
	// reads "websocket" for every record including the HTTP ones and the health checks.
	Family = "codexlb.family"
	// RequestKind is turn | prewarm | compaction. Prewarm does no engine work and
	// compaction is the server compacting context; neither is a user turn, and counting
	// them as one overstates turn rates and understates cost per turn.
	RequestKind = "codexlb.request_kind"
	// Status is the response outcome: completed | incomplete | transport | error.
	Status = "codexlb.status"
	// ReasoningEffort is low | medium | high | xhigh.
	ReasoningEffort = "codexlb.reasoning_effort"
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
	// ToolName is the tool invoked. Bounded in practice but open by construction, so it
	// is capped like every other bounded-open field.
	ToolName = "codexlb.tool_name"
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

	RequestID        = "codexlb.request_id"
	SessionID        = "codexlb.session_id"
	ThreadID         = "codexlb.thread_id"
	ParentThreadID   = "codexlb.parent_thread_id"
	TurnID           = "codexlb.turn_id"
	ParentTurnID     = "codexlb.parent_turn_id"
	LogicalTurnID    = "codexlb.logical_turn_id"
	WindowID         = "codexlb.window_id"
	InstallationID   = "codexlb.installation_id"
	PromptCacheKey   = "codexlb.prompt_cache_key"
	EngineIDs        = "codexlb.engine_ids"
	InstructionsHash = "codexlb.instructions_hash"
	ErrorMessage     = "codexlb.error_message"
	SafetyID         = "codexlb.safety_identifier"
)

// GenAIProviderValue is emitted on every GenAI metric, as the convention requires it.
// codex-lb proxies OpenAI, so the provider is openai regardless of which account
// served the request.
const GenAIProviderValue = "openai"

// GenAIOperationValue is the convention's operation name for a chat-style completion.
const GenAIOperationValue = "chat"

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
// `gen_ai.client.token.usage` as a HISTOGRAM. Token totals here are counters instead.
// The question this service answers is "how many tokens did which model burn on whose
// account", which is a sum; a histogram would multiply every attribute combination by
// its bucket count to deliver a distribution nobody queries. The convention's histogram
// name is therefore deliberately not reused for a differently-typed instrument - these
// carry codexlb.* names so nothing downstream mistakes them for the standard ones.
const (
	MetricTokens          = "codexlb.tokens"                  // counter, {token}, by GenAITokenType
	MetricResponses       = "codexlb.responses"               // counter, {response}
	MetricTurns           = "codexlb.turns"                   // counter, {turn} - excludes prewarm and compaction
	MetricEngineCalls     = "codexlb.engine_calls"            // counter, {call}
	MetricToolCalls       = "codexlb.tool_calls"              // counter, {call}
	MetricWebSearch       = "codexlb.web_search_requests"     // counter, {request}
	MetricImageGenTokens  = "codexlb.image_gen_tokens"        // counter, {token}
	MetricErrors          = "codexlb.errors"                  // counter, {error}
	MetricTransportEvents = "codexlb.transport_events"        // counter, {event}
	MetricSafetyBuffering = "codexlb.safety_buffering_events" // counter, {event}
	MetricBaselineResets  = "codexlb.baseline_resets"         // counter, {response}
	MetricAttrsRejected   = "codexlb.attributes_rejected"     // counter, {attribute} - the guard's own output

	// Durations are seconds, as the convention requires for gen_ai.client.operation.duration.
	MetricOperationDuration = "gen_ai.client.operation.duration" // histogram, s - convention-compliant
	MetricTurnDuration      = "codexlb.turn.duration"            // histogram, s - client turn start to server completion
	MetricTTFT              = "codexlb.time_to_first_token"      // histogram, s
	MetricEngineWall        = "codexlb.engine_wall"              // histogram, s
	MetricHarnessUnblocked  = "codexlb.harness_unblocked"        // histogram, s
	MetricPreInference      = "codexlb.pre_inference"            // histogram, s
	MetricSamplingStream    = "codexlb.sampling_and_stream"      // histogram, s
	MetricClientToolPause   = "codexlb.client_tool_pause"        // histogram, s

	// Rate-limit gauges. Meaningless unless grouped by AccountID - see that constant.
	MetricRateLimitUsed     = "codexlb.rate_limit.used_percent"           // gauge, %
	MetricRateLimitReset    = "codexlb.rate_limit.reset_after"            // gauge, s
	MetricRateLimitUsed2    = "codexlb.rate_limit.secondary_used_percent" // gauge, %
	MetricRateLimitPerModel = "codexlb.rate_limit.model_used_percent"     // gauge, %, by GenAIRequestModel
	MetricCreditsBalance    = "codexlb.credits.balance"                   // gauge
	MetricCreditsUnlimited  = "codexlb.credits.unlimited"                 // gauge, 0|1
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
