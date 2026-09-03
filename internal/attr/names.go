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
	// GenAITokenSemantics declares what the input bucket COVERS, so a consumer never
	// has to guess whether the cache buckets are inside it or on top of it. The only
	// value this service emits is TokenSemanticsInclusive.
	//
	// Not in the GenAI registry - it is agent-observability's own extension, and the
	// name is theirs verbatim (apps/plugin's dashboard/queries.ts matches on
	// gen_ai_token_semantics="inclusive"). Carried under gen_ai.* rather than
	// codexlb.* precisely because it is not ours to rename: a consumer keying on the
	// SDK's spelling has to see the SDK's spelling. Emitted on the two token
	// instruments only, where it is the difference between a correct cost figure and
	// one that counts every cached prompt token twice.
	GenAITokenSemantics = "gen_ai.token.semantics"
	// GenAIErrorType is the convention's low-cardinality error discriminator.
	ErrorType = "error.type"
	// ErrorCategory is Agent Observability's coarse SDK error classification.
	ErrorCategory = "error.category"

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
	// GenAIAgentName is WHICH AGENT this service is reporting on: codex for ordinary
	// coding-agent turns and codex/subagent only for source-proven child threads.
	// Originator below retains the client entrypoint independently.
	//
	// It exists because Grafana's agent-observability groups its whole Agents surface
	// by this exact label - apps/plugin's agentRows.ts keys rows on gen_ai_agent_name
	// and buckets everything without it as "anonymous", which is what every series
	// this service emitted was, up to issue #32.
	//
	// UNTIL #32 THIS KEY CARRIED ToolCall.TaskName on invoke_agent spans, which was
	// wrong twice over: a spawn_agent task label ("issue23_history_api") is a task
	// description rather than an agent name, and it is unbounded by construction, so
	// it both misstated the concept and flooded agent-observability's own `agent`
	// search filter (sigil/pkg/searchcore/filter.go maps that filter to
	// span.gen_ai.agent.name) with one-off values. It now lives on SubagentTask.
	GenAIAgentName = "gen_ai.agent.name"
	// GenAIAgentVersion is Turn.InstructionsHash - sha256(instructions)[:16], the
	// identity of the system prompt this response ran under.
	//
	// NOT Turn.ClientVersion, which reads the `version` request header and is ALWAYS
	// EMPTY: a census of 48,000 records across every corpus hour (issue #32) found
	// authorization, chatgpt-account-id, openai-beta, originator, session-id,
	// thread-id, traceparent, x-client-request-id, x-codex-installation-id,
	// x-codex-beta-features, x-codex-turn-metadata, x-codex-turn-state,
	// x-codex-window-id, x-codex-parent-thread-id, x-openai-subagent, tracestate and
	// x-request-id - and no `version` at any point. reducer.go still reads the header
	// so that a future codex client that starts sending it is picked up for free, but
	// nothing may depend on it being populated.
	//
	// The instructions hash is the better answer regardless. Sigil's own notion of an
	// agent version IS the system prompt's identity: agentmeta.resolveEffectiveVersion
	// hashes the declared version when one is present and falls back to hashing the
	// system prompt when it is not. Declaring it also avoids a fragmentation bug -
	// this service's system_prompt is populated only on the response where the
	// instructions hash first CHANGES (turn.go's dedup on Prompts/InstructionsHash),
	// so with the field unset the server would hash the empty prompt for almost every
	// generation and mint one meaningless catalog version. And it round-trips: a
	// version string in sigil's UI greps straight back to codexlb_instructions_hash in
	// Loki.
	GenAIAgentVersion = "gen_ai.agent.version"

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

	// --- agento11y.* : Grafana agent-observability's own span markers (issue #32) ---
	//
	// Neither is a GenAI convention name, and neither is ours to rename - they are the
	// keys that product's plugin reads, verbatim. A `sigil.*` legacy spelling of each
	// exists for spans stored before the 2026-07-16 rename; this service never emitted
	// one and never should.

	// AgentO11ySDKName marks a span as belonging to agent-observability. Its
	// conversation search requires it: with no other filter set, the compiled TraceQL
	// carries (span.agento11y.sdk.name != "" || span.sigil.sdk.name != ""), so a span
	// without it is unsearchable from that UI however complete the rest of it is.
	AgentO11ySDKName = "agento11y.sdk.name"
	// AgentO11yGenerationID binds a span to the Generation the agento11y sink pushes
	// for the same response - it is what lets the conversation view attach a trace to
	// a turn. See GenerationID for the value, which is shared with that sink rather
	// than derived twice.
	AgentO11yGenerationID = "agento11y.generation.id"

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
	// ServiceTier is the tier the SERVER reported: default | auto.
	ServiceTier = "codexlb.service_tier"
	// ServiceTierRequested is the tier the CLIENT asked for: priority, or absent when
	// the request named none. Deliberately a separate key rather than a second value
	// set on ServiceTier, because the two disagree on every response measured since
	// priority processing was switched on - see Turn.ServiceTierRequested. A dashboard
	// that wants to know whether priority is worth having has to be able to select on
	// what was asked for, independently of what the server says it did.
	ServiceTierRequested = "codexlb.service_tier_requested"
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
	// SubagentTask is ToolCall.TaskName - "populated for spawn_agent, describing the
	// child agent" (turn.ToolCall's own doc comment) - carried only on the tool_call
	// span this service classifies as invoke_agent. It held the standard
	// gen_ai.agent.name key until issue #32; see GenAIAgentName above for why it was
	// moved off it.
	SubagentTask   = "codexlb.subagent_task"
	TurnID         = "codexlb.turn_id"
	ParentTurnID   = "codexlb.parent_turn_id"
	LogicalTurnID  = "codexlb.logical_turn_id"
	WindowID       = "codexlb.window_id"
	InstallationID = "codexlb.installation_id"
	PromptCacheKey = "codexlb.prompt_cache_key"
	// CostUSD and the proxy timings are measurements carried on response spans. They
	// are registered as Identity-class values so they can never become metric labels;
	// the dedicated cost counter is the aggregation surface.
	CostUSD                       = "codexlb.cost_usd"
	APIKeyID                      = "codexlb.api_key_id"
	APIKeyName                    = "codexlb.api_key_name"
	ProxyStatus                   = "codexlb.proxy_status"
	ProxyErrorCode                = "codexlb.proxy_error_code"
	ProxyFailurePhase             = "codexlb.proxy_failure_phase"
	ProxyTimeToResponseCreated    = "codexlb.proxy.time_to_response_created"
	ProxyTimeToFirstUpstreamEvent = "codexlb.proxy.time_to_first_upstream_event"
	EngineIDs                     = "codexlb.engine_ids"
	InstructionsHash              = "codexlb.instructions_hash"
	ErrorMessage                  = "codexlb.error_message"
	SafetyID                      = "codexlb.safety_identifier"
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

