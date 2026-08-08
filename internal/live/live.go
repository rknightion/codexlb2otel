// Package live keeps a bounded, in-memory view of recent conversation activity and
// serves it as a web UI, so a human can watch threads and the subagents they spawn in
// something close to real time.
//
// It is a WINDOW, not a destination. Nothing here is durable: a restart empties it, and
// Loki remains the store of record. That is why its Sink implementation returns nil from
// Flush and Close without doing anything - there is nothing to lose.
//
// Two constraints from the rest of the service shape this package, and neither is
// negotiable:
//
//  1. Emit runs INSIDE tail.Watcher.Poll, under that watcher's write lock, and its
//     return value gates the checkpoint. So Emit must never block and must never fail.
//     A slow HTTP subscriber that could back-pressure into Emit would stall archive
//     ingestion for the whole service - see subscriber.send.
//
//  2. Nothing here may read turn.Reducer directly. In-flight data arrives via a
//     caller-supplied snapshot function (SetInFlightSource, wired to
//     tail.Watcher.InFlight), which is published atomically and takes no lock. Reading
//     watcher state under its mutex from an HTTP handler is the shape that deadlocked
//     the service in issue #29.
package live

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/rknightion/codexlb2otel/internal/sink"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

// Options tunes what the store keeps and what it is willing to show.
type Options struct {
	// RetainTurns bounds the ring of completed turns. Everything the API serves is
	// derived from this ring, so it is the single memory bound of the package.
	RetainTurns int
	// RetainWindow drops threads whose newest turn is older than this, so an idle
	// overnight thread does not sit at the top of the view forever.
	RetainWindow time.Duration
	// Content gates conversation text. False yields a structural view - models, tool
	// names, subagent kinds, timings - and no prompt or message bodies anywhere,
	// including in the per-thread detail endpoint.
	Content bool
	// IncludeProbe keeps synthetic health-check traffic (Family "probe") in the view.
	// Off by default: it is this service's own noise and it would otherwise dominate a
	// quiet period.
	IncludeProbe bool

	// Now is injectable for tests. Defaults to time.Now.
	Now func() time.Time
}

func (o *Options) setDefaults() {
	if o.RetainTurns <= 0 {
		o.RetainTurns = 500
	}
	if o.RetainWindow <= 0 {
		o.RetainWindow = 2 * time.Hour
	}
	if o.Now == nil {
		o.Now = time.Now
	}
}

// Store holds the view and implements sink.Sink.
type Store struct {
	opts Options

	mu sync.Mutex
	// turns is the ring, oldest first. The thread index is rebuilt from it rather than
	// maintained alongside it - see rebuild for why that trade is worth making.
	turns   []*turn.Turn
	threads map[string]*Thread
	// turnOwner maps a server turn id to the thread that ran it, which is what lets a
	// subagent attach to the exact turn that spawned it rather than to its parent
	// thread as a whole.
	turnOwner map[string]string

	inFlightSrc func() []turn.InFlight

	subs    map[uint64]*subscriber
	nextSub uint64
}

var _ sink.Sink = (*Store)(nil)

// New returns an empty Store.
func New(opts Options) *Store {
	opts.setDefaults()
	return &Store{
		opts:      opts,
		threads:   map[string]*Thread{},
		turnOwner: map[string]string{},
		subs:      map[uint64]*subscriber{},
	}
}

// SetInFlightSource supplies the snapshot function for responses still running. It is
// wired to tail.Watcher.InFlight by the caller rather than imported, so this package
// does not depend on internal/tail and internal/tail does not depend on this one.
//
// The source must be safe to call from any goroutine and must not block; the watcher's
// implementation is a single atomic load.
func (s *Store) SetInFlightSource(fn func() []turn.InFlight) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inFlightSrc = fn
}

// Name identifies the sink.
func (*Store) Name() string { return "live" }

