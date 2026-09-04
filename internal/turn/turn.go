// Package turn reduces archive frames into one record per model response.
//
// Two properties of the wire protocol drive this design:
//
//  1. A response's frames are spread across a websocket request_id, so state must be
//     accumulated per request_id and flushed on response.completed.
//
//  2. The server's timing_metrics have timing_scope="logical_turn" and are
//     CUMULATIVE from the start of the logical turn (user message -> final answer),
//     resetting when a new turn begins. Summing them overcounts ~5.7x. This package
//     converts them to per-response deltas; those are what callers should aggregate.
//     usage.* from response.completed is already per-response and passes through.
package turn

import "time"

// Turn is one model response, assembled from every frame sharing a request_id.
type Turn struct {
	// Identity. All high cardinality - log attributes only, never metric labels.
	RequestID      string    `json:"request_id"`
	ResponseID     string    `json:"response_id,omitempty"`
	PrevResponseID string    `json:"previous_response_id,omitempty"`
	SessionID      string    `json:"session_id,omitempty"`
	ThreadID       string    `json:"thread_id,omitempty"`
	ParentThreadID string    `json:"parent_thread_id,omitempty"`
	LogicalTurnID  string    `json:"logical_turn_id,omitempty"`
	LogicalTurnSeq int       `json:"logical_turn_seq,omitempty"`
	InstallationID string    `json:"installation_id,omitempty"`
	WindowID       string    `json:"window_id,omitempty"`
	TraceID        string    `json:"trace_id,omitempty"`
	SpanID         string    `json:"span_id,omitempty"`
	AccountID      string    `json:"account_id,omitempty"`
	SafetyID       string    `json:"safety_identifier,omitempty"`
	FirstTS        time.Time `json:"first_ts"`
	LastTS         time.Time `json:"last_ts"`

	// TurnID is the SERVER's own turn id, from response.create's client_metadata.
	// Authoritative where present; LogicalTurnID falls back to counter inference when
	// it is absent, which prewarm responses legitimately are.
	TurnID string `json:"turn_id,omitempty"`
	// ParentTurnID is the turn that spawned this one. Thread-level parenting only says
	// which conversation a subagent came from; this says which TURN did, which is what
	// lets the span model attach a spawned agent to the exact parent span rather than
	// to the whole thread.
	ParentTurnID string `json:"parent_turn_id,omitempty"`
	// TurnStart is the CLIENT's turn start, so a turn can be measured end to end
	// rather than from when the server began work.
	TurnStart time.Time `json:"turn_start,omitempty"`
	// ClientRequestStart is when the client began this request.
	ClientRequestStart time.Time `json:"client_request_start,omitempty"`
	// Server-side response bounds, independent of when the frames were captured.
	ServerCreatedAt   time.Time `json:"server_created_at,omitempty"`
	ServerCompletedAt time.Time `json:"server_completed_at,omitempty"`

	ForkedFromThreadID string `json:"forked_from_thread_id,omitempty"`
	PromptCacheKey     string `json:"prompt_cache_key,omitempty"`
	RootTurnID         string `json:"root_turn_id,omitempty"`
	AgentName          string `json:"agent_name,omitempty"`
	WindowNumber       int    `json:"window_number,omitempty"`

	// Postgres enrichment fields. CostUSD is a pointer because an explicitly computed
	// zero cost is different from a lookup miss. The proxy fields are codex-lb's view
	// of the same response and are carried to Loki and Tempo, not re-derived here.
	CostUSD                   *float64 `json:"cost_usd,omitempty"`
	APIKeyID                  string   `json:"api_key_id,omitempty"`
	APIKeyName                string   `json:"api_key_name,omitempty"`
	ProxyStatus               string   `json:"proxy_status,omitempty"`
	ProxyErrorCode            string   `json:"proxy_error_code,omitempty"`
	ProxyFailurePhase         string   `json:"proxy_failure_phase,omitempty"`
	ProxyResponseCreatedMS    float64  `json:"proxy_response_created_ms,omitempty"`
	ProxyFirstUpstreamEventMS float64  `json:"proxy_first_upstream_event_ms,omitempty"`

	// Low cardinality - safe as metric attributes.
	Model     string `json:"model,omitempty"`
	Status    string `json:"status,omitempty"`
	Effort    string `json:"reasoning_effort,omitempty"`
	Verbosity string `json:"verbosity,omitempty"`
	// ServiceTier is the tier the SERVER reports, from response.created and
	// response.completed. ServiceTierRequested is what the CLIENT asked for, from
	// response.create. They are two sides of the wire and they do not agree: over the
	// 332 responses following the 2026-08-08T12:13:41Z priority cutover, every request
	// asked for "priority" and every response came back "default" (321) or "auto" (9),
	// never "priority". Whether that is the platform declining to serve priority or
	// simply not reporting it is not decidable from the capture - TTFT moved ~30% in
	// the same window, which says the request is doing something - so both are carried
	// and neither is allowed to overwrite the other. Collapsing them into one field
	// would have made the question unaskable.
	ServiceTier          string `json:"service_tier,omitempty"`
	ServiceTierRequested string `json:"service_tier_requested,omitempty"`
	PlanType             string `json:"plan_type,omitempty"`
	IsSubagent           bool   `json:"is_subagent"`

	// Temperature and TopP are request parameters read from response.completed. #18
	// recorded both as absent - wrong, per #22 item 2: they are present on every
	// completion in the corpus (constant at 1.0 / 0.98 in the 2026-08-07 sample, but
	// that is this traffic's choice, not a wire guarantee, so both are still carried
	// per response rather than assumed fixed). response.max_output_tokens really is
	// always null and is deliberately NOT added.
	Temperature float64 `json:"temperature,omitempty"`
	TopP        float64 `json:"top_p,omitempty"`

	// Family is websocket | http | probe | unknown. Label metrics with this, never with
	// the record's Transport field, which reads "websocket" for every record including
	// the HTTP ones and the synthetic health checks.
	Family string `json:"family,omitempty"`
	// RequestKind is turn | prewarm | compaction | memory, and is more than a label: it
	// is half of the key the reducer diffs cumulative counters against, because the
	// kinds run CONCURRENTLY on one thread with separate counter series. See seriesKey.
	RequestKind           string  `json:"request_kind,omitempty"`
	ThreadSource          string  `json:"thread_source,omitempty"`
	SubagentKind          string  `json:"subagent_kind,omitempty"`
	Sandbox               string  `json:"sandbox,omitempty"`
	SandboxMode           string  `json:"sandbox_mode,omitempty"`
	Originator            string  `json:"originator,omitempty"`
	ClientVersion         string  `json:"client_version,omitempty"`
	ReasoningCtx          string  `json:"reasoning_context,omitempty"`
	ReasoningMode         string  `json:"reasoning_mode,omitempty"`
	CacheRetention        string  `json:"prompt_cache_retention,omitempty"`
	PromptCacheMode       string  `json:"prompt_cache_mode,omitempty"`
	PromptCacheTTL        float64 `json:"prompt_cache_ttl_seconds,omitempty"`
	PromptCacheDiagnostic string  `json:"prompt_cache_diagnostic,omitempty"`
	ParallelTools         bool    `json:"parallel_tool_calls,omitempty"`
	// Lite marks the internal codex-responses-lite path.
	Lite bool `json:"lite,omitempty"`

	// Instructions reach 67 KB and take only a handful of distinct values across a
	// whole day. The hash identifies which system prompt was in play without paying to
	// ship it on every response; the body itself is emitted once, via Prompts.
	InstructionsHash  string `json:"instructions_hash,omitempty"`
	InstructionsChars int    `json:"instructions_chars,omitempty"`

	// Tools/ToolsHash get the identical treatment for the same reason: a re-sent
	// catalogue is boilerplate the moment it repeats. ToolsHash is set on every
	// response that carries a tool catalogue; Tools (the bodies) only on the response
	// where the hash first changes. See applyCreate/captureInput and ToolDef - #22
	// item 1's premise that this lives at response.create's own top-level tools[] is
	// corpus-false (measured 2026-08-08 against all 8,916 response.create events: zero
	// carry a top-level "tools" key). The only tools[] the wire protocol carries is
	// nested inside an "additional_tools" input item - corpus.sig.json's own induced
	// path is input[].tools[], never a bare tools[] - and only 461/8,916 creates (5%)
	// carry one, naming just 5 distinct entries (collaboration, functions, exec, wait,
	// request_user_input). That is a materially smaller and different thing than "the
	// tools available to this agent" #22 wanted a hash of - flagged rather than
	// silently worked around; decode it as-is until product intent is confirmed.
	Tools     []ToolDef `json:"tools,omitempty"`
	ToolsHash string    `json:"tools_hash,omitempty"`

	// Websocket lifecycle. The close code is in the archive's extra block and the
	// reason is PLAIN TEXT in the payload, so a decoder that only parses JSON payloads
	// cannot see a connection die at all.
	CloseCode      *int   `json:"close_code,omitempty"`
	FrameErrors    int    `json:"frame_errors,omitempty"`
	TransportEvent string `json:"transport_event,omitempty"`

	// Error detail, set when Status is StatusError. ErrorType and ErrorCode are
	// enum-like and safe as metric attributes; ErrorMessage embeds ids and is a log
	// field only.
	ErrorType    string `json:"error_type,omitempty"`
	ErrorCode    string `json:"error_code,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`

	// EngineIDs can be a comma-joined list and rotates over time. Log field only.
	EngineIDs string `json:"engine_ids,omitempty"`

	// Per-response usage, safe to sum as-is.
	InputTokens      int     `json:"input_tokens"`
	CachedTokens     int     `json:"cached_tokens"`
	CacheWriteTokens int     `json:"cache_write_tokens"`
	OutputTokens     int     `json:"output_tokens"`
	ReasoningTokens  int     `json:"reasoning_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	CacheHitRatio    float64 `json:"cache_hit_ratio"`
	// Attribution token counts are sums across response.usage.attribution.items,
	// never keyed by the server's per-item identifiers.
	AttributionInputTokens      int `json:"attribution_input_tokens,omitempty"`
	AttributionCachedTokens     int `json:"attribution_cached_tokens,omitempty"`
	AttributionCacheWriteTokens int `json:"attribution_cache_write_tokens,omitempty"`
	AttributionOutputTokens     int `json:"attribution_output_tokens,omitempty"`

	// BaselineReset marks a response whose deltas were computed with no prior
	// cumulative baseline - a cold start, or the first sighting of a thread after a
	// gap. Its deltas are upper bounds that absorb unobserved work, so aggregations
	// wanting accuracy should exclude these.
	BaselineReset bool `json:"baseline_reset,omitempty"`

	// Per-response deltas derived from the cumulative logical-turn metrics.
	EngineCallsDelta        int     `json:"engine_calls_delta"`
	TurnTimeSecondsDelta    float64 `json:"turn_time_seconds_delta"`
	SampledTokensDelta      int     `json:"sampled_tokens_delta"`
	EnginePromptTokensDelta int     `json:"engine_prompt_tokens_delta"`
	EngineCachedTokensDelta int     `json:"engine_cached_tokens_delta"`
	ClientToolPauseMsDelta  float64 `json:"client_tool_pause_ms_delta"`

	// The nine fields below are cumulative over the logical turn exactly like the
	// block above - measured on the full 2026-08-07 corpus (#12's corrective comment,
	// not the issue body, which wrongly called several of these absent or wrongly
	// scoped the taas_* split as deliverable when every taas_* value in the corpus is
	// null). Each decrease-fraction matches num_engine_calls's own turn-boundary rate
	// exactly, so they share its baseline and reset rules. EngineUncachedPromptTokens
	// is engine_uncached_prompt_tokens_total's sibling to the already-decoded
	// engine_total_prompt_tokens_total (EnginePromptTokensDelta above) - only the
	// uncached one was ever missing.
	EngineServiceInferenceMsDelta              float64 `json:"engine_service_inference_ms_delta,omitempty"`
	EngineServiceSamplingMsDelta               float64 `json:"engine_service_sampling_ms_delta,omitempty"`
	EngineIapiInferenceMsDelta                 float64 `json:"engine_iapi_inference_ms_delta,omitempty"`
	EngineIapiSamplingMsDelta                  float64 `json:"engine_iapi_sampling_ms_delta,omitempty"`
	ResponsesExclEngineAndToolMsDelta          float64 `json:"responses_excl_engine_and_tool_ms_delta,omitempty"`
	ResponsesExclEngineWaitSamplingMsDelta     float64 `json:"responses_excl_engine_wait_sampling_ms_delta,omitempty"`
	ResponsesExclEngineWaitSamplingIapiMsDelta float64 `json:"responses_excl_engine_wait_sampling_iapi_ms_delta,omitempty"`
	ResponsesAPIExclClientToolsMsDelta         float64 `json:"responsesapi_excl_client_tools_ms_delta,omitempty"`
	EngineUncachedPromptTokensDelta            int     `json:"engine_uncached_prompt_tokens_delta,omitempty"`

	// CriticalPath is the server's OWN per-response breakdown. Prefer it to the delta
	// fields above wherever it is populated.
	CriticalPath CriticalPath `json:"critical_path"`

	// Point-in-time timings, not cumulative.
	TTFTMs           float64 `json:"ttft_ms,omitempty"`
	PreInferenceMs   float64 `json:"pre_inference_ms,omitempty"`
	EngineQueueMaxMs float64 `json:"engine_queue_max_ms,omitempty"`

	// The three TBT (time-between-tokens) fields below are RUNNING AVERAGES across
	// engine calls, not sums - #12's corpus measurement proved this by construction:
	// EngineServiceMinusIapiTBTMs goes negative (min -6.70), which a monotonic
	// cumulative sum can never do. Delta-ing them would silently corrupt the
	// arithmetic, so they are used exactly as reported, per response.
	EngineServiceTBTMs          float64 `json:"engine_service_tbt_ms,omitempty"`
	EngineIapiTBTMs             float64 `json:"engine_iapi_tbt_ms,omitempty"`
	EngineServiceMinusIapiTBTMs float64 `json:"engine_service_minus_iapi_tbt_ms,omitempty"`

	// Streaming shape, from the timing block. Delta count and time-between-tokens
	// describe how the response was delivered rather than how long it took.
	FirstMsgTTFTMs     float64 `json:"first_message_ttft_ms,omitempty"`
	FirstMsgReasoning  int     `json:"first_message_reasoning_tokens,omitempty"`
	TextDeltaCount     int     `json:"ws_text_delta_count,omitempty"`
	TextDeltaTBTMs     float64 `json:"ws_text_delta_tbt_ms,omitempty"`
	PauseCount         int     `json:"pause_count,omitempty"`
	EngineServiceTotal float64 `json:"engine_service_total_ms,omitempty"`
	EngineServiceTTFT  float64 `json:"engine_service_ttft_ms,omitempty"`
	EngineIapiTTFT     float64 `json:"engine_iapi_ttft_ms,omitempty"`

	// Tool usage billed separately from the response's own tokens.
	WebSearchRequests int `json:"web_search_requests,omitempty"`
	ImageGenTokens    int `json:"image_gen_total_tokens,omitempty"`

	// SafetyBuffering fires when the platform re-runs a response through a different
	// model for safety reasons. Rare - 2 responses in 8,078 - and worth seeing.
	SafetyBuffering  bool     `json:"safety_buffering,omitempty"`
	SafetyRetryModel string   `json:"safety_retry_model,omitempty"`
	SafetyUseCases   []string `json:"safety_use_cases,omitempty"`
	// SafetyReasons is the platform's own account of WHY it buffered - the only field
	// of the block that explains rather than describes. Captured empty on both observed
	// occurrences, so it is carried on the strength of what it means rather than of
	// what it has so far contained.
	SafetyReasons []string `json:"safety_reasons,omitempty"`

	// Rate limits, as reported alongside this response. codex-lb balances across
	// several accounts, so these are per-account headroom readings and only mean
	// anything when grouped by AccountID.
	RateLimitUsedPercent  float64 `json:"rate_limit_used_percent,omitempty"`
	RateLimitWindowMin    int     `json:"rate_limit_window_minutes,omitempty"`
	RateLimitResetSeconds float64 `json:"rate_limit_reset_after_seconds,omitempty"`
	RateLimitReached      bool    `json:"rate_limit_reached"`
	RateLimitAllowed      bool    `json:"rate_limit_allowed,omitempty"`

	// Secondary window, when the plan has one.
	RateLimit2UsedPercent  float64 `json:"rate_limit_secondary_used_percent,omitempty"`
	RateLimit2WindowMin    int     `json:"rate_limit_secondary_window_minutes,omitempty"`
	RateLimit2ResetSeconds float64 `json:"rate_limit_secondary_reset_after_seconds,omitempty"`

	// ExtraRateLimits is keyed by model name - the server reports separate headroom
	// for models such as GPT-5.3-Codex-Spark. Bounded by the number of models on the
	// plan, so it is safe to fan out into per-model gauges. Each model can carry both
	// the primary and secondary plan windows; retaining only the percentage loses
	// which quota the reading describes.
	ExtraRateLimits map[string][]RateLimitWindow `json:"extra_rate_limits,omitempty"`

	CreditsBalance   string `json:"credits_balance,omitempty"`
	CreditsUnlimited bool   `json:"credits_unlimited,omitempty"`
	HasCredits       bool   `json:"has_credits,omitempty"`

	// Activity.
	ToolCalls     []ToolCall     `json:"tool_calls,omitempty"`
	Messages      []Message      `json:"messages,omitempty"`
	Prompts       []Prompt       `json:"prompts,omitempty"`
	ToolOutputs   []ToolOutput   `json:"tool_outputs,omitempty"`
	AgentMessages []AgentMessage `json:"agent_messages,omitempty"`
	InputItems    int            `json:"input_items,omitempty"`

	// ToolCallDurationsMs is a SUFFIX DIFF, not the server's raw reading. The wire
	// field (responsesapi_duration_excl_engine_per_tool_call_ms) accumulates one entry
	// per tool call across the whole logical turn and only resets at the turn
	// boundary - #12's corpus measurement proved it shrinks in exactly the responses
	// where the num_engine_calls turn-boundary detector also fires (300/8,663 pairs,
	// identical numerator and denominator). So this carries only the entries newly
	// added since the previous response of the same series, the same "newly seen, not
	// total" principle InputImages already uses. See applyTiming.
	ToolCallDurationsMs []float64 `json:"tool_call_durations_ms,omitempty"`
	// ToolCallDurationsTruncated is the companion flag, read directly (never diffed) -
	// responsesapi_duration_excl_engine_per_tool_call_TRUNCATED, which drops the "_ms"
	// its sibling carries. Missing the rename once already cost a round trip. It is
	// null on 8,853 of 8,883 corpus events and true on the other 30, never false.
	ToolCallDurationsTruncated bool `json:"tool_call_durations_truncated,omitempty"`
	// InputImages counts image parts newly seen on this response - multimodal input,
	// which the capture proves exists and which nothing else in the pipeline records.
	// Newly seen, not total: response.create re-sends the whole history every turn, so
	// a running total would count one screenshot once per remaining turn of the thread.
	InputImages  int            `json:"input_images,omitempty"`
	ItemCounts   map[string]int `json:"item_counts,omitempty"`
	TextDeltas   int            `json:"text_deltas"`
	ToolDeltas   int            `json:"tool_input_deltas"`
	Frames       int            `json:"frames"`
	Bytes        int            `json:"bytes"`
	ReasoningEnc int            `json:"reasoning_encrypted_chars,omitempty"`
}

// RateLimitWindow is one quota window reported for an account or model.
type RateLimitWindow struct {
	UsedPercent  float64 `json:"used_percent"`
	WindowMin    int     `json:"window_minutes"`
	ResetSeconds float64 `json:"reset_after_seconds,omitempty"`
}

// ToolDef is one entry of a re-sent tool catalogue, deduplicated onto Turn.Tools by
// Turn.ToolsHash. Deliberately shallow: Kind and Name are enum-like and Description
// is prose, all cheap to carry, but a tool's parameters/output_schema are
// client-authored JSON Schema of unbounded shape - internal/profile.opaqueKind exists
// because walking that subtree once produced 217 spurious findings from a single new
// tool, and one observed schema alone ran to ~190 properties. Decoding it here would
// import the exact problem that guard was built to avoid.
type ToolDef struct {
	Name        string `json:"name"`
	Kind        string `json:"kind,omitempty"` // the wire's `type`
	Description string `json:"description,omitempty"`
	Strict      bool   `json:"strict,omitempty"`
}

// CriticalPath is the server's per-response timing breakdown.
//
// It sits inside the same timing_metrics block as the cumulative logical-turn
// counters, but declares scope="response" and is genuinely per-response - no delta
// arithmetic required. It also self-reports its own reliability: Coverage is
// "complete" or "missing_harness_boundary", the latter on about 4.6% of responses,
// where the numbers should not be trusted.
type CriticalPath struct {
	Coverage     string  `json:"coverage,omitempty"`
	BoundaryType string  `json:"boundary_type,omitempty"`
	EngineCalls  int     `json:"engine_calls,omitempty"`
	EngineWallMs float64 `json:"engine_wall_ms,omitempty"`
	EngineIapiMs float64 `json:"engine_iapi_total_ms,omitempty"`
	// HarnessUnblockedMs is how long the harness spent unblocked - the server-side
	// share of the response as opposed to time waiting on the client.
	HarnessUnblockedMs float64 `json:"harness_unblocked_ms,omitempty"`
	PreInferenceMs     float64 `json:"pre_inference_ms,omitempty"`
	SamplingStreamMs   float64 `json:"sampling_and_stream_ms,omitempty"`
	ClientToolPauseMs  float64 `json:"client_tool_pause_ms,omitempty"`
	OtherMs            float64 `json:"other_ms,omitempty"`
}

// Complete reports whether the server considered this breakdown trustworthy.
func (c CriticalPath) Complete() bool { return c.Coverage == "complete" }

// AgentMessage is one message passed between agents in the spawned-agent tree.
//
// Author and Recipient are task paths such as "/root/lab_baseline" and "/root", so
// these reconstruct the actual multi-agent communication topology - which exists
// nowhere else in the capture.
type AgentMessage struct {
	Author    string `json:"author,omitempty"`
	Recipient string `json:"recipient,omitempty"`
	Chars     int    `json:"chars"`
	Text      string `json:"text,omitempty"`
}

// ToolCall is one tool invocation the model made.
type ToolCall struct {
	Kind       string `json:"kind"` // custom | function
	Name       string `json:"name"`
	CallID     string `json:"call_id,omitempty"`
	Status     string `json:"status,omitempty"`
	InputChars int    `json:"input_chars"`
	Input      string `json:"input,omitempty"`

	// Populated for spawn_agent, describing the child agent.
	TaskName  string `json:"task_name,omitempty"`
	SubModel  string `json:"sub_model,omitempty"`
	SubEffort string `json:"sub_effort,omitempty"`
}

// Message is an assistant message emitted during the response.
type Message struct {
	Phase string `json:"phase,omitempty"`
	Chars int    `json:"chars"`
	Text  string `json:"text,omitempty"`
}

// Prompt is an input-side message: what the user or the harness asked for.
//
// These are recovered from response.create, which re-sends the entire conversation
// on every turn, so each is emitted only on the first response that carries it.
// Without this the archive records only the model's half of the conversation.
type Prompt struct {
	Role  string `json:"role"` // user | developer | assistant
	Chars int    `json:"chars"`
	Text  string `json:"text,omitempty"`
	// Images counts image parts on this message. The images themselves are never
	// carried: they arrive as base64 data URIs measured at up to 784 KB each, and a
	// Loki line over the limit is discarded whole rather than truncated, so inlining
	// one would destroy the message it belongs to. See turn.imageParts.
	Images int `json:"images,omitempty"`
	// ImageMIME is the media type the data URI declared for the FIRST image part, e.g.
	// "image/png". Read from the URI's own declaration, never sniffed from the bytes,
	// so it stays bounded. Empty when the images did not declare one - "an image was
	// here" is worth little on its own, but a guessed type would be worth less.
	ImageMIME string `json:"image_mime,omitempty"`
}

// ToolOutput is the result fed back for a tool call - command output, file contents,
// and so on. Recovered from response.create like Prompt, and deduplicated by call id.
//
// Text is truncated: these carry whole command outputs and are the single largest
// content source in the archive. Chars always records the untruncated length.
type ToolOutput struct {
	CallID    string `json:"call_id,omitempty"`
	Chars     int    `json:"chars"`
	Truncated bool   `json:"truncated,omitempty"`
	Text      string `json:"text,omitempty"`
}

// cumulative snapshots the logical-turn counters so the next response can be diffed
// against them.
type cumulative struct {
	engineCalls   int
	turnTimeS     float64
	sampledTokens int
	promptTokens  int
	cachedTokens  int
	toolPauseMs   float64

	// The nine fields #12's corpus comment added, same series/reset rules as above.
	serviceInferenceMs           float64
	serviceSamplingMs            float64
	iapiInferenceMs              float64
	iapiSamplingMs               float64
	exclEngineAndToolMs          float64
	exclEngineWaitSamplingMs     float64
	exclEngineWaitSamplingIapiMs float64
	exclClientToolsMs            float64
	uncachedPromptTokens         int

	// toolCallDurationsMs is the last cumulative reading of the per-tool-call array,
	// kept so applyTiming can suffix-diff the next one against it rather than against
	// the values already reported. Unlike the scalar fields above this needs no
	// separate reset flag: a shorter new array than this one IS the reset signal, by
	// construction - see applyTiming.
	toolCallDurationsMs []float64
}
