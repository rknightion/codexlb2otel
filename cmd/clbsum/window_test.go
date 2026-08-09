package main

import (
	"slices"
	"testing"
	"time"

	"github.com/rknightion/codexlb2otel/internal/turn"
)

func utc(day, hour, min int) time.Time {
	return time.Date(2026, 8, day, hour, min, 0, 0, time.UTC)
}

func TestArchiveHour(t *testing.T) {
	cases := []struct {
		name string
		path string
		want time.Time
		ok   bool
	}{
		{"plain", "corpus/processed/2026-08-07T10.jsonl.gz", utc(7, 10, 0), true},
		{"uncompressed", "2026-08-07T10.jsonl", utc(7, 10, 0), true},
		// clbsync keeps a recreated capture alongside its predecessor rather than
		// overwriting it. Failing to parse this drops a whole hour of a window.
		{"regenerated", "corpus/processed/2026-08-07T10.gen2.jsonl.gz", utc(7, 10, 0), true},
		{"gen11", "2026-08-07T10.gen11.jsonl.gz", utc(7, 10, 0), true},
		{"midnight", "2026-08-07T00.jsonl.gz", utc(7, 0, 0), true},
		{"not an archive name", "notes.txt", time.Time{}, false},
		{"no timestamp", "corpus/processed/checkpoint.json", time.Time{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := archiveHour(tc.path)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if ok && !got.Equal(tc.want) {
				t.Errorf("hour = %s, want %s", got, tc.want)
			}
		})
	}
}

// codex-lb writes these names in UTC. Parsing them in the local zone shifts every file by
// the machine's offset and silently drops an hour from the edge of the window.
func TestArchiveHour_IsUTC(t *testing.T) {
	got, ok := archiveHour("2026-08-07T10.jsonl.gz")
	if !ok {
		t.Fatal("did not parse")
	}
	if _, offset := got.Zone(); offset != 0 {
		t.Errorf("parsed with offset %d, want UTC", offset)
	}
}

func TestFilesInWindow(t *testing.T) {
	files := []string{
		"2026-08-07T06.jsonl.gz",
		"2026-08-07T07.jsonl.gz",
		"2026-08-07T08.jsonl.gz",
		"2026-08-07T09.jsonl.gz",
		"2026-08-07T10.jsonl.gz",
		"2026-08-07T11.jsonl.gz",
		"2026-08-07T12.jsonl.gz",
	}
	// A 09:00-10:00 window, widened by an hour each side, reaches 08:00 through 11:00.
	got := filesInWindow(files, utc(7, 9, 0), utc(7, 10, 0), defaultSlop)
	want := []string{
		"2026-08-07T08.jsonl.gz",
		"2026-08-07T09.jsonl.gz",
		"2026-08-07T10.jsonl.gz",
		"2026-08-07T11.jsonl.gz",
	}
	if !slices.Equal(got, want) {
		t.Errorf("filesInWindow =\n%v\nwant\n%v", got, want)
	}
}

// The reason the slop exists: a turn's frames can begin in one hourly archive and complete
// in the next, so a session starting at 08:59 is invisible to a 09:00 window without it -
// and that is exactly the session a morning summary wants most.
func TestFilesInWindow_ReachesTheHourBeforeTheWindow(t *testing.T) {
	files := []string{"2026-08-07T08.jsonl.gz", "2026-08-07T09.jsonl.gz"}
	got := filesInWindow(files, utc(7, 9, 0), utc(7, 9, 30), defaultSlop)
	if !slices.Contains(got, "2026-08-07T08.jsonl.gz") {
		t.Errorf("the preceding hour was dropped: %v", got)
	}
}

// A naming-convention change must not silently remove data from the scan. Reading one
// archive too many costs seconds; missing one produces a confidently incomplete summary.
func TestFilesInWindow_KeepsUnparseableNames(t *testing.T) {
	files := []string{"2026-08-07T09.jsonl.gz", "weird-name.jsonl.gz"}
	got := filesInWindow(files, utc(7, 9, 0), utc(7, 10, 0), defaultSlop)
	if !slices.Contains(got, "weird-name.jsonl.gz") {
		t.Errorf("an unparseable name was dropped rather than kept: %v", got)
	}
}