// The gen_ai.operation.name values this service emits.
//
// GenAIOperationGenerateText and GenAIOperationStreamText replaced a single "chat"
// constant in issue #32, and this is a DELIBERATE DIVERGENCE FROM THE GENAI
// CONVENTION, reversing that part of issue #18. The convention's value for a
// chat-style completion is "chat"; agent-observability's vocabulary is the Vercel AI
// SDK's generateText/streamText, and it is a closed set - apps/plugin's
// conversation/attributes.ts recognises exactly generateText, streamText, embeddings,
// execute_tool, framework_chain and framework_retriever.
//
// Emitting "chat" was not a soft mismatch. sigil-sdk's own llms.txt states the
// failure: an unrecognised value "still reaches Tempo, but the UI classifies it as
// unknown and renders a synthetic generation node with no attached span - the trace
// does not appear inside the conversation and the T icon is absent even though
// trace_id/span_id are set". On the ingest side the same value is stored verbatim:
// sigil's generation service only substitutes a default when operation_name is EMPTY.
//
// Both sides of that vocabulary are non-semconv - sigil's own internal judge emits
// generateText - so this is not "the spec versus a vendor", it is "a spec nobody
// downstream of this service reads versus the one its only consumer does".
//
// Which of the two a response gets is Turn-derived, not constant: see OperationName.
//
// GenAIOperationExecuteTool and GenAIOperationInvokeAgent (issue #18) are the two
// other operation values this service emits, on tool_call spans only - see
// otlptrace/spans.go's emitToolCalls for which of the two a given tool call gets and
// why, and its comment on the double-count trap fixed in 228c717 for why adding
// these does not reopen it: that fix constrains how many spans per RESPONSE may
// claim to be the inference itself; execute_tool and invoke_agent describe a
// different event (one tool call) on a different span, counted once each already,
// not a second claim on the same inference. Both are in agent-observability's
// recognised set too, so neither needed changing.
const (
	GenAIOperationGenerateText = "generateText"
	GenAIOperationStreamText   = "streamText"
	GenAIOperationExecuteTool  = "execute_tool"
	GenAIOperationInvokeAgent  = "invoke_agent"
)

// AgentO11ySDKNameValue is what this service calls itself in AgentO11ySDKName.
//
// The SDKs put their own package name here (agento11y-sdk-go and friends). This
// service is not one of them - it is a replay pipeline reading an archive - so it
// gives its own name rather than impersonating an SDK it does not use. The field's
// consumers only test it for non-emptiness; nothing downstream parses the value.
const AgentO11ySDKNameValue = "codexlb2otel"

// These values match the coding-agent identity exported by agento11y's Codex plugin.
// Originator remains a separate attribute; it is an entrypoint, not another agent.
// Agent Observability deliberately treats a slash as a subagent marker, so only a
// source-proven child thread receives one.
const (
	AgentNameValue    = "codex"
	AgentSubagentName = "codex/subagent"
)

