package turn

import (
	"encoding/json"
	"time"
)

const (
	callIndexLimit       = 512
	callIndexThreadLimit = 4096
	callIndexMaxAge      = 24 * time.Hour
	callIndexSweepEvery  = time.Minute
)

// callRef identifies an observed invocation; CapturedAt is archive observation time.
type callRef struct {
	ResponseID string    `json:"response_id"`
	TurnID     string    `json:"turn_id"`
	ToolName   string    `json:"tool_name"`
	CapturedAt time.Time `json:"captured_at"`
	Occurrence int       `json:"occurrence"`
}

// callIndex holds bounded correlation history, scoped to originating threads.
// advance supplies the archive clock without adding a wall-clock lookup parameter.
type callIndex struct {
	clock     time.Time
	nextSweep time.Time
	threads   map[string][]callIndexEntry
}

type callIndexEntry struct {
	CallID   string  `json:"call_id"`
	Ref      callRef `json:"ref"`
	Consumed bool    `json:"consumed,omitempty"`
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
	c.enforceLimits()
	return nil
}

// advance moves the archive clock forward. Archive records can be replayed after
// a restart, so a timestamp older than the current archive clock never rewinds it.
func (c *callIndex) advance(at time.Time) {
	if at.After(c.clock) {
		c.clock = at
	}
}

// record retains a completed call in the bounded index. A consumed entry remains
// until it expires or is evicted so a reused call ID receives the next occurrence.
// Calls without both correlation keys cannot safely be associated and are ignored.
func (c *callIndex) record(thread, callID string, ref callRef) int {
	if thread == "" || callID == "" {
		return 0
	}
	c.advance(ref.CapturedAt)
	if c.threads == nil {
		c.threads = make(map[string][]callIndexEntry)
	}
	// Sweep inactive threads at most once per archive minute. Capacity eviction
	// remains immediate, and the active thread is pruned on every insertion.
	if c.nextSweep.IsZero() || !c.clock.Before(c.nextSweep) {
		c.pruneAll()
	}
	entries := c.prune(c.threads[thread])
	occurrence := 1
	for _, entry := range entries {
		if entry.CallID == callID && entry.Ref.Occurrence >= occurrence {
			occurrence = entry.Ref.Occurrence + 1
		}
	}
	ref.Occurrence = occurrence
	entries = append(entries, callIndexEntry{CallID: callID, Ref: ref})
	entries = c.prune(entries)
	if len(entries) == 0 {
		delete(c.threads, thread)
		return 0
	}
	if len(entries) > callIndexLimit {
		entries = append([]callIndexEntry(nil), entries[len(entries)-callIndexLimit:]...)
	}
	c.threads[thread] = entries
	if len(c.threads) > callIndexThreadLimit {
		c.evictOldestThread()
	}
	return occurrence
}

// lookup finds an unconsumed origin only in the supplied thread. An exact match is
// marked consumed so a duplicate tool output cannot attach to the same invocation.
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
		if entry.CallID != callID || entry.Consumed {
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
	entries[matchAt].Consumed = true
	c.threads[thread] = entries
	return ref, "exact"
}

// evictOldestThread removes the resident thread whose newest correlation entry is
// oldest. A lexical thread-ID tie-break makes checkpoint-sized pressure deterministic.
func (c *callIndex) evictOldestThread() {
	var oldestThread string
	var oldestNewest time.Time
	for thread, entries := range c.threads {
		if len(entries) == 0 {
			delete(c.threads, thread)
			continue
		}
		newest := entries[0].Ref.CapturedAt
		for _, entry := range entries[1:] {
			if entry.Ref.CapturedAt.After(newest) {
				newest = entry.Ref.CapturedAt
			}
		}
		if oldestThread == "" || newest.Before(oldestNewest) || (newest.Equal(oldestNewest) && thread < oldestThread) {
			oldestThread, oldestNewest = thread, newest
		}
	}
	if oldestThread != "" {
		delete(c.threads, oldestThread)
	}
}

// enforceLimits repairs decoded or programmatically supplied state before it can
// become resident. Normal record insertion participates in global eviction after
// the new entry has been added, so an older incoming thread evicts itself.
func (c *callIndex) enforceLimits() {
	c.pruneAll()
	for thread, entries := range c.threads {
		if len(entries) > callIndexLimit {
			c.threads[thread] = append([]callIndexEntry(nil), entries[len(entries)-callIndexLimit:]...)
		}
	}
	for len(c.threads) > callIndexThreadLimit {
		c.evictOldestThread()
	}
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
	c.nextSweep = c.clock.Add(callIndexSweepEvery)
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
	snapshot := callIndex{clock: c.clock, threads: c.copyThreads()}
	snapshot.enforceLimits()
	return snapshot
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