func TestFilesInWindow_ExcludesTheFarSide(t *testing.T) {
	files := []string{"2026-08-06T09.jsonl.gz", "2026-08-08T09.jsonl.gz"}
	if got := filesInWindow(files, utc(7, 9, 0), utc(7, 10, 0), defaultSlop); len(got) != 0 {
		t.Errorf("kept archives a day away: %v", got)
	}
}

func TestInWindow_IsOverlapNotContainment(t *testing.T) {
	from, to := utc(7, 9, 0), utc(7, 18, 0)
	cases := []struct {
		name        string
		first, last time.Time
		want        bool
	}{
		{"wholly inside", utc(7, 10, 0), utc(7, 11, 0), true},
		{"straddles the start", utc(7, 8, 50), utc(7, 9, 10), true},
		{"straddles the end", utc(7, 17, 55), utc(7, 18, 30), true},
		{"encloses the window", utc(7, 7, 0), utc(7, 20, 0), true},
		{"ends before it opens", utc(7, 7, 0), utc(7, 8, 0), false},
		{"starts after it closes", utc(7, 19, 0), utc(7, 20, 0), false},
		{"touches the start exactly", utc(7, 8, 0), utc(7, 9, 0), true},
		{"touches the end exactly", utc(7, 18, 0), utc(7, 19, 0), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := inWindow(&turn.Turn{FirstTS: tc.first, LastTS: tc.last}, from, to)
			if got != tc.want {
				t.Errorf("inWindow = %v, want %v", got, tc.want)
			}
		})
	}
}

// A turn the reducer flushed without ever seeing a completion has no LastTS. Treating that
// as the zero time puts it before every window and hides an in-flight session.
func TestInWindow_MissingLastTSFallsBackToFirst(t *testing.T) {
	if !inWindow(&turn.Turn{FirstTS: utc(7, 10, 0)}, utc(7, 9, 0), utc(7, 18, 0)) {
		t.Error("a turn with no LastTS was excluded")
	}
}

func TestInWindow_TurnWithNoTimestampsIsExcluded(t *testing.T) {
	if inWindow(&turn.Turn{}, utc(7, 9, 0), utc(7, 18, 0)) {
		t.Error("a turn with no timestamps at all should not match a window")
	}
}

func TestParseWhen(t *testing.T) {
	got, err := parseWhen("2026-08-07T09:30:00Z")
	if err != nil {
		t.Fatalf("RFC3339: %v", err)
	}
	if !got.Equal(utc(7, 9, 30)) {
		t.Errorf("RFC3339 = %s, want %s", got, utc(7, 9, 30))
	}

	// The short form is LOCAL, because "from 9am" means the user's 9am. Reading it as UTC
	// would silently return the wrong sessions rather than an error.
	short, err := parseWhen("2026-08-07T09:30")
	if err != nil {
		t.Fatalf("short form: %v", err)
	}
	want := time.Date(2026, 8, 7, 9, 30, 0, 0, time.Local)
	if !short.Equal(want) {
		t.Errorf("short form = %s, want %s (local)", short, want)
	}

	if _, err := parseWhen("yesterday"); err == nil {
		t.Error("parseWhen accepted nonsense")
	}
}

func TestWindow_FlagCombinations(t *testing.T) {
	if _, _, err := window(4*time.Hour, "2026-08-07T09:00", ""); err == nil {
		t.Error("-since and -from together should be rejected")
	}
	if _, _, err := window(0, "", ""); err == nil {
		t.Error("no window at all should be rejected")
	}
	if _, _, err := window(0, "2026-08-07T18:00", "2026-08-07T09:00"); err == nil {
		t.Error("-to before -from should be rejected")
	}

	from, to, err := window(4*time.Hour, "", "")
	if err != nil {
		t.Fatalf("-since: %v", err)
	}
	if d := to.Sub(from); d < 4*time.Hour-time.Second || d > 4*time.Hour+time.Second {
		t.Errorf("-since 4h produced a %s window", d)
	}
}
