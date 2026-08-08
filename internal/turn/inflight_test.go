package turn

import (
	"testing"
	"time"
)

func TestInFlight_SnapshotsOpenResponses(t *testing.T) {
	r := New()
	start := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	r.open["ws_2"] = &Turn{
		RequestID: "ws_2", ThreadID: "t-child", IsSubagent: true, SubagentKind: "explore",
		FirstTS: start.Add(time.Minute), LastTS: start.Add(2 * time.Minute),
		TextDeltas: 7,
		ToolCalls: []ToolCall{
			{Name: "read", Input: "one"},
			{Name: "spawn_agent", Input: "two", TaskName: "/root/lab"},
		},
		Messages: []Message{{Text: "first"}, {Text: "latest"}},
	}
	r.open["ws_1"] = &Turn{RequestID: "ws_1", ThreadID: "t-parent", FirstTS: start, LastTS: start}

	got := r.InFlight()
	if len(got) != 2 {
		t.Fatalf("got %d in-flight, want 2", len(got))
	}
	// Oldest first, and totally ordered - Go randomises map iteration, so without this
	// the view reshuffles on every poll.
	if got[0].RequestID != "ws_1" || got[1].RequestID != "ws_2" {
		t.Fatalf("order = %s, %s; want ws_1 then ws_2", got[0].RequestID, got[1].RequestID)
	}

	f := got[1]
	if f.LastToolCall != "spawn_agent" || f.LastToolInput != "two" {
		t.Errorf("last tool = %q/%q, want the NEWEST call", f.LastToolCall, f.LastToolInput)
	}
	if f.SpawnedTask != "/root/lab" {
		t.Errorf("spawned task = %q, want /root/lab", f.SpawnedTask)
	}
	if f.LastMessage != "latest" {
		t.Errorf("last message = %q, want the newest", f.LastMessage)
	}
	if !f.IsSubagent || f.SubagentKind != "explore" {
		t.Errorf("subagent fields lost: %+v", f)
	}
}

// A reader keeps hold of these while the poll goroutine keeps appending to the very
// slices they came from. Aliasing one would be a data race on live conversation text.
func TestInFlight_DoesNotAliasReducerState(t *testing.T) {
	r := New()
	open := &Turn{
		RequestID: "ws_1",
		ToolCalls: []ToolCall{{Name: "read", Input: "before"}},
		Messages:  []Message{{Text: "before"}},
	}
	r.open["ws_1"] = open

	got := r.InFlight()
	if len(got) != 1 {
		t.Fatalf("got %d in-flight, want 1", len(got))
	}

	open.ToolCalls = append(open.ToolCalls, ToolCall{Name: "shell", Input: "after"})
	open.ToolCalls[0].Input = "mutated"
	open.Messages[0].Text = "mutated"

	if got[0].LastToolCall != "read" || got[0].LastToolInput != "before" {
		t.Errorf("snapshot followed a later mutation: %+v", got[0])
	}
	if got[0].LastMessage != "before" {
		t.Errorf("snapshot followed a later mutation: %q", got[0].LastMessage)
	}
}

func TestInFlight_EmptyReducer(t *testing.T) {
	if got := New().InFlight(); len(got) != 0 {
		t.Errorf("got %d in-flight from a fresh reducer, want 0", len(got))
	}
}
