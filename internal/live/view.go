package live

import (
	"time"

	"github.com/rknightion/codexlb2otel/internal/turn"
)

// Snapshot is the whole view at one instant: the thread forest, plus every response
// currently open.
type Snapshot struct {
	At time.Time `json:"at"`
	// Content reports whether bodies are being served, so the UI can say "content
	// disabled" rather than render a conversation that looks mysteriously empty.
	Content bool `json:"content"`
	// StallAfterMs is the configured stall threshold, echoed so the UI can explain what
	// "stalled" means rather than asserting it.
	StallAfterMs int64     `json:"stall_after_ms,omitempty"`
	Roots        []*Thread `json:"roots"`
	Running      []Running `json:"running"`
}

// Thread is one conversation - a parent, or a subagent spawned by one.
type Thread struct {
	ThreadID       string `json:"thread_id"`
	ParentThreadID string `json:"parent_thread_id,omitempty"`
	ParentTurnID   string `json:"parent_turn_id,omitempty"`
	IsSubagent     bool   `json:"is_subagent"`
	SubagentKind   string `json:"subagent_kind,omitempty"`
	// Orphaned marks a subagent whose parent has aged out of the retention window. It
	// is shown as a root, with the flag so the UI can say why it is floating rather
	// than misrepresent it as a top-level conversation.
	Orphaned bool `json:"orphaned,omitempty"`

	// ForkedFrom is set when this thread is a FORK of another rather than a fresh
	// agent. Worth surfacing on its own: a fork inherits the parent's entire message
	// history, so everything it says about its own past is really the parent's, and a
	// reader comparing two forks is looking at two continuations of one conversation.
	ForkedFrom string `json:"forked_from_thread_id,omitempty"`

	Model  string `json:"model,omitempty"`
	Effort string `json:"reasoning_effort,omitempty"`

	// TaskPath and TaskName are the collaboration-layer identity, e.g.
	// "/root/final_integration_review_v2" and "final_integration_review_v2". The only
	// human-meaningful name a subagent has; see live.TaskPath for how it is recovered
	// and why the obvious route does not work.
	TaskPath string `json:"task_path,omitempty"`
	TaskName string `json:"task_name,omitempty"`

	// Latest is the agent's own newest message - the primary line of a row.
	Latest string `json:"latest_message,omitempty"`
	// Ask is what a human asked for, scaffolding filtered out. Frequently empty on a
	// subagent, which never receives one directly.
	Ask string `json:"ask,omitempty"`
	// Activity is the mechanical state - the newest tool call, or thinking/writing.
	// Refreshed from in-flight data where there is any, so it moves between turns.
	Activity string `json:"activity,omitempty"`
	// Spawned lists task names this thread dispatched. Attributed to the thread that
	// MADE the calls, never to the threads they created - that join does not exist.
	Spawned []string `json:"spawned,omitempty"`

	// Name is the resolved row label, computed at snapshot time so the UI never has to
	// reimplement the fallback order.
	Name string `json:"name"`

	Turns   int `json:"turns"`
	Running int `json:"running"`
	// Stalled counts this thread's open responses that have gone quiet past the
	// threshold - the wedged case, which otherwise reads identically to a busy one
	// because both display "thinking" forever.
	Stalled int `json:"stalled"`
	Errors  int `json:"errors"`

	InputTokens     int `json:"input_tokens"`
	OutputTokens    int `json:"output_tokens"`
	ReasoningTokens int `json:"reasoning_tokens"`
	TotalTokens     int `json:"total_tokens"`

	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`

	Children []*Thread `json:"children,omitempty"`
}

// observe folds one completed turn into the thread's aggregate. Turns arrive in archive
// order, so "last non-empty wins" is genuinely the newest value.
func (th *Thread) observe(t *turn.Turn, content bool) {
	th.Turns++
	if t.FirstTS.Before(th.FirstSeen) || th.FirstSeen.IsZero() {
		th.FirstSeen = t.FirstTS
	}
	if t.LastTS.After(th.LastSeen) {
		th.LastSeen = t.LastTS
	}

	if t.IsSubagent {
		th.IsSubagent = true
	}
	setIfNotEmpty(&th.SubagentKind, t.SubagentKind)
	setIfNotEmpty(&th.ParentThreadID, t.ParentThreadID)
	setIfNotEmpty(&th.ParentTurnID, t.ParentTurnID)
	setIfNotEmpty(&th.ForkedFrom, t.ForkedFromThreadID)
	setIfNotEmpty(&th.Model, t.Model)
	setIfNotEmpty(&th.Effort, t.Effort)

	if t.Status == turn.StatusError {
		th.Errors++
	}

	th.InputTokens += t.InputTokens
	th.OutputTokens += t.OutputTokens
	th.ReasoningTokens += t.ReasoningTokens
	th.TotalTokens += t.TotalTokens

	// Only a root thread can have been asked anything. A fork inherits the parent's
	// ENTIRE prompt history, so a user-role message found on one was addressed to the
	// parent - deriving an ask from it stamped the same human sentence onto every
	// subagent on screen, each claiming to have been told something it never was. A
	// subagent's real instruction is the spawn message, which is encrypted.
	if !t.IsSubagent {
		setIfNotEmpty(&th.Ask, askOf(t, content))
	}
	setIfNotEmpty(&th.Latest, latestOf(t, content))
	setIfNotEmpty(&th.Activity, activityOf(t, content))
}

// name resolves the label a row leads with, best first. Task name is the only genuinely
// identifying one; the rest are honest fallbacks that say what little is known rather
// than leaving a blank row.
func (th *Thread) name() string {
	switch {
	case th.TaskName != "":
		return th.TaskName
	case th.Ask != "":
		return th.Ask
	case th.IsSubagent && th.ForkedFrom != "":
		return "fork"
	case th.IsSubagent:
		return "subagent"
	default:
		return "session"
	}
}

func setIfNotEmpty(dst *string, v string) {
	if v != "" {
		*dst = v
	}
}

// Running is one response the reducer has open - work in progress.
type Running struct {
	RequestID    string `json:"request_id"`
	ThreadID     string `json:"thread_id,omitempty"`
	ParentTurnID string `json:"parent_turn_id,omitempty"`
	IsSubagent   bool   `json:"is_subagent"`
	SubagentKind string `json:"subagent_kind,omitempty"`
	Model        string `json:"model,omitempty"`
	RequestKind  string `json:"request_kind,omitempty"`
	Activity     string `json:"activity,omitempty"`
	// ElapsedMs is measured from the response's first frame to its latest, which is the
	// ARCHIVE's own clock. Deliberately not wall-clock-minus-start: the tail may be
	// catching up on a backlog, and a row claiming a response has run for six hours
	// because the file is old would be worse than useless.
	ElapsedMs int64 `json:"elapsed_ms"`
	// QuietMs is how long since this response last produced a frame, measured on the
	// archive's clock, and Stalled is that exceeding the threshold.
	QuietMs    int64     `json:"quiet_ms"`
	Stalled    bool      `json:"stalled,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	LastFrame  time.Time `json:"last_frame"`
	TextDeltas int       `json:"text_deltas"`
	ToolDeltas int       `json:"tool_input_deltas"`
}

