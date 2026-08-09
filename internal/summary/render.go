package summary

import (
	"fmt"
	"io"
	"time"
)

// Render writes the report as markdown.
//
// Failures are rendered, not hidden. A session that could not be summarised gets a section
// saying so with its error, because a report that silently omits three of eleven sessions
// reads as a complete account of a quiet day.
func Render(w io.Writer, r Report) error {
	ok, failed := 0, 0
	for _, s := range r.Sessions {
		if s.Err == nil && s.Summary != "" {
			ok++
		} else {
			failed++
		}
	}

	fmt.Fprintf(w, "# Work summary — %s to %s\n\n", stamp(r.From), stamp(r.To))

	if len(r.Sessions) == 0 {
		fmt.Fprintf(w, "No sessions ran in this window.\n")
		return nil
	}

	fmt.Fprintf(w, "%s in this window", plural(len(r.Sessions), "session", "sessions"))
	if failed > 0 {
		fmt.Fprintf(w, ", %d of which could not be summarised", failed)
	}
	fmt.Fprintf(w, ".\n\n")

	if r.Window != "" {
		fmt.Fprintf(w, "## Across the window\n\n%s\n\n", r.Window)
		if failed > 0 {
			fmt.Fprintf(w, "*This covers the %s that summarised successfully; the %s below did not.*\n\n",
				plural(ok, "session", "sessions"), plural(failed, "session", "sessions"))
		}
	} else if r.WindowErr != nil {
		fmt.Fprintf(w, "## Across the window\n\n*The roll-up could not be produced: %v*\n\n", r.WindowErr)
	}

	fmt.Fprintf(w, "## Sessions\n")
	for _, s := range r.Sessions {
		fmt.Fprintf(w, "\n### %s — %s\n\n", s.Name, span(s.First, s.Last))

		var notes []string
		if s.Orphaned {
			notes = append(notes, "sub-agent whose parent was not captured, shown on its own")
		}
		if s.Subagents > 0 {
			notes = append(notes, plural(s.Subagents, "sub-agent", "sub-agents"))
		}
		if s.Passes > 1 {
			// Said out loud because it explains a vaguer narrative. A reader comparing a
			// thin summary against a long session should know it was summarised in
			// stages rather than conclude the session did little.
			notes = append(notes, fmt.Sprintf("summarised in %d passes", s.Passes))
		}
		if len(notes) > 0 {
			fmt.Fprintf(w, "*%s*\n\n", join(notes, " · "))
		}

		if s.Err != nil {
			fmt.Fprintf(w, "Could not be summarised: %v\n", s.Err)
			continue
		}
		fmt.Fprintf(w, "%s\n", s.Summary)
	}
	return nil
}

// span renders a session's real time span, which may run outside the requested window - a
// selected thread is read whole. It prints both dates when they differ, so an overnight
// session does not read as a nine-minute one.
func span(a, b time.Time) string {
	a, b = a.Local(), b.Local()
	if a.IsZero() || b.IsZero() {
		return "time unknown"
	}
	if a.YearDay() == b.YearDay() && a.Year() == b.Year() {
		return fmt.Sprintf("%s–%s", a.Format("2006-01-02 15:04"), b.Format("15:04"))
	}
	return fmt.Sprintf("%s – %s", a.Format("2006-01-02 15:04"), b.Format("2006-01-02 15:04"))
}

func stamp(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	return t.Local().Format("2006-01-02 15:04")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func join(ss []string, sep string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += sep
		}
		out += s
	}
	return out
}
