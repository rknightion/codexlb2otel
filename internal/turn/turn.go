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

	// Low cardinality - safe as metric attributes.
	Model       string `json:"model,omitempty"`
	Status      string `json:"status,omitempty"`
	Effort      string `json:"reasoning_effort,omitempty"`
	Verbosity   string `json:"verbosity,omitempty"`
	ServiceTier string `json:"service_tier,omitempty"`
	PlanType    string `json:"plan_type,omitempty"`
	IsSubagent  bool   `json:"is_subagent"`

	// Family is websocket | http | probe. Label metrics with this, never with the
	// record's Transport field, which reads "websocket" for every record including the
	// HTTP ones and the synthetic health checks.
	Family string `json:"family,omitempty"`
	// RequestKind is turn | prewarm. A prewarm is a speculative warmup that does no
	// engine work and reports no counters; counting it as a turn inflates turn rates.
	RequestKind    string `json:"request_kind,omitempty"`
	ThreadSource   string `json:"thread_source,omitempty"`
	SubagentKind   string `json:"subagent_kind,omitempty"`
	Sandbox        string `json:"sandbox,omitempty"`
	Originator     string `json:"originator,omitempty"`
	ClientVersion  string `json:"client_version,omitempty"`
	ReasoningCtx   string `json:"reasoning_context,omitempty"`
	ReasoningMode  string `json:"reasoning_mode,omitempty"`
	CacheRetention string `json:"prompt_cache_retention,omitempty"`
	ParallelTools  bool   `json:"parallel_tool_calls,omitempty"`
	// Lite marks the internal codex-responses-lite path.
	Lite bool `json:"lite,omitempty"`

	// Instructions reach 67 KB and take only a handful of distinct values across a
	// whole day. The hash identifies which system prompt was in play without paying to
	// ship it on every response; the body itself is emitted once, via Prompts.
	InstructionsHash  string `json:"instructions_hash,omitempty"`
	InstructionsChars int    `json:"instructions_chars,omitempty"`

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

	// CriticalPath is the server's OWN per-response breakdown. Prefer it to the delta
	// fields above wherever it is populated.
	CriticalPath CriticalPath `json:"critical_path"`

	// Point-in-time timings, not cumulative.
	TTFTMs           float64 `json:"ttft_ms,omitempty"`
	PreInferenceMs   float64 `json:"pre_inference_ms,omitempty"`
	EngineQueueMaxMs float64 `json:"engine_queue_max_ms,omitempty"`

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
	// model for safety reasons. Rare - 4 responses in 4000 - and worth seeing.
	SafetyBuffering  bool     `json:"safety_buffering,omitempty"`
	SafetyRetryModel string   `json:"safety_retry_model,omitempty"`
	SafetyUseCases   []string `json:"safety_use_cases,omitempty"`

	// Rate limits, as reported alongside this response. codex-lb balances across
	// several accounts, so these are per-account headroom readings and only mean
	// anything when grouped by AccountID.
	RateLimitUsedPercent  float64 `json:"rate_limit_used_percent,omitempty"`
	RateLimitWindowMin    int     `json:"rate_limit_window_minutes,omitempty"`
	RateLimitResetSeconds float64 `json:"rate_limit_reset_after_seconds,omitempty"`
	RateLimitReached      bool    `json:"rate_limit_reached"`
	RateLimitAllowed      bool    `json:"rate_limit_allowed,omitempty"`

	// Secondary window, when the plan has one.
	RateLimit2UsedPercent float64 `json:"rate_limit_secondary_used_percent,omitempty"`
	RateLimit2WindowMin   int     `json:"rate_limit_secondary_window_minutes,omitempty"`

	// ExtraRateLimits is keyed by model name - the server reports separate headroom
	// for models such as GPT-5.3-Codex-Spark. Bounded by the number of models on the
	// plan, so it is safe to fan out into per-model gauges.
	ExtraRateLimits map[string]float64 `json:"extra_rate_limits,omitempty"`

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
	ItemCounts    map[string]int `json:"item_counts,omitempty"`
	TextDeltas    int            `json:"text_deltas"`
	ToolDeltas    int            `json:"tool_input_deltas"`
	Frames        int            `json:"frames"`
	Bytes         int            `json:"bytes"`
	ReasoningEnc  int            `json:"reasoning_encrypted_chars,omitempty"`
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
}
