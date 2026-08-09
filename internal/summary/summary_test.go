package summary

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rknightion/codexlb2otel/internal/live"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

// fakeClient records every call and answers from a scripted table. No test in this package
// touches the network; the whole point of the Client interface is that none has to.
type fakeClient struct {
	mu    sync.Mutex
	calls []call
	// reply maps a substring of the user prompt to the answer. First match wins.
	reply map[string]string
	// fail maps a substring of the user prompt to an error.
	fail map[string]error
}

type call struct{ system, user string }

func (f *fakeClient) Complete(_ context.Context, system, user string) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, call{system, user})
	f.mu.Unlock()

	for k, err := range f.fail {
		if strings.Contains(user, k) {
			return "", err
		}
	}
	for k, v := range f.reply {
		if strings.Contains(user, k) {
			return v, nil
		}
	}
	return "generic summary", nil
}

func (f *fakeClient) systems() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	for i, c := range f.calls {
		out[i] = c.system
	}
	return out
}

// mapSource satisfies Source from a plain map.
type mapSource map[string][]*turn.Turn

func (m mapSource) Turns(id string) []*turn.Turn { return m[id] }

func msg(text string) []turn.Message { return []turn.Message{{Text: text}} }

func TestRun_RollsSubagentsUpIntoTheirSession(t *testing.T) {
	root := &live.Thread{
		ThreadID: "root", Name: "the session", FirstSeen: at(0), LastSeen: at(30),
		Children: []*live.Thread{
			{ThreadID: "kid-a", Name: "reviewer", TaskPath: "/root/reviewer", FirstSeen: at(5)},
			{ThreadID: "kid-b", Name: "builder", TaskPath: "/root/builder", FirstSeen: at(10)},
		},
	}
	src := mapSource{
		"root":  {{FirstTS: at(0), Messages: msg("ROOT-WORK")}},
		"kid-a": {{FirstTS: at(5), Messages: msg("KID-A-WORK")}},
		"kid-b": {{FirstTS: at(10), Messages: msg("KID-B-WORK")}},
	}
	c := &fakeClient{reply: map[string]string{
		"KID-A-WORK": "reviewed the retention change",
		"KID-B-WORK": "built the picker",
		"ROOT-WORK":  "coordinated the work",
	}}

	rep := Run(context.Background(), c, src, []Session{{Thread: root}}, Options{})

	if len(rep.Sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(rep.Sessions))
	}
	s := rep.Sessions[0]
	if s.Err != nil {
		t.Fatalf("session failed: %v", s.Err)
	}
	if s.Subagents != 2 {
		t.Errorf("Subagents = %d, want 2", s.Subagents)
	}

	// The final session pass must have been handed both subagent summaries AND the root's,
	// or the session summary cannot mention work its children did.
	var final string
	for _, cl := range c.calls {
		if strings.Contains(cl.user, "reviewed the retention change") {
			final = cl.user
		}
	}
	if final == "" {
		t.Fatal("no call carried the subagent summaries; they were dropped")
	}
	for _, want := range []string{"reviewed the retention change", "built the picker", "coordinated the work", "/root/reviewer"} {
		if !strings.Contains(final, want) {
			t.Errorf("final session input is missing %q", want)
		}
	}

	if rep.Window == "" {
		t.Error("no window roll-up was produced")
	}
	if !containsString(c.systems(), windowPrompt) {
		t.Error("the roll-up did not use the window prompt")
	}
	if !containsString(c.systems(), subagentPrompt) {
		t.Error("subagents were not summarised with the subagent prompt")
	}
}

// One session failing must not discard the eleven that succeeded - they have already been
// paid for.
func TestRun_OneFailedSessionDoesNotSinkTheRest(t *testing.T) {
	good := &live.Thread{ThreadID: "good", Name: "good", FirstSeen: at(0), LastSeen: at(5)}
	bad := &live.Thread{ThreadID: "bad", Name: "bad", FirstSeen: at(0), LastSeen: at(5)}
	src := mapSource{
		"good": {{FirstTS: at(0), Messages: msg("GOOD-WORK")}},
		"bad":  {{FirstTS: at(0), Messages: msg("BAD-WORK")}},
	}
	boom := errors.New("upstream exploded")
	c := &fakeClient{
		reply: map[string]string{"GOOD-WORK": "shipped the thing"},
		fail:  map[string]error{"BAD-WORK": boom},
	}

	rep := Run(context.Background(), c, src, []Session{{Thread: good}, {Thread: bad}}, Options{})

	if rep.Sessions[0].Summary != "shipped the thing" {
		t.Errorf("good session summary = %q", rep.Sessions[0].Summary)
	}
	if !errors.Is(rep.Sessions[1].Err, boom) {
		t.Errorf("bad session error = %v, want %v", rep.Sessions[1].Err, boom)
	}
	if rep.Window == "" {
		t.Error("the roll-up should still run over the session that succeeded")
	}
	if rep.Failed() {
		t.Error("Failed() is true, but one session succeeded - this must exit 0")
	}
}

