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

// A row must never be blank. With no task path and no ask, the name says what little is
// known - and "fork" is meaningfully different from "subagent" to a reader.
func TestSnapshot_NameFallsBackWhenNothingIdentifiesTheThread(t *testing.T) {
	s := newTestStore(t, Options{Content: false})
	child := completed("thread-child", "turn-2", 2)
	child.IsSubagent = true
	child.ForkedFromThreadID = "thread-parent"
	child.Prompts = []turn.Prompt{{Role: "user", Text: "withheld"}}
	emit(t, s, child)

	snap := s.Snapshot()
	if len(snap.Roots) != 1 {
		t.Fatalf("got %d roots, want 1", len(snap.Roots))
	}
	if got := snap.Roots[0].Name; got != "fork" {
		t.Errorf("name = %q, want %q", got, "fork")
	}
}

// The task path wins over every fallback: it is the only genuinely identifying name a
// subagent has.
func TestSnapshot_NameUsesTheTaskPathWhenThereIsOne(t *testing.T) {
	s := newTestStore(t, Options{Content: true})
	child := completed("thread-child", "turn-2", 2)
	child.IsSubagent = true
	child.AgentMessages = []turn.AgentMessage{
		{Author: "/root", Recipient: "/root/final_integration_review_v2"},
	}
	emit(t, s, child)

	snap := s.Snapshot()
	if got := snap.Roots[0].Name; got != "final_integration_review_v2" {
		t.Errorf("name = %q, want the task name", got)
	}
	if got := snap.Roots[0].TaskPath; got != "/root/final_integration_review_v2" {
		t.Errorf("task path = %q", got)
	}
}

// REGRESSION, #36. parent_turn_id is absent on every fork subagent in the corpus, and
// where it does appear on a later turn it names a turn of the ROOT - so preferring it
// reparented every descendant onto the root and collapsed a genuine four-level tree to
// one level. parent_thread_id must win.
func TestSnapshot_DeepChainIsNotFlattenedByAParentTurnPointingAtTheRoot(t *testing.T) {
	s := newTestStore(t, Options{Content: true})

	root := completed("root", "root-turn", 1)

	mid := completed("mid", "mid-turn", 2)
	mid.IsSubagent = true
	mid.ParentThreadID = "root"
	mid.ParentTurnID = "root-turn"

	leaf := completed("leaf", "leaf-turn", 3)
	leaf.IsSubagent = true
	leaf.ParentThreadID = "mid"
	// The trap: the leaf's parent TURN belongs to the root, two levels up.
	leaf.ParentTurnID = "root-turn"

	emit(t, s, root, mid, leaf)

	snap := s.Snapshot()
	if len(snap.Roots) != 1 {
		t.Fatalf("got %d roots, want 1: %+v", len(snap.Roots), snap.Roots)
	}
	kids := snap.Roots[0].Children
	if len(kids) != 1 || kids[0].ThreadID != "mid" {
		t.Fatalf("root children = %+v, want just mid", kids)
	}
	if len(kids[0].Children) != 1 || kids[0].Children[0].ThreadID != "leaf" {
		t.Fatalf("leaf did not nest under mid; the tree was flattened: %+v", kids[0].Children)
	}
}

// The plain age check evicted, with priority, the single most useful thing on the page:
// a thread whose response is still open has by definition not completed a turn recently,
// so its newest COMPLETED turn is exactly what goes stale while it hangs.
func TestSnapshot_AThreadWithAnOpenResponseSurvivesTheIdleWindow(t *testing.T) {
	s := New(Options{
		RetainWindow: 10 * time.Minute,
		Content:      true,
		Now:          func() time.Time { return ts(40) },
	})
	emit(t, s, completed("wedged", "turn-1", 1), completed("recent", "turn-2", 35))
	s.SetInFlightSource(func() []turn.InFlight {
		return []turn.InFlight{{RequestID: "ws_open", ThreadID: "wedged", FirstTS: ts(1), LastTS: ts(1)}}
	})
	// Eviction happens in rebuild, so it must be re-run now the source is wired.
	emit(t, s)

	ids := map[string]bool{}
	for _, r := range s.Snapshot().Roots {
		ids[r.ThreadID] = true
	}
	if !ids["wedged"] {
		t.Error("the thread with an open response was evicted for being idle; that is the wedged agent")
	}
	if !ids["recent"] {
		t.Error("the recent thread was evicted")
	}
}

