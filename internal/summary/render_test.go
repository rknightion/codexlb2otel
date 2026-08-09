package summary

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func render(t *testing.T, r Report) string {
	t.Helper()
	var b strings.Builder
	if err := Render(&b, r); err != nil {
		t.Fatalf("Render: %v", err)
	}
	return b.String()
}

func TestRender_EmptyWindowSaysSo(t *testing.T) {
	got := render(t, Report{From: at(0), To: at(60)})
	if !strings.Contains(got, "No sessions ran in this window") {
		t.Errorf("an empty window should say so plainly:\n%s", got)
	}
}

// A report that silently omits three of eleven sessions reads as a complete account of a
// quiet day, which is the most damaging thing this tool could produce.
func TestRender_FailuresAreVisible(t *testing.T) {
	got := render(t, Report{
		From: at(0), To: at(60),
		Window: "shipped the retention change",
		Sessions: []Result{
			{Name: "good", First: at(0), Last: at(20), Summary: "did the work"},
			{Name: "bad", First: at(30), Last: at(40), Err: errors.New("upstream exploded")},
		},
	})

	for _, want := range []string{
		"2 sessions in this window",
		"1 of which could not be summarised",
		"### bad",
		"Could not be summarised: upstream exploded",
		"the 1 session that summarised successfully",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report is missing %q:\n%s", want, got)
		}
	}
}

func TestRender_RollupFailureIsReported(t *testing.T) {
	got := render(t, Report{
		From: at(0), To: at(60),
		WindowErr: errors.New("no credit"),
		Sessions:  []Result{{Name: "s", First: at(0), Last: at(5), Summary: "did a thing"}},
	})
	if !strings.Contains(got, "The roll-up could not be produced: no credit") {
		t.Errorf("a failed roll-up must be reported, not left as a missing section:\n%s", got)
	}
	if !strings.Contains(got, "did a thing") {
		t.Error("the per-session summaries must survive a failed roll-up")
	}
}

// A thin summary of a long session should not read as a session that did little.
func TestRender_ChunkingIsDisclosed(t *testing.T) {
	got := render(t, Report{
		From: at(0), To: at(60),
		Sessions: []Result{{Name: "long", First: at(0), Last: at(50), Summary: "lots happened", Passes: 3}},
	})
	if !strings.Contains(got, "summarised in 3 passes") {
		t.Errorf("chunking was not disclosed:\n%s", got)
	}
}

func TestRender_OrphanIsExplained(t *testing.T) {
	got := render(t, Report{
		From: at(0), To: at(60),
		Sessions: []Result{{Name: "floating", First: at(0), Last: at(5), Summary: "x", Orphaned: true}},
	})
	if !strings.Contains(got, "parent was not captured") {
		t.Errorf("an orphaned subagent must be explained, not shown as a top-level session:\n%s", got)
	}
}

func TestRender_SubagentCountIsShown(t *testing.T) {
	got := render(t, Report{
		From: at(0), To: at(60),
		Sessions: []Result{{Name: "fanout", First: at(0), Last: at(5), Summary: "x", Subagents: 1}},
	})
	if !strings.Contains(got, "1 sub-agent") || strings.Contains(got, "1 sub-agents") {
		t.Errorf("subagent count is wrong or badly pluralised:\n%s", got)
	}
}

// A session running past midnight must not render as a nine-minute one.
func TestSpan_ShowsBothDatesWhenTheyDiffer(t *testing.T) {
	a := time.Date(2026, 8, 8, 23, 51, 0, 0, time.Local)
	b := time.Date(2026, 8, 9, 0, 12, 0, 0, time.Local)

	got := span(a, b)
	if !strings.Contains(got, "2026-08-08 23:51") || !strings.Contains(got, "2026-08-09 00:12") {
		t.Errorf("span across midnight = %q, want both dates", got)
	}

	noon := time.Date(2026, 8, 8, 12, 0, 0, 0, time.Local)
	sameDay := span(noon, noon.Add(20*time.Minute))
	if strings.Count(sameDay, "2026-") != 1 {
		t.Errorf("same-day span = %q, want the date printed once", sameDay)
	}
}

func TestSpan_ZeroTimeIsHonest(t *testing.T) {
	if got := span(time.Time{}, time.Time{}); got != "time unknown" {
		t.Errorf("span(zero) = %q, want it to say the time is unknown", got)
	}
}
