package turn

import (
	"sort"
	"time"
)

// StatusIncomplete marks a turn emitted without ever seeing response.completed.
// These are real - a websocket can drop, and codex-lb drops records under disk
// pressure - so they are reported rather than discarded, but their usage figures
// are absent and their deltas unreliable.
const StatusIncomplete = "incomplete"

// StatusError marks a turn terminated by an error frame rather than a completion.
// Distinct from StatusIncomplete: here the server said why it ended.
const StatusError = "error"

// Flush emits every in-flight response and clears them. Use at end of input.
//
// Without this, a response whose completion frame never arrives would sit in the
// open map forever: a slow memory leak, and silently missing telemetry.
func (r *Reducer) Flush() []*Turn {
	out := make([]*Turn, 0, len(r.open))
	for id, t := range r.open {
		markIncomplete(t)
		r.ensureLogicalTurn(t)
		out = append(out, t)
		delete(r.open, id)
	}
	sortByStart(out)
	return out
}

// Evict emits in-flight responses whose last frame is older than cutoff. This is the
// long-running equivalent of Flush: a response still open well past any plausible
// completion is never going to complete.
func (r *Reducer) Evict(cutoff time.Time) []*Turn {
	var out []*Turn
	for id, t := range r.open {
		if t.LastTS.Before(cutoff) {
			markIncomplete(t)
			r.ensureLogicalTurn(t)
			out = append(out, t)
			delete(r.open, id)
		}
	}
	sortByStart(out)
	return out
}

func markIncomplete(t *Turn) {
	if t.Status == "" {
		t.Status = StatusIncomplete
	}
}

func sortByStart(ts []*Turn) {
	sort.SliceStable(ts, func(i, j int) bool { return ts[i].FirstTS.Before(ts[j].FirstTS) })
}