func newRunning(f turn.InFlight, content bool) Running {
	return Running{
		RequestID:    f.RequestID,
		ThreadID:     f.ThreadID,
		ParentTurnID: f.ParentTurnID,
		IsSubagent:   f.IsSubagent,
		SubagentKind: f.SubagentKind,
		Model:        f.Model,
		RequestKind:  f.RequestKind,
		Activity:     activityOfInFlight(f, content),
		ElapsedMs:    f.LastTS.Sub(f.FirstTS).Milliseconds(),
		StartedAt:    f.FirstTS,
		LastFrame:    f.LastTS,
		TextDeltas:   f.TextDeltas,
		ToolDeltas:   f.ToolDeltas,
	}
}

// redact returns a copy of t with every content-bearing field emptied, for a store
// configured with Content false.
//
// It copies and clears rather than building a whitelist of safe fields: a whitelist
// silently omits nothing when a new field is added to Turn, whereas this at worst
// carries a new field it should have cleared - and turn.Turn grows content fields
// regularly. The counts (Chars, InputChars) are kept deliberately: "the agent printed
// 40 KB" is operationally useful and is not content.
func redact(t *turn.Turn) *turn.Turn {
	c := *t
	c.Prompts = nil
	c.Messages = nil
	c.ToolOutputs = nil
	c.AgentMessages = nil
	c.ErrorMessage = ""

	c.ToolCalls = make([]turn.ToolCall, len(t.ToolCalls))
	for i, tc := range t.ToolCalls {
		// Tool NAMES are bounded and structural - they are the whole point of the view -
		// but a call's arguments are arbitrary text the model wrote.
		tc.Input = ""
		c.ToolCalls[i] = tc
	}
	return &c
}
