package turn

import (
	"encoding/json"
	"time"
)

const (
	callIndexLimit  = 512
	callIndexMaxAge = 24 * time.Hour
)

// callRef identifies an observed invocation; CapturedAt is archive observation time.
type callRef struct {
	ResponseID string    `json:"response_id"`
	TurnID     string    `json:"turn_id"`
	ToolName   string    `json:"tool_name"`
	CapturedAt time.Time `json:"captured_at"`
}

// callIndex holds unconsumed tool calls, scoped to their originating thread.
// advance supplies the archive clock without adding a wall-clock lookup parameter.
type callIndex struct {
	clock   time.Time
	threads map[string][]callIndexEntry
}

type callIndexEntry struct {
	CallID string  `json:"call_id"`
	Ref    callRef `json:"ref"`
}

type callIndexWire struct {
	Clock   time.Time                   `json:"clock,omitempty"`
	Threads map[string][]callIndexEntry `json:"threads,omitempty"`
}

// MarshalJSON keeps the implementation maps private while making checkpoints
// stable and content-free apart from the correlation identifiers already in State.
func (c callIndex) MarshalJSON() ([]byte, error) {
	return json.Marshal(callIndexWire{Clock: c.clock, Threads: c.copyThreads()})
}

func (c *callIndex) UnmarshalJSON(data []byte) error {
	var wire callIndexWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	c.clock = wire.Clock
	c.threads = cloneCallThreads(wire.Threads)
	return nil
}

// advance moves the archive clock forward. Archive records can be replayed after
// a restart, so a timestamp older than the current archive clock never rewinds it.
func (c *callIndex) advance(at time.Time) {
	if at.After(c.clock) {
		c.clock = at
	}
}

// record retains a completed call until one unambiguous output consumes it. Calls
// without both correlation keys cannot safely be associated and are ignored.
func (c *callIndex) record(thread, callID string, ref callRef) {
	if thread == "" || callID == "" {
		return
	}
	c.advance(ref.CapturedAt)
	if c.threads == nil {
		c.threads = make(map[string][]callIndexEntry)
	}
	c.pruneAll()
	entries := c.threads[thread]
	entries = append(entries, callIndexEntry{CallID: callID, Ref: ref})
	entries = c.prune(entries)
	if len(entries) > callIndexLimit {
		entries = append([]callIndexEntry(nil), entries[len(entries)-callIndexLimit:]...)
	}
	c.threads[thread] = entries
}

// lookup finds an origin only in the supplied thread. An exact match is consumed
// so a duplicate tool output cannot attach to the same invocation again.
func (c *callIndex) lookup(thread, callID string) (callRef, string) {
	if thread == "" || callID == "" {
		return callRef{}, "none"
	}
	entries := c.prune(c.threads[thread])
	if len(entries) == 0 {
		delete(c.threads, thread)
	} else {
		c.threads[thread] = entries
	}
	matchAt := -1
	for i, entry := range entries {
		if entry.CallID != callID {
			continue
		}
		if matchAt >= 0 {
			return callRef{}, "ambiguous"
		}
		matchAt = i
	}
	if matchAt < 0 {
		return callRef{}, "none"
	}
	ref := entries[matchAt].Ref
	entries = append(entries[:matchAt], entries[matchAt+1:]...)
	if len(entries) == 0 {
		delete(c.threads, thread)
	} else {
		c.threads[thread] = entries
	}
	return ref, "exact"
}

func (c *callIndex) prune(entries []callIndexEntry) []callIndexEntry {
	if c.clock.IsZero() {
		return entries
	}
	cutoff := c.clock.Add(-callIndexMaxAge)
	kept := entries[:0]
	for _, entry := range entries {
		if entry.Ref.CapturedAt.IsZero() || !entry.Ref.CapturedAt.Before(cutoff) {
			kept = append(kept, entry)
		}
	}
	return kept
}

func (c *callIndex) pruneAll() {
	for thread, entries := range c.threads {
		entries = c.prune(entries)
		if len(entries) == 0 {
			delete(c.threads, thread)
			continue
		}
		c.threads[thread] = entries
	}
}

func (c callIndex) entries(thread string) int { return len(c.threads[thread]) }

func (c callIndex) snapshot() callIndex {
	return callIndex{clock: c.clock, threads: c.copyThreads()}
}

func (c callIndex) copyThreads() map[string][]callIndexEntry {
	return cloneCallThreads(c.threads)
}

func cloneCallThreads(in map[string][]callIndexEntry) map[string][]callIndexEntry {
	if in == nil {
		return nil
	}
	out := make(map[string][]callIndexEntry, len(in))
	for thread, entries := range in {
		out[thread] = append([]callIndexEntry(nil), entries...)
	}
	return out
}
