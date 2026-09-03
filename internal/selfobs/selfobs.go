// Package selfobs collects this service's own operational state - the tail
// watcher's progress against the archive, the reducer's in-memory size, and each
// sink's delivery health - into one Snapshot (issue #8).
//
// It exists as its own package rather than living inside internal/tail or
// internal/sink/otlpmetric directly: otlpmetric turns a Snapshot into metrics
// without needing to import the concrete tail.Watcher type, and tail does not need
// to know anything about metrics. cmd/codexlb2otel is the only place that wires a
// live Watcher and Sink into a Collector.
package selfobs

import (
	"time"

	"github.com/rknightion/codexlb2otel/internal/sink"
	"github.com/rknightion/codexlb2otel/internal/tail"
)

// Snapshot is this process's own operational state, collected fresh at each metrics
// collection tick rather than pushed per turn - self-observability describes the
// pipeline carrying GenAI operations, not one operation itself, which is why none of
// this rides along with a *turn.Turn the way every other emitted measurement does.
type Snapshot struct {
	// Now and Watermark are kept separate rather than pre-subtracted here, so
	// IngestLagSeconds stays the one place that does the subtraction and the one
	// place a test can pin it - see that method's own doc comment for why this is the
	// ONLY wall-clock touch anywhere in this self-observability path.
	Now          time.Time
	Watermark    time.Time
	HasWatermark bool

	FilesWatched      int64
	CurrentFile       string
	CurrentFileOffset int64

	BytesRead          int64
	MembersRead        int64
	LinesDecoded       int64
	UndecodableLines   int64
	PartialMemberReads int64
	DecodeErrors       int64
	FileReplacements   int64
	FilesReclaimed     int64
	TurnsEmitted       int64
	TurnsEvicted       int64
	OpenResponses      int64
	ReducerSeriesCount int64
	ReducerThreadCount int64
	EnrichCacheEntries int64
	HasDrift           bool
	DriftBreaking      int64
	DriftNew           int64
	DriftInfo          int64

	Sinks []SinkHealth
}

// SinkHealth is one sink's own delivery health, read through the sink.Reporter
// interface every sink already implements - issue #8's explicit instruction to reuse
// that rather than add a parallel counting mechanism.
type SinkHealth struct {
	Name       string
	Rejections []sink.Rejection
	Pending    int
}

// IngestLagSeconds is issue #8's "one metric that matters": wall-clock now minus the
// newest record timestamp the watcher has seen. False until the watcher has processed
// at least one record - before that there is no watermark to diff against, and
// reporting a lag against the Go zero time would read as a decades-long outage
// instead of the honest "no data yet" it actually is.
//
// This is the ONLY subtraction against wall-clock time anywhere in this self-
// observability path. internal/tail's Poll measures EVICTION against the exact same
// watermark this reads, but never against time.Now() - deliberately, so that
// catching up on a backlog does not evict every in-flight response on the first pass
// purely because the archive is hours old (see Poll's own comment, and the
// incremental-append test that caught this originally). Keeping "watermark vs now"
// and "watermark vs watermark-minus-EvictAfter" in two different packages, only one
// of which ever calls time.Now(), is deliberate - not an oversight this file's
// author failed to unify.
func (s Snapshot) IngestLagSeconds() (float64, bool) {
	if !s.HasWatermark {
		return 0, false
	}
	return s.Now.Sub(s.Watermark).Seconds(), true
}

// Collector gathers a Snapshot from a live tail.Watcher and the sink tree it feeds.
// w and s must be the actual instances wired into the running service - a Collector
// over any other pair would report someone else's progress.
type Collector struct {
	w             *tail.Watcher
	s             sink.Sink
	enrichEntries func() int64
	driftCounts   func() (breaking, newFindings, info int64)
}

// New builds a Collector over w and s.
func New(w *tail.Watcher, s sink.Sink) *Collector { return &Collector{w: w, s: s} }

// SetEnrichmentSource adds the optional Postgres cache-size source. It is configured
// once during startup before the first metric collection.
func (c *Collector) SetEnrichmentSource(source func() int64) { c.enrichEntries = source }

// SetDriftSource adds the optional archive-drift finding source. It is configured
// once during startup before the first metric collection.
func (c *Collector) SetDriftSource(source func() (breaking, newFindings, info int64)) {
	c.driftCounts = source
}

// Collect builds a fresh Snapshot. Safe to call from any goroutine: the one call here
// that touches state Poll also mutates - w.Progress() - is lock-protected for exactly
// that (see tail.Watcher's own doc comment on its mutex).
func (c *Collector) Collect() Snapshot {
	p := c.w.Progress()
	snap := Snapshot{
		Now:                time.Now().UTC(),
		Watermark:          p.Watermark,
		HasWatermark:       !p.Watermark.IsZero(),
		FilesWatched:       p.Stats.FilesSeen,
		CurrentFile:        p.CurrentFile,
		CurrentFileOffset:  p.CurrentFileOffset,
		BytesRead:          p.Stats.BytesRead,
		MembersRead:        p.Stats.MembersRead,
		LinesDecoded:       p.Stats.FramesRead,
		UndecodableLines:   p.Stats.ParseErrors,
		PartialMemberReads: p.Stats.PartialMemberReads,
		DecodeErrors:       p.Stats.DecodeErrors,
		FileReplacements:   p.Stats.FilesReplaced,
		FilesReclaimed:     p.Stats.FilesDeleted,
		TurnsEmitted:       p.Stats.TurnsEmitted,
		TurnsEvicted:       p.Stats.EvictedOpen,
		OpenResponses:      int64(p.OpenResponses),
		ReducerSeriesCount: int64(p.ReducerSeriesCount),
		ReducerThreadCount: int64(p.ReducerThreadCount),
		Sinks:              collectSinkHealth(c.s),
	}
	if c.enrichEntries != nil {
		snap.EnrichCacheEntries = c.enrichEntries()
	}
	if c.driftCounts != nil {
		snap.HasDrift = true
		snap.DriftBreaking, snap.DriftNew, snap.DriftInfo = c.driftCounts()
	}
	return snap
}

// collectSinkHealth walks the sink tree the same way cmd/codexlb2otel's own
// reportRejections does (main.go), duplicated rather than shared: it is ten lines,
// internal/sink.Sink/Multi/Reporter are all exported for exactly this kind of
// read-only walk, and neither call site owns the other.
func collectSinkHealth(s sink.Sink) []SinkHealth {
	if s == nil {
		return nil
	}
	var out []SinkHealth
	var walk func(sink.Sink)
	walk = func(x sink.Sink) {
		if m, ok := x.(sink.Multi); ok {
			for _, inner := range m {
				walk(inner)
			}
			return
		}
		if r, ok := x.(sink.Reporter); ok {
			out = append(out, SinkHealth{Name: x.Name(), Rejections: r.Rejections(), Pending: r.Pending()})
		}
	}
	walk(s)
	return out
}
