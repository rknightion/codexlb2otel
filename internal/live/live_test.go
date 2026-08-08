package live

import (
	"context"
	"testing"
	"time"

	"github.com/rknightion/codexlb2otel/internal/turn"
)

func ts(min int) time.Time {
	return time.Date(2026, 8, 8, 12, min, 0, 0, time.UTC)
}

func completed(threadID, turnID string, min int) *turn.Turn {
	return &turn.Turn{
		RequestID: "ws_" + turnID, ThreadID: threadID, TurnID: turnID,
		Model: "gpt-5.6-sol", Status: "completed", Family: "websocket", RequestKind: "turn",
		FirstTS: ts(min), LastTS: ts(min),
	}
}

func newTestStore(t *testing.T, opts Options) *Store {
	t.Helper()
	if opts.Now == nil {
		// Pinned well after the fixture timestamps so nothing is evicted by the window
		// unless a test is deliberately testing eviction.
		opts.Now = func() time.Time { return ts(30) }
	}
	return New(opts)
}

func emit(t *testing.T, s *Store, turns ...*turn.Turn) {
	t.Helper()
	if err := s.Emit(context.Background(), turns); err != nil {
		t.Fatalf("Emit: %v", err)
	}
}

// The whole point of the view: a subagent hangs off the turn that spawned it, not off
// the top level.
func TestSnapshot_SubagentNestsUnderTheSpawningTurn(t *testing.T) {
	s := newTestStore(t, Options{Content: true})

	parent := completed("thread-parent", "turn-1", 1)
	child := completed("thread-child", "turn-2", 2)
	child.IsSubagent = true
	child.SubagentKind = "explore"
	child.ParentTurnID = "turn-1"
	emit(t, s, parent, child)

	snap := s.Snapshot()
	if len(snap.Roots) != 1 {
		t.Fatalf("got %d roots, want 1 (the subagent should be nested)", len(snap.Roots))
	}
	root := snap.Roots[0]
	if root.ThreadID != "thread-parent" {
		t.Errorf("root is %q, want thread-parent", root.ThreadID)
	}
	if len(root.Children) != 1 || root.Children[0].ThreadID != "thread-child" {
		t.Fatalf("children = %+v, want the subagent thread", root.Children)
	}
	if !root.Children[0].IsSubagent {
		t.Error("nested child is not flagged as a subagent")
	}
}

// ParentTurnID is preferred, but a turn whose parent turn was never captured must still
// find its parent thread rather than floating to the top level.
func TestSnapshot_FallsBackToParentThreadWhenTheSpawningTurnIsUnknown(t *testing.T) {
	s := newTestStore(t, Options{Content: true})

	parent := completed("thread-parent", "turn-1", 1)
	child := completed("thread-child", "turn-2", 2)
	child.IsSubagent = true
	child.ParentTurnID = "turn-never-captured"
	child.ParentThreadID = "thread-parent"
	emit(t, s, parent, child)

	snap := s.Snapshot()
	if len(snap.Roots) != 1 {
		t.Fatalf("got %d roots, want 1", len(snap.Roots))
	}
	if len(snap.Roots[0].Children) != 1 {
		t.Fatalf("subagent did not fall back to its parent thread: %+v", snap.Roots[0])
	}
}

// A subagent whose parent has aged out is still live activity. Hiding it is the one
// failure a "what is happening right now" view cannot afford, so it is promoted to a
// root and flagged.
func TestSnapshot_OrphanedSubagentIsPromotedNotDropped(t *testing.T) {
	s := newTestStore(t, Options{Content: true})

	child := completed("thread-child", "turn-2", 2)
	child.IsSubagent = true
	child.ParentThreadID = "thread-long-gone"
	emit(t, s, child)

	snap := s.Snapshot()
	if len(snap.Roots) != 1 {
		t.Fatalf("got %d roots, want the orphan promoted", len(snap.Roots))
	}
	if !snap.Roots[0].Orphaned {
		t.Error("orphan is not flagged; the UI would show it as a top-level conversation")
	}
}

