package turn

import (
	"encoding/json"
	"fmt"

	"github.com/rknightion/codexlb2otel/internal/frame"
)

// Reducer accumulates frames into Turns. It is not safe for concurrent use.
//
// State is per-request_id while a response streams, plus per-thread cumulative
// counters used to convert the server's logical-turn metrics into per-response
// deltas. Both must survive a process restart or the first turn after one produces
// a bogus delta - see Snapshot/Restore.
type Reducer struct {
	open map[string]*Turn      // request_id -> in-flight turn
	prev map[string]cumulative // thread_id -> last cumulative snapshot
	seq  map[string]int        // thread_id -> logical turn counter
}

// New returns an empty Reducer.
func New() *Reducer {
	return &Reducer{
		open: map[string]*Turn{},
		prev: map[string]cumulative{},
		seq:  map[string]int{},
	}
}

// Add feeds one frame in. It returns a completed Turn when the frame closed one,
// otherwise nil. A frame whose payload is unparseable is counted and ignored.
func (r *Reducer) Add(rec *frame.Record) (*Turn, error) {
	t := r.open[rec.RequestID]
	if t == nil {
		t = &Turn{
			RequestID:      rec.RequestID,
			SessionID:      rec.Header(frame.HdrSession),
			ThreadID:       rec.Header(frame.HdrThread),
			ParentThreadID: rec.Header(frame.HdrParentThread),
			InstallationID: rec.Header(frame.HdrInstallation),
			WindowID:       rec.Header(frame.HdrWindow),
			AccountID:      rec.AccountID,
			IsSubagent:     rec.IsSubagent(),
			FirstTS:        rec.Timestamp,
			ItemCounts:     map[string]int{},
		}
		t.TraceID, t.SpanID = rec.Trace()
		r.open[rec.RequestID] = t
	}
	t.LastTS = rec.Timestamp
	t.Frames++
	t.Bytes += len(rec.Payload.Text)

	ev, ok := rec.ParseEvent()
	if !ok {
		return nil, nil
	}

	// Deltas dominate volume; count and discard without decoding.
	if frame.IsDelta(ev.Type) {
		switch ev.Type {
		case frame.EvOutputTextDelta:
			t.TextDeltas++
		default:
			t.ToolDeltas++
		}
		return nil, nil
	}

	switch ev.Type {
	case frame.EvResponseCreate:
		r.applyCreate(t, ev)
	case frame.EvRateLimits:
		r.applyRateLimits(t, ev)
	case frame.EvResponseCreated:
		r.applyCreated(t, ev)
	case frame.EvOutputItemDone:
		r.applyOutputItem(t, ev)
	case frame.EvWebsocketTiming:
		r.applyTiming(t, ev)
	case frame.EvResponseCompleted:
		if err := r.applyCompleted(t, ev); err != nil {
			return nil, err
		}
		r.ensureLogicalTurn(t)
		delete(r.open, rec.RequestID)
		return t, nil
	}
	return nil, nil
}

// ensureLogicalTurn assigns an id to a response that never reported timing metrics.
// Prewarm responses do no engine work and carry no cumulative series, so each is its
// own logical turn - there is nothing for it to be a continuation of.
func (r *Reducer) ensureLogicalTurn(t *Turn) {
	if t.LogicalTurnID != "" {
		return
	}
	r.seq[t.ThreadID]++
	t.LogicalTurnSeq = r.seq[t.ThreadID]
	t.LogicalTurnID = fmt.Sprintf("%s:%d", t.ThreadID, t.LogicalTurnSeq)
}

type createEvent struct {
	Model     string `json:"model"`
	Reasoning struct {
		Effort string `json:"effort"`
	} `json:"reasoning"`
	Text struct {
		Verbosity string `json:"verbosity"`
	} `json:"text"`
}

func (r *Reducer) applyCreate(t *Turn, ev frame.Event) {
	var c createEvent
	if ev.Decode(&c) != nil {
		return
	}
	if t.Model == "" {
		t.Model = c.Model
	}
	t.Effort = c.Reasoning.Effort
	t.Verbosity = c.Text.Verbosity
}

type rateLimitEvent struct {
	PlanType   string `json:"plan_type"`
	RateLimits struct {
		LimitReached bool `json:"limit_reached"`
		Primary      *struct {
			UsedPercent       float64 `json:"used_percent"`
			WindowMinutes     int     `json:"window_minutes"`
			ResetAfterSeconds float64 `json:"reset_after_seconds"`
		} `json:"primary"`
	} `json:"rate_limits"`
}