// Eviction is per thread but the view is a tree: dropping a parent does not drop its
// children, it re-roots them, so the run appears to GAIN top-level agents as it ages.
func TestSnapshot_AnAncestorOfAKeptThreadIsNotEvicted(t *testing.T) {
	s := New(Options{
		RetainWindow: 10 * time.Minute,
		Content:      true,
		Now:          func() time.Time { return ts(40) },
	})
	oldParent := completed("parent", "p-turn", 1)
	child := completed("child", "c-turn", 35)
	child.IsSubagent = true
	child.ParentThreadID = "parent"
	emit(t, s, oldParent, child)

	snap := s.Snapshot()
	if len(snap.Roots) != 1 || snap.Roots[0].ThreadID != "parent" {
		t.Fatalf("roots = %+v, want the stale parent kept so the child stays nested", snap.Roots)
	}
	if len(snap.Roots[0].Children) != 1 {
		t.Fatalf("child was re-rooted: %+v", snap.Roots[0])
	}
}

func TestSnapshot_StallIsMeasuredOnTheArchiveClock(t *testing.T) {
	s := New(Options{
		Content:    true,
		StallAfter: 5 * time.Minute,
		// Generous, so this test is about stalls and not about eviction.
		RetainWindow: 72 * time.Hour,
		// Wall clock is hours ahead of the fixture data. If stall used it, EVERYTHING
		// would be stalled - which is what happens whenever ingestion falls behind.
		Now: func() time.Time { return ts(30).Add(48 * time.Hour) },
	})
	emit(t, s, completed("busy", "turn-1", 20))
	s.SetInFlightSource(func() []turn.InFlight {
		return []turn.InFlight{
			{RequestID: "fresh", ThreadID: "busy", FirstTS: ts(18), LastTS: ts(20)},
			{RequestID: "wedged", ThreadID: "busy", FirstTS: ts(1), LastTS: ts(2)},
		}
	})

	snap := s.Snapshot()
	got := map[string]bool{}
	for _, r := range snap.Running {
		got[r.RequestID] = r.Stalled
	}
	if got["fresh"] {
		t.Error("a response producing frames at the newest archive timestamp was called stalled")
	}
	if !got["wedged"] {
		t.Error("a response quiet for 18 archive-minutes was not called stalled")
	}
	if snap.Roots[0].Stalled != 1 {
		t.Errorf("thread reports %d stalled, want 1", snap.Roots[0].Stalled)
	}
}

func TestSnapshot_NothingIsStalledWhenTheWholeArchiveIsQuiet(t *testing.T) {
	// Everything quiet is an INGESTION outage, not a fleet of wedged agents. /healthz
	// answers for that; a page full of false stall badges would not.
	s := New(Options{
		Content:      true,
		StallAfter:   5 * time.Minute,
		RetainWindow: 72 * time.Hour,
		Now:          func() time.Time { return ts(30).Add(48 * time.Hour) },
	})
	emit(t, s, completed("a", "turn-1", 1))
	s.SetInFlightSource(func() []turn.InFlight {
		return []turn.InFlight{{RequestID: "x", ThreadID: "a", FirstTS: ts(1), LastTS: ts(1)}}
	})

	for _, r := range s.Snapshot().Running {
		if r.Stalled {
			t.Errorf("%s was called stalled when nothing in the archive is newer", r.RequestID)
		}
	}
}

// A fork inherits the parent's entire prompt history, so a user-role message on one was
// addressed to the parent. Deriving an ask from it stamped the same human sentence onto
// every subagent on screen.
func TestSnapshot_AForkDoesNotClaimTheParentsAsk(t *testing.T) {
	s := newTestStore(t, Options{Content: true})
	root := completed("root", "r-turn", 1)
	root.Prompts = []turn.Prompt{{Role: "user", Text: "those defaults look good to me"}}

	child := completed("child", "c-turn", 2)
	child.IsSubagent = true
	child.ParentThreadID = "root"
	child.ForkedFromThreadID = "root"
	// The inherited history, verbatim.
	child.Prompts = []turn.Prompt{{Role: "user", Text: "those defaults look good to me"}}
	emit(t, s, root, child)

	snap := s.Snapshot()
	if snap.Roots[0].Ask == "" {
		t.Error("the root lost its own ask")
	}
	if got := snap.Roots[0].Children[0].Ask; got != "" {
		t.Errorf("fork claims ask %q, which was addressed to its parent", got)
	}
}