func TestSnapshot_RingIsBoundedAndThreadsAggregate(t *testing.T) {
	s := newTestStore(t, Options{RetainTurns: 3, Content: true})

	for i := 1; i <= 5; i++ {
		tn := completed("thread-a", "turn-"+string(rune('0'+i)), i)
		tn.TotalTokens = 100
		emit(t, s, tn)
	}

	if got := len(s.turns); got != 3 {
		t.Errorf("ring holds %d turns, want 3", got)
	}
	// The backing array must not still reference the evicted turns; those are the objects
	// carrying conversation text.
	backing := s.turns[:cap(s.turns)]
	for i := len(s.turns); i < len(backing); i++ {
		if backing[i] != nil {
			t.Errorf("backing array slot %d still references an evicted turn", i)
		}
	}

	snap := s.Snapshot()
	if len(snap.Roots) != 1 {
		t.Fatalf("got %d roots, want 1", len(snap.Roots))
	}
	// Aggregates are rebuilt from the ring, so they describe what is retained - not a
	// running total that would drift from its own source once eviction started.
	if got := snap.Roots[0].Turns; got != 3 {
		t.Errorf("thread reports %d turns, want 3 (the retained ones)", got)
	}
	if got := snap.Roots[0].TotalTokens; got != 300 {
		t.Errorf("thread reports %d tokens, want 300", got)
	}
}

func TestSnapshot_ThreadsIdleBeyondTheWindowAreDropped(t *testing.T) {
	s := New(Options{
		RetainWindow: 10 * time.Minute,
		Content:      true,
		Now:          func() time.Time { return ts(40) },
	})
	emit(t, s, completed("stale", "turn-1", 1), completed("recent", "turn-2", 35))

	snap := s.Snapshot()
	if len(snap.Roots) != 1 || snap.Roots[0].ThreadID != "recent" {
		t.Fatalf("roots = %+v, want only the recent thread", snap.Roots)
	}
}

func TestEmit_ExcludesProbeTrafficByDefault(t *testing.T) {
	s := newTestStore(t, Options{Content: true})
	probe := completed("thread-probe", "turn-1", 1)
	probe.Family = "probe"
	emit(t, s, probe, completed("thread-real", "turn-2", 2))

	snap := s.Snapshot()
	if len(snap.Roots) != 1 || snap.Roots[0].ThreadID != "thread-real" {
		t.Fatalf("roots = %+v, want probe traffic excluded", snap.Roots)
	}
}

func TestSnapshot_RunningRowsComeFromTheInFlightSource(t *testing.T) {
	s := newTestStore(t, Options{Content: true})
	emit(t, s, completed("thread-a", "turn-1", 1))
	s.SetInFlightSource(func() []turn.InFlight {
		return []turn.InFlight{{
			RequestID: "ws_open", ThreadID: "thread-a",
			FirstTS: ts(2), LastTS: ts(3), LastToolCall: "shell",
		}}
	})

	snap := s.Snapshot()
	if len(snap.Running) != 1 {
		t.Fatalf("got %d running rows, want 1", len(snap.Running))
	}
	if got := snap.Running[0].ElapsedMs; got != 60_000 {
		t.Errorf("elapsed = %dms, want 60000 (archive clock, not wall clock)", got)
	}
	if snap.Roots[0].Running != 1 {
		t.Error("the running response was not counted against its thread")
	}
	if snap.Roots[0].Activity != "shell" {
		t.Errorf("thread activity = %q, want the in-flight tool call", snap.Roots[0].Activity)
	}
}

