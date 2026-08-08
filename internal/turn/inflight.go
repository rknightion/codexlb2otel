package turn

import (
	"sort"
	"time"
)

// InFlight is a read-only view of one response the reducer is still accumulating.
//
// It exists because "what is this agent doing right now" is unanswerable from completed
// turns alone: a Turn only materialises on response.completed, so a completed-only view
// goes silent for exactly the window in which the agent is working. This is the other
// half.
//
// It is a VALUE COPY, deliberately, and carries no pointer into the reducer's state.
// The open map is mutated by the poll goroutine, so anything a reader keeps hold of has
// to be detached from it - handing out the *Turn itself would let a reader walk slices
// that Add is appending to.
type InFlight struct {
	RequestID      string    `json:"request_id"`
	ResponseID     string    `json:"response_id,omitempty"`
	ThreadID       string    `json:"thread_id,omitempty"`
	ParentThreadID string    `json:"parent_thread_id,omitempty"`
	TurnID         string    `json:"turn_id,omitempty"`
	ParentTurnID   string    `json:"parent_turn_id,omitempty"`
	IsSubagent     bool      `json:"is_subagent"`
	SubagentKind   string    `json:"subagent_kind,omitempty"`
	Model          string    `json:"model,omitempty"`
	Effort         string    `json:"reasoning_effort,omitempty"`
	RequestKind    string    `json:"request_kind,omitempty"`
	Family         string    `json:"family,omitempty"`
	FirstTS        time.Time `json:"first_ts"`
	LastTS         time.Time `json:"last_ts"`

	// TextDeltas and ToolDeltas are the only progress signal available before
	// completion: usage.* arrives with response.completed and is zero until then, so a
	// token count would read "0" for every in-flight response and look stalled.
	TextDeltas int `json:"text_deltas"`
	ToolDeltas int `json:"tool_input_deltas"`

	// LastToolCall is the most recent tool the model invoked on this response, which is
	// the closest thing the wire carries to "what it is doing". Empty while it is still
	// reasoning or writing prose.
	LastToolCall string `json:"last_tool_call,omitempty"`
	// LastToolInput is that call's arguments, already truncated by the reducer's
	// MaxToolOutputChars. Content - a caller with content disabled must not surface it.
	LastToolInput string `json:"last_tool_input,omitempty"`
	// SpawnedTask is the child task name when the last call was a spawn, so a live view
	// can show a subagent starting before that subagent's own first response exists.
	SpawnedTask string `json:"spawned_task,omitempty"`
	// LastMessage is the tail of the assistant's own prose on this response. Content.
	LastMessage string `json:"last_message,omitempty"`
}

// InFlight snapshots every response currently open, oldest first.
//
// CONCURRENCY: this walks the same map Add writes to, so it carries the same contract
// as Open() - the caller must hold whatever lock serialises its own calls to Add. It
// takes none of its own, because the reducer has none; see tail.Watcher.mu, which is
// the lock that actually does this job in the running service.
func (r *Reducer) InFlight() []InFlight {
	out := make([]InFlight, 0, len(r.open))
	for _, t := range r.open {
		f := InFlight{
			RequestID:      t.RequestID,
			ResponseID:     t.ResponseID,
			ThreadID:       t.ThreadID,
			ParentThreadID: t.ParentThreadID,
			TurnID:         t.TurnID,
			ParentTurnID:   t.ParentTurnID,
			IsSubagent:     t.IsSubagent,
			SubagentKind:   t.SubagentKind,
			Model:          t.Model,
			Effort:         t.Effort,
			RequestKind:    t.RequestKind,
			Family:         t.Family,
			FirstTS:        t.FirstTS,
			LastTS:         t.LastTS,
			TextDeltas:     t.TextDeltas,
			ToolDeltas:     t.ToolDeltas,
		}
		if n := len(t.ToolCalls); n > 0 {
			last := t.ToolCalls[n-1]
			f.LastToolCall = last.Name
			f.LastToolInput = last.Input
			f.SpawnedTask = last.TaskName
		}
		if n := len(t.Messages); n > 0 {
			f.LastMessage = t.Messages[n-1].Text
		}
		out = append(out, f)
	}
	sortInFlightByStart(out)
	return out
}

// sortInFlightByStart orders oldest first, breaking ties on request id. The tiebreak is
// not cosmetic: Go randomises map iteration, so without a total order a live view would
// reshuffle same-millisecond rows on every single poll.
func sortInFlightByStart(f []InFlight) {
	sort.Slice(f, func(i, j int) bool {
		if !f[i].FirstTS.Equal(f[j].FirstTS) {
			return f[i].FirstTS.Before(f[j].FirstTS)
		}
		return f[i].RequestID < f[j].RequestID
	})
}