// Emit records a batch and wakes subscribers.
//
// It ALWAYS returns nil. That is not laziness: this sink's error would be indistinguishable
// to the caller from Loki's, and cmd/codexlb2otel refuses to advance the checkpoint past a
// batch any sink failed. Letting a browser-facing view veto ingestion progress would make a
// UI bug into a data-loss bug.
func (s *Store) Emit(_ context.Context, turns []*turn.Turn) error {
	s.mu.Lock()
	for _, t := range turns {
		if t == nil || !s.keep(t) {
			continue
		}
		s.turns = append(s.turns, t)
	}
	s.trim()
	s.rebuild()
	snap := s.snapshotLocked()
	s.mu.Unlock()

	s.broadcast(snap)
	return nil
}

// Flush is a no-op: nothing is buffered, because nothing is delivered anywhere.
func (*Store) Flush(context.Context) error { return nil }

// Close is a no-op for the store itself and disconnects any subscribers.
func (s *Store) Close(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, sub := range s.subs {
		close(sub.ch)
		delete(s.subs, id)
	}
	return nil
}

func (s *Store) keep(t *turn.Turn) bool {
	if !s.opts.IncludeProbe && t.Family == "probe" {
		return false
	}
	return true
}

// trim drops the oldest turns past RetainTurns, copying the survivors down rather than
// re-slicing off the front. Re-slicing would leave the dropped pointers reachable from
// the backing array forever, which on a long-running process is an unbounded leak of
// exactly the objects that carry conversation text.
func (s *Store) trim() {
	if len(s.turns) <= s.opts.RetainTurns {
		return
	}
	drop := len(s.turns) - s.opts.RetainTurns
	kept := append(s.turns[:0], s.turns[drop:]...)
	// Clear the tail the copy-down left behind. Without this the backing array still
	// holds pointers to the evicted turns - and those are the objects carrying whole
	// prompts and command output - so the "bound" would bound the view and not the
	// memory.
	tail := s.turns[:cap(s.turns)]
	for i := len(kept); i < len(tail); i++ {
		tail[i] = nil
	}
	s.turns = kept
}

// rebuild recomputes the whole thread index from the ring.
//
// Maintaining the index incrementally would be cheaper and is the obvious design; it is
// rejected on purpose. Eviction is the hard part of an incremental index - a thread's
// aggregate has to un-count turns that fell out of the ring - and getting that subtly
// wrong produces a view that drifts from its own source with nothing to detect it. The
// ring is bounded at a few hundred entries and this runs once per poll, so the cost is
// noise and the invariant is exact by construction.
func (s *Store) rebuild() {
	cutoff := s.opts.Now().Add(-s.opts.RetainWindow)

	s.threads = make(map[string]*Thread, len(s.threads))
	s.turnOwner = make(map[string]string, len(s.turnOwner))

	byThread := map[string][]*turn.Turn{}
	for _, t := range s.turns {
		id := threadKey(t)
		if id == "" {
			continue
		}
		th := s.threads[id]
		if th == nil {
			th = &Thread{ThreadID: id, FirstSeen: t.FirstTS}
			s.threads[id] = th
		}
		th.observe(t, s.opts.Content)
		byThread[id] = append(byThread[id], t)
		if t.TurnID != "" {
			s.turnOwner[t.TurnID] = id
		}
	}

	for id, th := range s.threads {
		if th.LastSeen.Before(cutoff) {
			delete(s.threads, id)
			continue
		}
		// Both of these need every turn of the thread at once rather than one at a
		// time - the task path is a majority vote, and the spawn list is a dedup - so
		// they run here rather than in observe.
		th.TaskPath = TaskPath(byThread[id])
		th.TaskName = TaskName(th.TaskPath)
		th.Spawned = SpawnedTasks(byThread[id])
		th.Name = th.name()
	}
}

// threadKey is the identity a thread is grouped under. ThreadID is the real one; a turn
// that carries none (a transport-only record, or a response whose create frame was never
// captured) falls back to its session so it is still visible rather than silently
// dropped from a view whose whole job is to show what is happening.
func threadKey(t *turn.Turn) string {
	if t.ThreadID != "" {
		return t.ThreadID
	}
	if t.SessionID != "" {
		return "session:" + t.SessionID
	}
	return ""
}