func TestTurns_RedactsContentWhenDisabled(t *testing.T) {
	s := newTestStore(t, Options{Content: false})
	tn := completed("thread-a", "turn-1", 1)
	tn.Prompts = []turn.Prompt{{Role: "user", Text: "the secret plan", Chars: 15}}
	tn.Messages = []turn.Message{{Text: "here it is", Chars: 10}}
	tn.ToolOutputs = []turn.ToolOutput{{Text: "root:x:0:0", Chars: 10}}
	tn.ToolCalls = []turn.ToolCall{{Name: "shell", Input: "cat /etc/passwd", InputChars: 15}}
	tn.ErrorMessage = "failed for resp_abc"
	emit(t, s, tn)

	got := s.Turns("thread-a")
	if len(got) != 1 {
		t.Fatalf("got %d turns, want 1", len(got))
	}
	g := got[0]
	if g.Prompts != nil || g.Messages != nil || g.ToolOutputs != nil || g.ErrorMessage != "" {
		t.Errorf("content survived redaction: %+v", g)
	}
	if len(g.ToolCalls) != 1 || g.ToolCalls[0].Input != "" {
		t.Errorf("tool call input survived redaction: %+v", g.ToolCalls)
	}
	// Structure and counts are the point of a content-free view; losing them would make
	// it useless rather than merely safe.
	if g.ToolCalls[0].Name != "shell" || g.ToolCalls[0].InputChars != 15 {
		t.Errorf("redaction destroyed structural fields: %+v", g.ToolCalls[0])
	}

	// The stored turn must be untouched - redact works on a copy, or disabling content
	// in the view would silently strip the turn every OTHER sink still holds a pointer
	// to.
	if tn.Prompts == nil {
		t.Fatal("redact mutated the caller's turn; other sinks share that pointer")
	}
}

// Emit runs inside tail.Watcher.Poll under its write lock, and its return value gates
// the ingestion checkpoint. A subscriber that stops reading must therefore be dropped
// on the floor, never waited for - otherwise one stalled browser stalls the pipeline
// this whole service exists to run.
func TestEmit_DoesNotBlockOnAStalledSubscriber(t *testing.T) {
	s := newTestStore(t, Options{Content: true})

	ch, cancel := s.Subscribe()
	defer cancel()
	_ = ch // deliberately never drained

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Far more than the queue depth, so this deadlocks rather than merely lags if the
		// send is ever allowed to block.
		for range subQueue * 20 {
			emit(t, s, completed("thread-a", "turn-1", 1))
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Emit blocked on a subscriber that stopped reading; this would stall archive ingestion")
	}
}

func TestSubscribe_PrimesWithTheCurrentSnapshot(t *testing.T) {
	s := newTestStore(t, Options{Content: true})
	emit(t, s, completed("thread-a", "turn-1", 1))

	ch, cancel := s.Subscribe()
	defer cancel()

	select {
	case snap := <-ch:
		if len(snap.Roots) != 1 {
			t.Errorf("primed snapshot has %d roots, want 1", len(snap.Roots))
		}
	default:
		t.Fatal("a new subscriber got nothing; the page would be blank until the next poll")
	}
}

func TestCancel_IsIdempotent(t *testing.T) {
	s := newTestStore(t, Options{Content: true})
	_, cancel := s.Subscribe()
	cancel()
	cancel() // a double close would panic
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// A subagent with no usable prompt still needs a label rather than a blank row - and its
// kind is structural, so it survives content being disabled.
func TestSnapshot_SubagentFallsBackToItsKindForAHeadline(t *testing.T) {
	s := newTestStore(t, Options{Content: false})
	child := completed("thread-child", "turn-2", 2)
	child.IsSubagent = true
	child.SubagentKind = "explore"
	child.Prompts = []turn.Prompt{{Role: "user", Text: "withheld"}}
	emit(t, s, child)

	snap := s.Snapshot()
	if len(snap.Roots) != 1 {
		t.Fatalf("got %d roots, want 1", len(snap.Roots))
	}
	if got := snap.Roots[0].Headline; got != "explore subagent" {
		t.Errorf("headline = %q, want %q", got, "explore subagent")
	}
}
