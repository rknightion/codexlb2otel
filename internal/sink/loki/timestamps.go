package loki

import (
	"time"

	"github.com/rknightion/codexlb2otel/internal/turn"
)

// Loki timestamps a line at ingest time by default. This sink never uses that: a
// replayed or backfilled turn would sort as "now" and every time-range query built
// against it would lie about when the underlying event actually happened. Each
// record type below picks a DIFFERENT one of the Turn's several clocks, chosen for
// what that record represents - not one blanket "the" timestamp for the whole turn.

// inputTS timestamps content recovered from response.create: prompts, the system
// instructions body, tool outputs and agent messages. All four are populated by the
// reducer's captureInput from that SAME frame (see internal/turn/reducer.go), so
// FirstTS - this response's first captured frame, which is that frame - is their
// true source time. Using the response's completion time instead would misdate
// input content by however long the response took to finish, which for a long tool
// call can be minutes.
func inputTS(t *turn.Turn) time.Time {
	if !t.FirstTS.IsZero() {
		return t.FirstTS
	}
	return t.LastTS
}

// outputTS timestamps content the MODEL produced during this response - assistant
// messages and tool calls. ServerCompletedAt is the server's own record of when the
// response finished, which is what "when did the model say this" means for a
// response that completed normally. LastTS (this response's last captured frame)
// is the fallback for turns that never got a completion event at all - a transport
// drop or a mid-response error - where ServerCompletedAt is the very thing missing.
func outputTS(t *turn.Turn) time.Time {
	if !t.ServerCompletedAt.IsZero() {
		return t.ServerCompletedAt
	}
	if !t.LastTS.IsZero() {
		return t.LastTS
	}
	return t.FirstTS
}

// turnTS timestamps the turn-metadata record itself. Same preference as outputTS -
// prefer the server's own completion time, since that is what "this turn happened"
// means for a response that finished - kept as its own function so a future
// divergence (e.g. keying the metadata line off TurnStart instead) is a one-line
// change here rather than a grep-and-replace through outputTS's callers.
func turnTS(t *turn.Turn) time.Time {
	if !t.ServerCompletedAt.IsZero() {
		return t.ServerCompletedAt
	}
	if !t.LastTS.IsZero() {
		return t.LastTS
	}
	return t.FirstTS
}

// lifecycleTS timestamps transport and error records, which by definition often
// describe turns that never reached ServerCompletedAt - a connection can drop or
// error mid-response, which is exactly the case these two record types exist to
// capture. LastTS (the last frame this sink actually saw for the turn) is the
// honest "when did we learn about this" answer.
func lifecycleTS(t *turn.Turn) time.Time {
	if !t.LastTS.IsZero() {
		return t.LastTS
	}
	return t.FirstTS
}

// safeTS guards against ever shipping a zero-value (1970) timestamp. FirstTS and
// LastTS are set from the frame's own timestamp on every real Turn the reducer
// produces (see reducer.Add), so this should be unreachable in practice - it exists
// so a future change to the reducer that left both zero fails safe (a line dated
// "now") instead of failing invisibly (a line dated the epoch, sorting before
// everything else in the stream and likely tripping Loki's out-of-order rejection).
func safeTS(ts time.Time) time.Time {
	if ts.IsZero() {
		return time.Now().UTC()
	}
	return ts
}
