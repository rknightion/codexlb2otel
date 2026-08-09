package main

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/rknightion/codexlb2otel/internal/turn"
)

// archiveHourLayout is how codex-lb names an archive: one file per hour, e.g.
// "2026-08-07T10.jsonl.gz". Colons are illegal in a filename on some systems, so the
// clock uses dots - which is also why this cannot simply be time.RFC3339.
const archiveHourLayout = "2006-01-02T15"

// defaultSlop widens the file scan either side of the window.
//
// A turn's frames can begin in one hourly archive and complete in the next, and the file a
// turn belongs to is decided by when its frames were WRITTEN, not by the turn's own
// timestamps. Without the slop a session that started at 08:59 is invisible to a window
// beginning at 09:00, which is exactly the session a morning summary wants most.
//
// It is also the honest bound on "a selected session is read whole". A session is SELECTED
// by overlapping the window and is then summarised from every turn that was LOADED - but
// only the widened range is loaded, so a session that began four hours before the window
// is read from the slop boundary, not from its true beginning. An hour covers the
// rotation-straddle case that motivates the slop at all; -slop widens it for a session
// known to run much longer.
const defaultSlop = time.Hour

// archiveHour extracts the hour an archive file covers.
//
// It tolerates the suffixes clbsync adds. A capture that codex-lb recreated from scratch
// is kept alongside its predecessor as "NAME.gen2.jsonl.gz" rather than overwriting it,
// so a name can carry more than the timestamp and still be a real archive for that hour -
// dropping those would silently lose a whole hour of a window.
func archiveHour(path string) (time.Time, bool) {
	name := filepath.Base(path)
	// Strip every extension, leaving the timestamp and any generation marker.
	for {
		ext := filepath.Ext(name)
		if ext == "" {
			break
		}
		name = strings.TrimSuffix(name, ext)
	}
	// The timestamp is the first dot-separated field; "2026-08-07T10.gen2" keeps only
	// "2026-08-07T10". Splitting is safe because the layout itself has no dots.
	if i := strings.IndexByte(name, '.'); i >= 0 {
		name = name[:i]
	}
	// Parsed as UTC: codex-lb writes these in UTC, and reading them in the local zone
	// would shift every file by the offset and drop an hour from the edge of the window.
	t, err := time.ParseInLocation(archiveHourLayout, name, time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// filesInWindow narrows a corpus listing to the archives that could hold turns in
// [from, to], widened by slop at each end.
//
// A file whose name does not parse is KEPT rather than dropped. The alternative silently
// omits data because of a naming convention change, and reading one archive too many
// costs seconds where missing one produces a confidently incomplete summary.
func filesInWindow(files []string, from, to time.Time, slop time.Duration) []string {
	lo, hi := from.Add(-slop), to.Add(slop)
	out := make([]string, 0, len(files))
	for _, f := range files {
		h, ok := archiveHour(f)
		if !ok {
			out = append(out, f)
			continue
		}
		// The file covers [h, h+1h). It is out of range when it ends at or before lo -
		// an archive ending exactly at lo holds nothing at or after it - or when it
		// begins after hi. A file beginning exactly at hi is kept, because it covers hi.
		if !h.Add(time.Hour).After(lo) || h.After(hi) {
			continue
		}
		out = append(out, f)
	}
	return out
}

// inWindow reports whether a turn overlaps [from, to] at all.
//
// Overlap, not containment: a turn running from 08:50 to 09:10 belongs to a window opening
// at 09:00, and so does one running 17:55 to 18:30 for a window closing at 18:00.
func inWindow(t *turn.Turn, from, to time.Time) bool {
	last := t.LastTS
	if last.IsZero() {
		last = t.FirstTS
	}
	if t.FirstTS.IsZero() && last.IsZero() {
		return false
	}
	return !last.Before(from) && !t.FirstTS.After(to)
}

// parseWhen accepts an RFC3339 instant or a local "2006-01-02T15:04".
//
// The short form is local because that is what a human means by "from 9am" - accepting it
// as UTC would silently shift the window by the machine's offset and quietly return the
// wrong sessions rather than an error.
func parseWhen(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.ParseInLocation("2006-01-02T15:04", s, time.Local)
}
