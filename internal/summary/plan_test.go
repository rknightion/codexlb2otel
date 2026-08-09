package summary

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/rknightion/codexlb2otel/internal/live"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

// The estimate is the number an operator decides on: told "about 8 calls" they wait, told
// nothing they interrupt a working run. So it is worth pinning that it matches what Run
// actually spends - counted here by running the same input through a fake client.
func TestEstimate_MatchesTheCallsRunActuallyMakes(t *testing.T) {
	root := &live.Thread{
		ThreadID: "root", Name: "the session",
		Children: []*live.Thread{
			{ThreadID: "kid-a", Name: "reviewer", TaskPath: "/root/reviewer"},
			{ThreadID: "kid-b", Name: "builder"},
		},
	}
	src := mapSource{
		"root":  {{Messages: msg("ROOT-WORK")}},
		"kid-a": {{Messages: msg("KID-A-WORK")}},
		"kid-b": {{Messages: msg("KID-B-WORK")}},
	}
	sessions := []Session{{Thread: root}}

	plan := Estimate(src, sessions, Options{})

	c := &fakeClient{}
	Run(context.Background(), c, src, sessions, Options{})

	c.mu.Lock()
	spent := len(c.calls)
	c.mu.Unlock()

	if plan.Calls != spent {
		t.Fatalf("estimated %d calls, Run made %d", plan.Calls, spent)
	}
	// Two sub-agents, one root pass, one fold, one roll-up.
	if plan.Calls != 5 {
		t.Fatalf("calls = %d, want 5", plan.Calls)
	}
}

// A chunked digest costs a call per chunk PLUS a combining call, and that surcharge is
// exactly the part an estimate is likely to drop.
func TestEstimate_ChargesForTheCombiningCallWhenADigestChunks(t *testing.T) {
	root := &live.Thread{ThreadID: "root", Name: "big"}
	src := mapSource{"root": {
		{Messages: msg(strings.Repeat("a", 400))},
		{Messages: msg(strings.Repeat("b", 400))},
		{Messages: msg(strings.Repeat("c", 400))},
	}}
	sessions := []Session{{Thread: root}}

	// A budget small enough to force one turn per chunk.
	o := Options{MaxCharsPerSession: 500}
	plan := Estimate(src, sessions, o)

	if plan.Sessions[0].Passes < 2 {
		t.Fatalf("expected the digest to chunk, got %d pass(es)", plan.Sessions[0].Passes)
	}

	c := &fakeClient{}
	Run(context.Background(), c, src, sessions, o)
	c.mu.Lock()
	spent := len(c.calls)
	c.mu.Unlock()

	if plan.Calls != spent {
		t.Fatalf("estimated %d calls, Run made %d", plan.Calls, spent)
	}
}

// Sub-agents are named the way the summariser names them, so -dry-run and the report agree
// on what a row refers to.
func TestEstimate_PrefersTheTaskPathForSubagentNames(t *testing.T) {
	root := &live.Thread{
		ThreadID: "root", Name: "the session",
		Children: []*live.Thread{
			{ThreadID: "kid-a", Name: "first line of a prompt", TaskPath: "maintenance_wiring"},
			{ThreadID: "kid-b", Name: "no task path here"},
		},
	}
	src := mapSource{
		"root":  {{Messages: msg("ROOT")}},
		"kid-a": {{Messages: msg("A")}},
		"kid-b": {{Messages: msg("B")}},
	}

	plan := Estimate(src, []Session{{Thread: root}}, Options{})

	got := []string{plan.Sessions[0].Subagents[0].Name, plan.Sessions[0].Subagents[1].Name}
	want := []string{"maintenance_wiring", "no task path here"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("subagent %d name = %q, want %q", i, got[i], want[i])
		}
	}
}

// A session with no captured content costs nothing, and an estimate that charged for it
// would overstate the wait on exactly the windows that are cheapest.
func TestEstimate_ChargesNothingForAnEmptyThread(t *testing.T) {
	root := &live.Thread{ThreadID: "root", Name: "empty"}
	plan := Estimate(mapSource{"root": []*turn.Turn{}}, []Session{{Thread: root}}, Options{})

	// The roll-up alone.
	if plan.Calls != 1 {
		t.Fatalf("calls = %d, want 1 (the roll-up)", plan.Calls)
	}
	if plan.Chars != 0 {
		t.Fatalf("chars = %d, want 0", plan.Chars)
	}
}

// Progress exists so a long run does not look hung. The count has to reach the caller, and
// a session has to be announced by name when it lands.
func TestRun_ReportsProgressAsWorkCompletes(t *testing.T) {
	root := &live.Thread{
		ThreadID: "root", Name: "the session",
		Children: []*live.Thread{{ThreadID: "kid", Name: "reviewer"}},
	}
	src := mapSource{
		"root": {{Messages: msg("ROOT")}},
		"kid":  {{Messages: msg("KID")}},
	}

	var mu sync.Mutex
	var counts []int
	var finished []string
	o := Options{Progress: func(ev Event) {
		mu.Lock()
		defer mu.Unlock()
		if ev.Session != "" {
			finished = append(finished, ev.Session)
			return
		}
		counts = append(counts, ev.Calls)
	}}

	Run(context.Background(), &fakeClient{}, src, []Session{{Thread: root}}, o)

	mu.Lock()
	defer mu.Unlock()
	if len(finished) != 1 || finished[0] != "the session" {
		t.Fatalf("finished sessions = %v, want [the session]", finished)
	}
	// One per sub-agent, root, fold and roll-up. The counter must be strictly increasing
	// and end at the total, or a progress line would go backwards.
	if len(counts) != 4 {
		t.Fatalf("call events = %d, want 4", len(counts))
	}
	for i, n := range counts {
		if n != i+1 {
			t.Fatalf("call events = %v, want 1..%d in order", counts, len(counts))
		}
	}
}