func TestReport_FailedOnlyWhenNothingSurvived(t *testing.T) {
	boom := errors.New("nope")
	cases := []struct {
		name string
		rep  Report
		want bool
	}{
		{"no sessions at all", Report{}, false},
		{"every session failed", Report{Sessions: []Result{{Err: boom}, {Err: boom}}}, true},
		{"one survived", Report{Sessions: []Result{{Err: boom}, {Summary: "ok"}}}, false},
		{"all fine but the roll-up failed", Report{Sessions: []Result{{Summary: "ok"}}, WindowErr: boom}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.rep.Failed(); got != tc.want {
				t.Errorf("Failed() = %v, want %v", got, tc.want)
			}
		})
	}
}

// A subagent that could not be summarised is reported to the parent as unknown rather than
// dropped: silently omitting it makes the session summary claim the work was done by fewer
// agents than it was.
func TestRun_FailedSubagentIsReportedNotDropped(t *testing.T) {
	root := &live.Thread{
		ThreadID: "root", Name: "s", FirstSeen: at(0), LastSeen: at(9),
		Children: []*live.Thread{{ThreadID: "kid", Name: "kid", TaskPath: "/root/kid", FirstSeen: at(1)}},
	}
	src := mapSource{
		"root": {{FirstTS: at(0), Messages: msg("ROOT-WORK")}},
		"kid":  {{FirstTS: at(1), Messages: msg("KID-WORK")}},
	}
	c := &fakeClient{fail: map[string]error{"KID-WORK": errors.New("timed out")}}

	rep := Run(context.Background(), c, src, []Session{{Thread: root}}, Options{})
	if rep.Sessions[0].Err != nil {
		t.Fatalf("the session itself should still succeed, got %v", rep.Sessions[0].Err)
	}

	var final string
	for _, cl := range c.calls {
		if strings.Contains(cl.user, "Sub-agents it spawned") {
			final = cl.user
		}
	}
	if final == "" {
		t.Fatal("the failed subagent was dropped entirely")
	}
	if !strings.Contains(final, "could not be summarised") || !strings.Contains(final, "/root/kid") {
		t.Errorf("the failure was not reported to the parent pass:\n%s", final)
	}
}

func TestRun_ChunkedSessionIsSummarisedInPasses(t *testing.T) {
	th := &live.Thread{ThreadID: "big", Name: "big", FirstSeen: at(0), LastSeen: at(20)}
	var turns []*turn.Turn
	for i := range 6 {
		turns = append(turns, &turn.Turn{FirstTS: at(i), Messages: msg(strings.Repeat("q", 1000))})
	}
	c := &fakeClient{}

	rep := Run(context.Background(), c, mapSource{"big": turns},
		[]Session{{Thread: th}}, Options{MaxCharsPerSession: 2500})

	if rep.Sessions[0].Passes < 2 {
		t.Fatalf("Passes = %d, want the session to have chunked", rep.Sessions[0].Passes)
	}
	if !containsString(c.systems(), chunkPrompt) {
		t.Error("chunks were not summarised with the chunk prompt")
	}
	// The combining pass must see the part summaries, framed as parts of one session.
	var combine string
	for _, cl := range c.calls {
		if strings.Contains(cl.user, "Part 1 of") && strings.Contains(cl.user, "consecutive parts") {
			combine = cl.user
		}
	}
	if combine == "" {
		t.Error("no pass combined the chunk summaries")
	}
}

func TestRun_SessionWithNoContentIsAnError(t *testing.T) {
	th := &live.Thread{ThreadID: "empty", Name: "empty", FirstSeen: at(0), LastSeen: at(1)}
	c := &fakeClient{}

	rep := Run(context.Background(), c, mapSource{"empty": {{FirstTS: at(0)}}}, []Session{{Thread: th}}, Options{})
	if rep.Sessions[0].Err == nil {
		t.Error("a session with no captured content should report why it produced nothing")
	}
	if len(c.calls) != 0 {
		t.Errorf("made %d model calls for an empty session; that is money for nothing", len(c.calls))
	}
}