// Snapshot returns the current view: root threads with their subagents nested beneath
// them, plus everything running right now.
func (s *Store) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

func (s *Store) snapshotLocked() Snapshot {
	// The in-flight source is read under s.mu but never touches the watcher's lock - it
	// is an atomic load behind a function value. See the package comment.
	var running []turn.InFlight
	if s.inFlightSrc != nil {
		running = s.inFlightSrc()
	}

	// Clone each thread so a caller mutating the result cannot corrupt the index, and
	// so Children can be populated without leaving stale links behind for next time.
	clones := make(map[string]*Thread, len(s.threads))
	for id, th := range s.threads {
		c := *th
		c.Children = nil
		c.Running = 0
		clones[id] = &c
	}

	out := Snapshot{At: s.opts.Now(), Content: s.opts.Content}
	for _, f := range running {
		if !s.opts.IncludeProbe && f.Family == "probe" {
			continue
		}
		r := newRunning(f, s.opts.Content)
		out.Running = append(out.Running, r)
		if th := clones[f.ThreadID]; th != nil {
			th.Running++
			th.Activity = r.Activity
		}
	}

	// Parent by the declared PARENT THREAD, falling back to the spawning turn's owner.
	//
	// This order is the REVERSE of what #35 shipped, and the reversal is measured. The
	// original reasoning - a turn is more specific than a thread, so prefer it - is
	// sound for a spawn_agent child and wrong for the fork subagents that actually
	// occur: across the 2026-08-08T16 capture, parent_turn_id is ABSENT on all 22 of
	// them, while parent_thread_id forms a genuine four-level chain
	// (019fe22f -> 019fe210 -> 019fe1ee -> 019fe148). Where parent_turn_id does turn up
	// on a later turn of such a thread it names a turn of the ROOT, so preferring it
	// reparented every descendant onto the root and collapsed the tree to one level.
	//
	// Corroborated independently: the collaboration task paths recovered by TaskPath
	// nest the same way parent_thread_id does
	// (/root/final_integration_review_v2/backend_contract_recheck under
	// /root/final_integration_review_v2), and they disagree with the turn-derived
	// shape. Two independent fields agreeing against a third settles it.
	for id, th := range clones {
		parent := th.ParentThreadID
		if parent == "" && th.ParentTurnID != "" {
			parent = s.turnOwner[th.ParentTurnID]
		}
		// A parent outside the retention window is not a parent we can show. Promote the
		// orphan to a root rather than dropping it: a subagent whose parent has aged out
		// is still live activity, and hiding it is the one failure this view cannot
		// afford.
		if parent != "" && parent != id {
			if p := clones[parent]; p != nil {
				p.Children = append(p.Children, th)
				continue
			}
			th.Orphaned = true
		}
		out.Roots = append(out.Roots, th)
	}

	sortThreads(out.Roots)
	for _, th := range clones {
		sortThreads(th.Children)
	}
	return out
}

// Turns returns the retained turns for one thread, oldest first. Content is stripped
// when the store is configured without it.
func (s *Store) Turns(threadID string) []*turn.Turn {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []*turn.Turn
	for _, t := range s.turns {
		if threadKey(t) != threadID {
			continue
		}
		if s.opts.Content {
			out = append(out, t)
			continue
		}
		out = append(out, redact(t))
	}
	return out
}

// sortThreads orders newest activity first, tiebreaking on id so the order is total and
// the view does not reshuffle between polls.
func sortThreads(ts []*Thread) {
	sort.Slice(ts, func(i, j int) bool {
		if !ts[i].LastSeen.Equal(ts[j].LastSeen) {
			return ts[i].LastSeen.After(ts[j].LastSeen)
		}
		return ts[i].ThreadID < ts[j].ThreadID
	})
}
