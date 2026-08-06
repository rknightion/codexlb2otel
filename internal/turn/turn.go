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

	// Low cardinality - safe as metric attributes.
	Model       string `json:"model,omitempty"`
	Status      string `json:"status,omitempty"`
	Effort      string `json:"reasoning_effort,omitempty"`
	Verbosity   string `json:"verbosity,omitempty"`
	ServiceTier string `json:"service_tier,omitempty"`
	PlanType    string `json:"plan_type,omitempty"`
	IsSubagent  bool   `json:"is_subagent"`

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

	// Per-response deltas derived from the cumulative logical-turn metrics.
	EngineCallsDelta        int     `json:"engine_calls_delta"`
	TurnTimeSecondsDelta    float64 `json:"turn_time_seconds_delta"`
	SampledTokensDelta      int     `json:"sampled_tokens_delta"`
	EnginePromptTokensDelta int     `json:"engine_prompt_tokens_delta"`
	EngineCachedTokensDelta int     `json:"engine_cached_tokens_delta"`
	ClientToolPauseMsDelta  float64 `json:"client_tool_pause_ms_delta"`

	// Point-in-time timings, not cumulative.
	TTFTMs           float64 `json:"ttft_ms,omitempty"`
	PreInferenceMs   float64 `json:"pre_inference_ms,omitempty"`
	EngineQueueMaxMs float64 `json:"engine_queue_max_ms,omitempty"`

	// Rate limits, as reported alongside this response.
	RateLimitUsedPercent  float64 `json:"rate_limit_used_percent,omitempty"`
	RateLimitWindowMin    int     `json:"rate_limit_window_minutes,omitempty"`
	RateLimitResetSeconds float64 `json:"rate_limit_reset_after_seconds,omitempty"`
	RateLimitReached      bool    `json:"rate_limit_reached"`

	// Activity.
	ToolCalls    []ToolCall     `json:"tool_calls,omitempty"`
	Messages     []Message      `json:"messages,omitempty"`
	ItemCounts   map[string]int `json:"item_counts,omitempty"`
	TextDeltas   int            `json:"text_deltas"`
	ToolDeltas   int            `json:"tool_input_deltas"`
	Frames       int            `json:"frames"`
	Bytes        int            `json:"bytes"`
	ReasoningEnc int            `json:"reasoning_encrypted_chars,omitempty"`
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