// TokenSemanticsInclusive is the only value GenAITokenSemantics takes: input_tokens
// covers every input token type, with the cache buckets as SUBSETS of it rather than
// additions.
//
// A statement of fact about the source data, not a guess - the OpenAI Responses API
// usage block has exactly that shape, and internal/sink/agento11y already declares the
// proto equivalent (TOKEN_INPUT_SEMANTICS_INCLUSIVE) on every Generation for the same
// reason. Absent, agent-observability's dashboard classifies the series as legacy
// provider-raw and adds the cache buckets ON TOP of input, over-counting both tokens
// and cost on every cached prompt - which is most of them.
const TokenSemanticsInclusive = "inclusive"

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
// GenAITokenType values as MetricTokens (input/output/reasoning/cache_read/cache_write),
// not only the two the registry's gen_ai.token.type enum lists as canonical
// (input/output) - same reasoning as GenAITokenType's own doc comment: the cache and
// reasoning breakdowns extend the axis rather than becoming separate instruments, and
// that reasoning does not change just because this particular instrument now has a
// standard name too.
//
// "Parallel" means same value and same fan-out, NOT same attributes - the two carried
// identical attribute sets until issue #32 and deliberately no longer do. This one
// additionally carries GenAIAgentName, GenAIAgentVersion and GenAITokenSemantics,
// because it is the instrument agent observability reads and MetricTokens is not. See
// recordTokens in internal/sink/otlpmetric for the full reasoning.
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
	MetricCostUSD         = "codexlb.cost_usd"                // float counter, {USD}

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

	// MetricToolCallsPerOperation is the released Agent Observability SDK's app-facing
	// distribution. It is not a current OTel semantic-convention name, but the app
	// queries this SDK contract literally.
	MetricToolCallsPerOperation = "gen_ai.client.tool_calls_per_operation" // histogram, count

	// Durations are seconds, as the convention requires for gen_ai.client.operation.duration.
	MetricOperationDuration = "gen_ai.client.operation.duration" // histogram, s - convention-compliant
	MetricTurnDuration      = "codexlb.turn.duration"            // histogram, s - client turn start to server completion
	// MetricTTFT is gen_ai.client.time_to_first_token, and the history matters because
	// this name has now been argued both ways.
	//
	// Issue #18 proposed the client.* form. Issue #23's work checked the live registry
	// (semantic-conventions-genai, 2026-08-07) and found no such name: the spec has
	// gen_ai.server.time_to_first_token and a differently-scoped
	// gen_ai.client.operation.time_to_first_chunk. Turn.TTFTMs is the SERVER's own
	// reported figure, from timing_metrics rather than measured client-side, so the
	// server.* form was chosen as the honest match.
	//
	// Issue #32 reversed it. That reasoning is still correct about the spec and still
	// wrong about the outcome: nothing reads gen_ai.server.time_to_first_token, and
	// agent-observability's TTFT panels query gen_ai_client_time_to_first_token_seconds
	// literally, so the spec-correct name bought a permanently empty panel and no
	// consumer. Same trade as the operation-name and token-type values above, made the
	// same way. The measurement is unchanged and still server-reported; only the name
	// moved.
	MetricTTFT = "gen_ai.client.time_to_first_token" // histogram, s - agent-observability's name, NOT the registry's

	MetricEngineWall       = "codexlb.engine_wall"         // histogram, s
	MetricHarnessUnblocked = "codexlb.harness_unblocked"   // histogram, s
	MetricPreInference     = "codexlb.pre_inference"       // histogram, s
	MetricSamplingStream   = "codexlb.sampling_and_stream" // histogram, s
	MetricClientToolPause  = "codexlb.client_tool_pause"   // histogram, s
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

	MetricSelfEnrichLookups        = "codexlb.selfobs.enrich_lookups"         // counter, {lookup}, by codexlb.selfobs.result
	MetricSelfEnrichLookupDuration = "codexlb.selfobs.enrich_lookup_duration" // histogram, s
	MetricSelfEnrichCacheEntries   = "codexlb.selfobs.enrich_cache_entries"   // gauge, {entry}
	MetricArchiveDriftFindings     = "codexlb.archive_drift_findings"         // gauge, {finding}, by codexlb.selfobs.severity
)

// Self-observability dimensions do not describe a Turn and therefore are not in the
// Turn-backed registry. They remain frozen here so the producer and dashboards share
// one spelling.
const (
	SelfObsResult   = "codexlb.selfobs.result"
	SelfObsSeverity = "codexlb.selfobs.severity"
)

// Token type values, per the convention's gen_ai.token.type.
//
// input and output are the convention's own; the cache and reasoning breakdowns are
// not in it but are the numbers that explain a bill, so they extend the same axis
// rather than becoming separate instruments.
// TokenCacheRead was "cached" until issue #32. Renamed for the same reason as the
// operation values above: agent-observability's dashboard matches
// gen_ai_token_type="cache_read" literally, in five separate panels (cache-hit rate,
// cache reads, cache savings, the cache breakdown and the legacy-semantics cost
// fallback), so "cached" made every one of them read zero. Neither spelling is in the
// GenAI registry - its gen_ai.token.type enum defines only input and output, and the
// cache and reasoning breakdowns are this contract's own extension either way - so
// nothing standard was given up by taking the consumer's spelling. TokenCacheWrite
// already matched.
const (
	TokenInput      = "input"
	TokenOutput     = "output"
	TokenReasoning  = "reasoning"
	TokenCacheRead  = "cache_read"
	TokenCacheWrite = "cache_write"
)