func TestRun_CarriesTheSessionsRealSpan(t *testing.T) {
	th := &live.Thread{ThreadID: "t", Name: "t", FirstSeen: at(0), LastSeen: at(45), Orphaned: true}
	rep := Run(context.Background(), &fakeClient{}, mapSource{"t": {{FirstTS: at(0), Messages: msg("w")}}},
		[]Session{{Thread: th}}, Options{})

	s := rep.Sessions[0]
	if !s.First.Equal(at(0)) || !s.Last.Equal(at(45)) {
		t.Errorf("span = %s..%s, want %s..%s", s.First, s.Last, at(0), at(45))
	}
	if !s.Orphaned {
		t.Error("Orphaned was not carried through, so the report cannot explain the floating session")
	}
}

func TestDescendants_FlattensTheWholeSubtree(t *testing.T) {
	root := &live.Thread{ThreadID: "r", FirstSeen: at(0), Children: []*live.Thread{
		{ThreadID: "a", FirstSeen: at(2), Children: []*live.Thread{
			{ThreadID: "a1", FirstSeen: at(3)},
		}},
		{ThreadID: "b", FirstSeen: at(1)},
	}}

	got := descendants(root)
	if len(got) != 3 {
		t.Fatalf("got %d descendants, want 3 (nested ones included)", len(got))
	}
	// Oldest first, so the account reads in the order the work happened.
	want := []string{"b", "a", "a1"}
	for i, id := range want {
		if got[i].ThreadID != id {
			t.Errorf("descendant %d = %s, want %s", i, got[i].ThreadID, id)
		}
	}
}

func TestRun_RespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	th := &live.Thread{ThreadID: "t", Name: "t", FirstSeen: at(0), LastSeen: at(1)}
	c := &fakeClient{fail: map[string]error{"w": context.Canceled}}
	rep := Run(ctx, c, mapSource{"t": {{FirstTS: at(0), Messages: msg("w")}}}, []Session{{Thread: th}}, Options{})

	if rep.Sessions[0].Err == nil {
		t.Error("a cancelled run should surface the cancellation, not a summary")
	}
}

func containsString(ss []string, want string) bool { return slices.Contains(ss, want) }

// countingClient records the high-water mark of concurrent calls.
type countingClient struct {
	mu       sync.Mutex
	inFlight int
	peak     int
}

func (c *countingClient) Complete(context.Context, string, string) (string, error) {
	c.mu.Lock()
	c.inFlight++
	if c.inFlight > c.peak {
		c.peak = c.inFlight
	}
	c.mu.Unlock()

	time.Sleep(2 * time.Millisecond)

	c.mu.Lock()
	c.inFlight--
	c.mu.Unlock()
	return "summary", nil
}

// The semaphore is shared across the WHOLE run, not created per session. A per-session
// limit multiplies - 4 sessions at 4 each is 16 in-flight - and a fan-out of 127
// sub-agents would push hard enough to earn the rate limiting the backoff then absorbs.
func TestRun_ConcurrencyIsBoundedAcrossTheWholeRun(t *testing.T) {
	const limit = 2

	src := mapSource{}
	var sessions []Session
	for s := range 4 {
		root := &live.Thread{
			ThreadID:  fmt.Sprintf("root-%d", s),
			Name:      fmt.Sprintf("root-%d", s),
			FirstSeen: at(0), LastSeen: at(10),
		}
		src[root.ThreadID] = []*turn.Turn{{FirstTS: at(0), Messages: msg("root work")}}
		for k := range 5 {
			kid := &live.Thread{ThreadID: fmt.Sprintf("kid-%d-%d", s, k), Name: "kid", FirstSeen: at(k)}
			src[kid.ThreadID] = []*turn.Turn{{FirstTS: at(k), Messages: msg("kid work")}}
			root.Children = append(root.Children, kid)
		}
		sessions = append(sessions, Session{Thread: root})
	}

	c := &countingClient{}
	Run(context.Background(), c, src, sessions, Options{Concurrency: limit})

	if c.peak > limit {
		t.Errorf("peak concurrency was %d, want at most %d - the semaphore is not shared", c.peak, limit)
	}
	if c.peak == 0 {
		t.Fatal("no calls were made at all")
	}
}