func (r *Reducer) applyRateLimits(t *Turn, ev frame.Event) {
	var e rateLimitEvent
	if ev.Decode(&e) != nil {
		return
	}
	t.PlanType = e.PlanType
	t.RateLimitReached = e.RateLimits.LimitReached
	if p := e.RateLimits.Primary; p != nil {
		t.RateLimitUsedPercent = p.UsedPercent
		t.RateLimitWindowMin = p.WindowMinutes
		t.RateLimitResetSeconds = p.ResetAfterSeconds
	}
}

type createdEvent struct {
	Response struct {
		ID          string `json:"id"`
		Model       string `json:"model"`
		ServiceTier string `json:"service_tier"`
		SafetyID    string `json:"safety_identifier"`
	} `json:"response"`
}

func (r *Reducer) applyCreated(t *Turn, ev frame.Event) {
	var e createdEvent
	if ev.Decode(&e) != nil {
		return
	}
	t.ResponseID = e.Response.ID
	t.ServiceTier = e.Response.ServiceTier
	t.SafetyID = e.Response.SafetyID
	if t.Model == "" {
		t.Model = e.Response.Model
	}
}

type outputItemEvent struct {
	Item struct {
		Type      string `json:"type"`
		Name      string `json:"name"`
		CallID    string `json:"call_id"`
		Status    string `json:"status"`
		Input     string `json:"input"`
		Arguments string `json:"arguments"`
		Phase     string `json:"phase"`
		Encrypted string `json:"encrypted_content"`
		Content   []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"item"`
}

// spawnArgs is the subset of spawn_agent arguments worth keeping. The `message`
// field is an OpenAI-encrypted blob and is deliberately dropped.
type spawnArgs struct {
	TaskName string `json:"task_name"`
	Model    string `json:"model"`
	Effort   string `json:"reasoning_effort"`
}

func (r *Reducer) applyOutputItem(t *Turn, ev frame.Event) {
	var e outputItemEvent
	if ev.Decode(&e) != nil {
		return
	}
	it := e.Item
	t.ItemCounts[it.Type]++

	switch it.Type {
	case "custom_tool_call":
		t.ToolCalls = append(t.ToolCalls, ToolCall{
			Kind: "custom", Name: it.Name, CallID: it.CallID,
			Status: it.Status, InputChars: len(it.Input), Input: it.Input,
		})
	case "function_call":
		tc := ToolCall{
			Kind: "function", Name: it.Name, CallID: it.CallID,
			Status: it.Status, InputChars: len(it.Arguments),
		}
		var a spawnArgs
		if json.Unmarshal([]byte(it.Arguments), &a) == nil {
			tc.TaskName, tc.SubModel, tc.SubEffort = a.TaskName, a.Model, a.Effort
		}
		t.ToolCalls = append(t.ToolCalls, tc)
	case "message":
		var body string
		for _, c := range it.Content {
			if c.Type == "output_text" {
				body += c.Text
			}
		}
		t.Messages = append(t.Messages, Message{Phase: it.Phase, Chars: len(body), Text: body})
	case "reasoning":
		// Content is Fernet-encrypted by OpenAI and cannot be read. Size only.
		t.ReasoningEnc += len(it.Encrypted)
	}
}

type timingEvent struct {
	M struct {
		TotalTurnTimeS     *float64 `json:"total_turn_time_s"`
		PreInferenceMs     *float64 `json:"pre_inference_ms"`
		FirstTTFTMs        *float64 `json:"first_sampled_message_ttft_ms"`
		EngineQueueMaxMs   *float64 `json:"engine_queue_max_ms"`
		NumEngineCalls     *int     `json:"num_engine_calls"`
		EngineIDs          *string  `json:"engine_ids"`
		PromptTokensTotal  *int     `json:"engine_total_prompt_tokens_total"`
		CachedTokensTotal  *int     `json:"engine_cached_prompt_tokens_total"`
		SampledTokensTotal *int     `json:"num_sampled_tokens_total"`
		ClientToolPauseMs  *float64 `json:"client_tool_pause_total_ms"`
	} `json:"timing_metrics"`
}

// applyTiming converts the cumulative logical-turn counters into per-response
// deltas. This is the single most important correctness property in the package:
// the raw values accumulate across a turn and reset at the next one, so summing
// them directly overcounts by roughly 5.7x.
func (r *Reducer) applyTiming(t *Turn, ev frame.Event) {
	var e timingEvent
	if ev.Decode(&e) != nil {
		return
	}
	m := e.M

	if m.EngineIDs != nil {
		t.EngineIDs = *m.EngineIDs
	}
	if m.PreInferenceMs != nil {
		t.PreInferenceMs = *m.PreInferenceMs
	}
	if m.FirstTTFTMs != nil {
		t.TTFTMs = *m.FirstTTFTMs
	}
	if m.EngineQueueMaxMs != nil {
		t.EngineQueueMaxMs = *m.EngineQueueMaxMs
	}

	cur := cumulative{}
	if m.NumEngineCalls != nil {
		cur.engineCalls = *m.NumEngineCalls
	}
	if m.TotalTurnTimeS != nil {
		cur.turnTimeS = *m.TotalTurnTimeS
	}
	if m.SampledTokensTotal != nil {
		cur.sampledTokens = *m.SampledTokensTotal
	}
	if m.PromptTokensTotal != nil {
		cur.promptTokens = *m.PromptTokensTotal
	}
	if m.CachedTokensTotal != nil {
		cur.cachedTokens = *m.CachedTokensTotal
	}
	if m.ClientToolPauseMs != nil {
		cur.toolPauseMs = *m.ClientToolPauseMs
	}

	prev, seen := r.prev[t.ThreadID]

	// A new logical turn begins whenever the cumulative engine-call counter fails to
	// advance. Validated across 87 turns of live traffic: zero negative deltas.
	if !seen || m.NumEngineCalls == nil || cur.engineCalls <= prev.engineCalls {
		r.seq[t.ThreadID]++
		prev = cumulative{}
	}
	t.LogicalTurnSeq = r.seq[t.ThreadID]
	t.LogicalTurnID = fmt.Sprintf("%s:%d", t.ThreadID, t.LogicalTurnSeq)

	t.EngineCallsDelta = cur.engineCalls - prev.engineCalls
	t.TurnTimeSecondsDelta = cur.turnTimeS - prev.turnTimeS
	t.SampledTokensDelta = cur.sampledTokens - prev.sampledTokens
	t.EnginePromptTokensDelta = cur.promptTokens - prev.promptTokens
	t.EngineCachedTokensDelta = cur.cachedTokens - prev.cachedTokens
	t.ClientToolPauseMsDelta = cur.toolPauseMs - prev.toolPauseMs

	// Only advance the baseline once the server actually reported engine work;
	// prewarm responses report no counters and must not reset it.
	if m.NumEngineCalls != nil {
		r.prev[t.ThreadID] = cur
	}
}

type completedEvent struct {
	Response struct {
		Status string `json:"status"`
		Model  string `json:"model"`
		Usage  *struct {
			InputTokens        int `json:"input_tokens"`
			OutputTokens       int `json:"output_tokens"`
			TotalTokens        int `json:"total_tokens"`
			InputTokensDetails struct {
				CachedTokens     int `json:"cached_tokens"`
				CacheWriteTokens int `json:"cache_write_tokens"`
			} `json:"input_tokens_details"`
			OutputTokensDetails struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
	} `json:"response"`
}

func (r *Reducer) applyCompleted(t *Turn, ev frame.Event) error {
	var e completedEvent
	if err := ev.Decode(&e); err != nil {
		return fmt.Errorf("turn: decode response.completed: %w", err)
	}
	t.Status = e.Response.Status
	if e.Response.Model != "" {
		t.Model = e.Response.Model
	}
	if u := e.Response.Usage; u != nil {
		t.InputTokens = u.InputTokens
		t.OutputTokens = u.OutputTokens
		t.TotalTokens = u.TotalTokens
		t.CachedTokens = u.InputTokensDetails.CachedTokens
		t.CacheWriteTokens = u.InputTokensDetails.CacheWriteTokens
		t.ReasoningTokens = u.OutputTokensDetails.ReasoningTokens
		if u.InputTokens > 0 {
			t.CacheHitRatio = float64(u.InputTokensDetails.CachedTokens) / float64(u.InputTokens)
		}
	}
	return nil
}

// Open reports how many responses are still streaming. A steadily growing value
// means responses are never completing, which is worth alerting on.
func (r *Reducer) Open() int { return len(r.open) }
