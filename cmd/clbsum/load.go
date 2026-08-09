package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/rknightion/codexlb2otel/internal/frame"
	"github.com/rknightion/codexlb2otel/internal/live"
	"github.com/rknightion/codexlb2otel/internal/scan"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

// errNoArchives reports a window that no archive covers. Distinguished from a real
// failure so the caller can treat a quiet period as a quiet period.
var errNoArchives = errors.New("no archives cover this window")

// load reads every archive overlapping the window and returns the thread forest.
//
// The forest is built by internal/live rather than here. That package already resolves
// subagent parenting via parent_turn_id with a parent_thread_id fallback, promotes a
// subagent whose parent is missing to a root instead of hiding it, detects forks, and
// recovers the collaboration-layer task name that is a subagent's only human-meaningful
// label. Reimplementing any of that for a second consumer would fork logic the live view
// got right the hard way.
func load(ctx context.Context, progress io.Writer, dir string, from, to time.Time, slop time.Duration) (*live.Store, live.Snapshot, error) {
	all, err := scan.Archives([]string{dir})
	if err != nil {
		return nil, live.Snapshot{}, err
	}
	files := filesInWindow(all, from, to, slop)
	if len(files) == 0 {
		// Not a failure. A window with no archives is a quiet period - a weekend, a
		// holiday, a machine that was off - and a nightly "summarise yesterday" job must
		// not page anyone for it. An unreadable corpus directory is a different thing
		// entirely and has already errored out of scan.Archives above.
		return nil, live.Snapshot{}, errNoArchives
	}
	// Hour order. The reducer is a state machine over a thread's frames and a thread runs
	// for hours across rotations, so feeding files out of order splits one conversation
	// into several.
	sort.Strings(files)

	fmt.Fprintf(progress, "reading %d archive(s) covering %s to %s...\n",
		len(files), from.Format("2006-01-02 15:04"), to.Format("2006-01-02 15:04"))

	// Content is truncated by internal/summary against its own budgets, so the reducer
	// must not truncate first - its defaults are sized for a Loki line, which is far
	// smaller than what a 1M-token context can take.
	opts := turn.DefaultOptions()
	opts.MaxToolOutputChars = 1 << 30
	opts.MaxPromptChars = 1 << 30

	r := turn.NewWithOptions(opts)
	var turns []*turn.Turn
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return nil, live.Snapshot{}, err
		}
		recs, err := scan.Collect(f, func(*frame.Record) bool { return true })
		if err != nil {
			return nil, live.Snapshot{}, fmt.Errorf("%s: %w", f, err)
		}
		for _, rec := range recs {
			done, err := r.Add(rec)
			if done != nil {
				turns = append(turns, done)
			}
			if err != nil {
				return nil, live.Snapshot{}, fmt.Errorf("%s: %w", f, err)
			}
		}
	}
	turns = append(turns, r.Flush()...)

	// A thread is SELECTED by overlapping the window, then summarised from every turn
	// LOADED - which is the whole widened range, not just the window. So the store is fed
	// everything and the window filter below only decides which roots are offered for
	// selection; truncating a conversation at a clock boundary would summarise half a job
	// and read as though the agent forgot what it was doing.
	//
	// The honest bound is the slop, not infinity: a session that began well before the
	// widened range is read from that boundary. -slop widens it when that matters.
	sort.SliceStable(turns, func(i, j int) bool { return turns[i].FirstTS.Before(turns[j].FirstTS) })

	store := live.New(live.Options{
		// Nothing may evict: this is a batch read of a chosen window, not a live window.
		RetainTurns:  len(turns) + 1,
		RetainWindow: 100 * 365 * 24 * time.Hour,
		Content:      true,
		StallAfter:   0,
	})
	if err := store.Emit(ctx, turns); err != nil {
		return nil, live.Snapshot{}, err
	}
	return store, store.Snapshot(), nil
}

// selectable returns the roots that overlap the window, newest first.
func selectable(store *live.Store, snap live.Snapshot, from, to time.Time) []*live.Thread {
	var out []*live.Thread
	for _, th := range snap.Roots {
		if threadInWindow(store, th, from, to) {
			out = append(out, th)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out
}

// threadInWindow reports whether a root or anything beneath it overlaps the window.
//
// The subtree matters: a parent's own turns can all fall outside the window while the
// subagents it spawned worked squarely inside it, and dropping that root would hide the
// session that did the most work.
func threadInWindow(store *live.Store, th *live.Thread, from, to time.Time) bool {
	for _, t := range store.Turns(th.ThreadID) {
		if inWindow(t, from, to) {
			return true
		}
	}
	for _, c := range th.Children {
		if threadInWindow(store, c, from, to) {
			return true
		}
	}
	return false
}
