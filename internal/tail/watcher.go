package tail

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/rknightion/codexlb2otel/internal/archive"
	"github.com/rknightion/codexlb2otel/internal/frame"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

// Config controls the watcher.
type Config struct {
	// Dir holds the archive files, named <YYYY-MM-DD>T<HH>.jsonl.gz.
	Dir string
	// CheckpointPath must live on a volume that survives container restarts.
	CheckpointPath string
	// ChunkSize bounds how much of a file is read into memory per pass. Archive
	// files reach several hundred MB, so reading one whole is not acceptable in a
	// long-running process.
	ChunkSize int
	// PollInterval is how often the directory is rescanned.
	PollInterval time.Duration
	// EvictAfter emits in-flight responses idle for this long. Without it a response
	// whose completion frame never arrives occupies memory forever.
	EvictAfter time.Duration
	// DeleteAfter reclaims fully ingested files older than this. Zero disables
	// deletion entirely, which is the default: this removes the only copy of the
	// raw capture, so it is opt-in rather than something that happens by surprise.
	DeleteAfter time.Duration
	// RetainDays keeps that many UTC calendar days of archive, counting today, and
	// reclaims fully ingested files from before that. 1 means "today only, yesterday
	// and older go". Zero disables it, same as DeleteAfter.
	//
	// Separate from DeleteAfter rather than expressed as one, because a rolling
	// duration and a calendar boundary are genuinely different things: "24h" at
	// 09:00 keeps half of yesterday, which is not what "delete yesterday" means. The
	// two compose - either rule firing is enough to reclaim a file.
	RetainDays int
	// Logger receives operational events. Defaults to slog.Default().
	Logger *slog.Logger

	// now is the clock, injectable so the calendar arithmetic in reclaim can be
	// tested at a chosen instant instead of only at whatever time the suite runs.
	now func() time.Time
}

func (c *Config) setDefaults() {
	if c.ChunkSize <= 0 {
		c.ChunkSize = 8 << 20
	}
	if c.PollInterval <= 0 {
		c.PollInterval = 5 * time.Second
	}
	if c.EvictAfter <= 0 {
		c.EvictAfter = 30 * time.Minute
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.now == nil {
		c.now = time.Now
	}
}

// Watcher follows the archive directory and produces turns.
type Watcher struct {
	cfg     Config
	reducer *turn.Reducer
	cp      Checkpoint
	log     *slog.Logger

	// watermark is the newest record timestamp seen. Eviction is measured against
	// this, not the wall clock, so catching up on a backlog does not look stale.
	watermark time.Time

	// currentFile is the live archive file - the last one in scan()'s chronological
	// order, same file reclaim() already treats as "never delete this one". Empty
	// before the first scan or once the directory is empty.
	currentFile string

	// reducerStateEntries is [len(Prev), len(Seq)] as of the last checkpoint save -
	// see turn.State's own doc comment for what those two maps hold. Recomputed in
	// save() rather than on every read, since Snapshot() is already called there.
	reducerStateEntries [2]int

	// mu guards every field above plus reducer against a self-observability read
	// (internal/selfobs, via Progress) running on a different goroutine than the one
	// driving Poll/Run. This package is otherwise single-goroutine by design (Poll's
	// own doc comment) and stays that way - mu exists ONLY to make Progress safe, not
	// to make Watcher generally concurrent. It matters for more than tidiness:
	// turn.Reducer is explicitly "not safe for concurrent use", and Poll mutates the
	// very map Open() reads the length of - without this lock a metrics-collection
	// tick landing mid-Poll would be a concurrent map read racing a map write, which
	// the Go runtime is entitled to crash the process over, not merely something
	// -race would flag.
	mu sync.RWMutex

	// Stats are cumulative counters for self-observability.
	Stats Stats
}

// Stats are the watcher's own operational counters.
type Stats struct {
	FilesSeen     int64
	FilesDeleted  int64
	FilesReplaced int64
	BytesRead     int64
	MembersRead   int64
	FramesRead    int64
	TurnsEmitted  int64
	DecodeErrors  int64
	ParseErrors   int64
	EvictedOpen   int64
	CheckpointErr int64
	// PartialMemberReads counts a decode pass that found a gzip member cut off
	// mid-write at the tail of the buffer - the NORMAL steady state of tailing a file
	// codex-lb is still appending to (see internal/archive's own package doc), not an
	// error. Deliberately a separate counter from DecodeErrors, which fires only on a
	// complete-but-corrupt member - see readFile's own comment on why the two can
	// never be conflated here.
	PartialMemberReads int64
}

// Progress is a point-in-time, lock-consistent view of the watcher's own operational
// state, for self-observability (issue #8). internal/selfobs is the one caller
// expected to invoke this from a goroutine other than the one driving Poll/Run - see
// Watcher's own doc comment on mu for why that is safe here and nowhere else in this
// package.
type Progress struct {
	Stats Stats
	// Watermark is the newest record timestamp seen - the archive's own clock, never
	// time.Now(). Poll measures eviction against this, deliberately never against wall
	// clock; see Poll's own comment. A caller building an ingest-lag metric performs
	// the one wall-clock subtraction against this value itself (internal/selfobs).
	Watermark time.Time
	// CurrentFile is the live archive file, and CurrentFileOffset how far the
	// checkpoint has advanced into it. Empty/zero before the first scan.
	CurrentFile       string
	CurrentFileOffset int64
	// OpenResponses is turn.Reducer.Open() - see its own doc comment: "a steadily
	// growing value means responses are never completing, which is worth alerting on."
	OpenResponses int
	// ReducerSeriesCount and ReducerThreadCount are the sizes of the two maps
	// turn.State persists across a restart as of the last checkpoint save. Neither is
	// ever pruned (reducer.go's own comment on why in-flight responses are dropped on
	// restart but these are not), so unbounded growth here is the persisted-state
	// twin of the OpenResponses leak.
	ReducerSeriesCount int
	ReducerThreadCount int
}

// Progress reports a consistent snapshot of the watcher's own state. Safe to call
// concurrently with Poll/Run - see the mu field's own doc comment for why that
// safety is not free and not the default anywhere else in this package.
func (w *Watcher) Progress() Progress {
	w.mu.RLock()
	defer w.mu.RUnlock()
	var offset int64
	if w.currentFile != "" {
		offset = w.cp.Files[w.currentFile].Offset
	}
	return Progress{
		Stats:              w.Stats,
		Watermark:          w.watermark,
		CurrentFile:        w.currentFile,
		CurrentFileOffset:  offset,
		OpenResponses:      w.reducer.Open(),
		ReducerSeriesCount: w.reducerStateEntries[0],
		ReducerThreadCount: w.reducerStateEntries[1],
	}
}

// New builds a Watcher, restoring reducer state and offsets from the checkpoint.
func New(cfg Config, r *turn.Reducer) (*Watcher, error) {
	cfg.setDefaults()
	cp, err := LoadCheckpoint(cfg.CheckpointPath)
	if err != nil {
		return nil, err
	}
	r.Restore(cp.Reducer)
	return &Watcher{cfg: cfg, reducer: r, cp: cp, log: cfg.Logger}, nil
}

// Emit receives each batch of completed turns. Returning an error aborts the pass
// WITHOUT advancing the checkpoint, so the same frames are retried next time. That
// is the only backpressure mechanism here, and it is why the checkpoint is saved
// after a successful emit rather than before.
type Emit func(context.Context, []*turn.Turn) error

// Run polls until ctx is cancelled.
func (w *Watcher) Run(ctx context.Context, emit Emit) error {
	t := time.NewTicker(w.cfg.PollInterval)
	defer t.Stop()

	for {
		if err := w.Poll(ctx, emit); err != nil && !errors.Is(err, context.Canceled) {
			// A failed pass is not fatal: the checkpoint was not advanced, so the
			// next tick retries the same bytes. Log and keep going rather than
			// taking the process down over a transient sink failure.
			w.log.Error("poll failed", "err", err)
		}
		select {
		case <-ctx.Done():
			// Final flush so in-flight responses are not lost on a clean shutdown.
			return w.shutdown(context.WithoutCancel(ctx), emit)
		case <-t.C:
		}
	}
}

func (w *Watcher) shutdown(ctx context.Context, emit Emit) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	pending := w.reducer.Flush()
	w.Stats.TurnsEmitted += int64(len(pending))
	if len(pending) > 0 {
		if err := emit(ctx, pending); err != nil {
			return err
		}
	}
	return w.save()
}

// Poll performs one pass: read what is new, emit it, checkpoint, then reclaim.
func (w *Watcher) Poll(ctx context.Context, emit Emit) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	files, err := w.scan()
	if err != nil {
		return err
	}

	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := w.readFile(ctx, f, emit); err != nil {
			return fmt.Errorf("read %s: %w", filepath.Base(f), err)
		}
	}

	// Evict responses that will never complete, measured against the newest record
	// SEEN rather than the wall clock. Those differ whenever we are catching up on a
	// backlog, and using the wall clock there evicts every in-flight response on the
	// first pass simply because the archive is a few hours old - the completions are
	// right there in the next chunk.
	if !w.watermark.IsZero() {
		if stale := w.reducer.Evict(w.watermark.Add(-w.cfg.EvictAfter)); len(stale) > 0 {
			w.Stats.EvictedOpen += int64(len(stale))
			w.Stats.TurnsEmitted += int64(len(stale))
			w.log.Info("evicted in-flight responses that never completed",
				"count", len(stale), "watermark", w.watermark)
			if err := emit(ctx, stale); err != nil {
				return err
			}
		}
	}

	if err := w.save(); err != nil {
		return err
	}
	w.reclaim(files)
	return nil
}

// scan lists archive files in chronological order. The naming scheme sorts
// lexicographically into time order, so no stat calls are needed to order them.
func (w *Watcher) scan() ([]string, error) {
	files, err := filepath.Glob(filepath.Join(w.cfg.Dir, "*.jsonl.gz"))
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", w.cfg.Dir, err)
	}
	sort.Strings(files)
	w.Stats.FilesSeen = int64(len(files))
	if len(files) > 0 {
		// The live file: sorted chronologically, this is always the one codex-lb is
		// still appending to - the same rule reclaim() below already relies on to
		// never delete it.
		w.currentFile = filepath.Base(files[len(files)-1])
	} else {
		w.currentFile = ""
	}
	return files, nil
}

// readFile consumes whatever has been appended since the last pass.
func (w *Watcher) readFile(ctx context.Context, path string, emit Emit) error {
	name := filepath.Base(path)
	st := w.cp.Files[name]
	if st.Deleted {
		return nil
	}

	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // rotated away between scan and stat
		}
		return err
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	// Identify the file by content, not by path. codex-lb recreates the archive at the
	// same name whenever the previous one is moved away, so a familiar name can be a
	// completely different file - and applying the old offset to it would resume at an
	// arbitrary point in an unrelated gzip stream.
	fp, err := fingerprint(f)
	if err != nil {
		return err
	}
	if st.Fingerprint != "" && fp != "" && fp != st.Fingerprint {
		w.log.Warn("archive file was replaced at the same path; restarting it",
			"file", name, "old_offset", st.Offset, "new_size", fi.Size())
		w.Stats.FilesReplaced++
		st = FileState{}
	} else if fi.Size() < st.Offset {
		// Same file, but shorter than where we were. Only truncation explains that,
		// and codex-lb never truncates, so say so rather than pretending it is normal.
		w.log.Warn("archive file shrank; restarting it",
			"file", name, "was", st.Offset, "now", fi.Size())
		w.Stats.FilesReplaced++
		st = FileState{}
	}
	st.Fingerprint = fp

	if fi.Size() == st.Offset {
		w.cp.Files[name] = st
		return nil
	}
	if _, err := f.Seek(st.Offset, io.SeekStart); err != nil {
		return err
	}

	buf := make([]byte, 0, w.cfg.ChunkSize)
	tmp := make([]byte, w.cfg.ChunkSize)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, rerr := f.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			res, derr := archive.DecodeMembers(buf)
			if derr != nil {
				// A corrupt member cannot be skipped safely - member boundaries are
				// only discoverable by decoding - so stop this file here and keep the
				// offset. Later appends still land, and the gap is visible.
				w.Stats.DecodeErrors++
				w.log.Error("corrupt gzip member; abandoning rest of file",
					"file", name, "offset", st.Offset, "err", derr)
				break
			}
			if res.Consumed > 0 {
				turns, perr := w.reduce(res.Data)
				w.Stats.ParseErrors += perr
				w.Stats.MembersRead += int64(res.Members)
				w.Stats.BytesRead += int64(res.Consumed)
				if len(turns) > 0 {
					w.Stats.TurnsEmitted += int64(len(turns))
					if err := emit(ctx, turns); err != nil {
						return err
					}
				}
				// Only advance past bytes whose turns were accepted by the sink.
				st.Offset += int64(res.Consumed)
				buf = append(buf[:0], buf[res.Consumed:]...)
			}
			// A partial member left sitting in buf here is the normal steady state of
			// tailing a file codex-lb is still appending to (archive package's own doc
			// comment) - not an error. Counted and logged separately from DecodeErrors
			// above (issue #8's acceptance criterion: the two must never be conflated)
			// so a dashboard or a log line can tell "waiting on the writer" apart from
			// "the archive is corrupt". This fires on essentially every poll of an
			// actively-written file - that volume IS the signal that ingestion is
			// keeping up, not a fault.
			if len(buf) > 0 {
				w.Stats.PartialMemberReads++
				w.log.Debug("partial gzip member at tail; resuming next pass",
					"file", name, "offset", st.Offset, "buffered_bytes", len(buf))
			}
		}
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) {
				return rerr
			}
			break
		}
	}

	st.Size = fi.Size()
	w.cp.Files[name] = st
	return nil
}

// reduce turns decoded JSONL into turns, tolerating individual bad records.
func (w *Watcher) reduce(data []byte) ([]*turn.Turn, int64) {
	var out []*turn.Turn
	var parseErrs int64
	err := frame.Lines(data, func(rec *frame.Record) error {
		w.Stats.FramesRead++
		if rec.Timestamp.After(w.watermark) {
			w.watermark = rec.Timestamp
		}
		done, err := w.reducer.Add(rec)
		if err != nil {
			parseErrs++
			return nil // one bad record must not abandon the batch
		}
		if done != nil {
			out = append(out, done)
		}
		return nil
	})
	if err != nil {
		// Lines stops at the first undecodable record. The rest of this chunk is
		// lost, but the offset still advances past it: retrying would loop forever.
		parseErrs++
	}
	return out, parseErrs
}

func (w *Watcher) save() error {
	st := w.reducer.Snapshot()
	w.cp.Reducer = st
	// See Progress/Watcher.reducerStateEntries: the size of the two maps turn.State
	// persists, refreshed here since Snapshot() is already being called.
	w.reducerStateEntries = [2]int{len(st.Prev), len(st.Seq)}
	if err := w.cp.Save(w.cfg.CheckpointPath); err != nil {
		w.Stats.CheckpointErr++
		return err
	}
	return nil
}

// archiveDayLayout is the date prefix of an archive filename. codex-lb builds the
// name as datetime.now(UTC).strftime('%Y-%m-%dT%H') - read from its source on camden,
// not inferred - so the date in the name is a UTC calendar day and RetainDays has to
// do its arithmetic in UTC to match. Using the local day instead would be invisible
// in Britain for half the year and would quietly delete the current day's early files
// anywhere west of Greenwich.
const archiveDayLayout = "2006-01-02"

// archiveDay reads the UTC calendar day out of an archive filename. A name that does
// not parse returns false and is never reclaimed by the RetainDays rule - an
// unrecognised file in the archive directory is not something to delete on a guess.
func archiveDay(name string) (time.Time, bool) {
	if len(name) < len(archiveDayLayout) {
		return time.Time{}, false
	}
	day, err := time.ParseInLocation(archiveDayLayout, name[:len(archiveDayLayout)], time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	return day, true
}

// reclaim deletes archive files that have been fully ingested.
//
// Two conditions are always required, whichever retention rule fires. The file must
// be fully consumed - so a restart that comes up behind a backlog reads the backlog
// before deleting any of it, rather than dropping data it never shipped. And it must
// not be the newest file, which is the one codex-lb is currently appending to, where
// offset equalling size only means we have caught up, not that it is finished.
//
// On top of those, at least one retention rule must select it:
//
//   - RetainDays: the file's own UTC calendar day is older than the retained window.
//   - DeleteAfter: the file's mtime is older than the operator's margin.
//
// Neither set means nothing is ever deleted, which is the default.
func (w *Watcher) reclaim(files []string) {
	if len(files) < 2 || (w.cfg.DeleteAfter <= 0 && w.cfg.RetainDays <= 0) {
		return
	}
	now := w.cfg.now()
	cutoff := now.Add(-w.cfg.DeleteAfter)

	// The oldest UTC day to keep. RetainDays counts today, so 1 keeps today alone.
	var keepFrom time.Time
	if w.cfg.RetainDays > 0 {
		y, m, d := now.UTC().Date()
		keepFrom = time.Date(y, m, d, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -(w.cfg.RetainDays - 1))
	}

	// Never the last entry: sorted chronologically, that is the live file.
	for _, path := range files[:len(files)-1] {
		name := filepath.Base(path)
		st, ok := w.cp.Files[name]
		if !ok || st.Deleted || st.Offset < st.Size || st.Size == 0 {
			continue
		}

		reason := ""
		if w.cfg.RetainDays > 0 {
			if day, parsed := archiveDay(name); parsed && day.Before(keepFrom) {
				reason = "retain_days"
			}
		}
		if reason == "" && w.cfg.DeleteAfter > 0 {
			fi, err := os.Stat(path)
			if err == nil && !fi.ModTime().After(cutoff) {
				reason = "delete_after"
			}
		}
		if reason == "" {
			continue
		}

		if err := os.Remove(path); err != nil {
			w.log.Error("could not delete ingested archive", "file", name, "err", err)
			continue
		}
		st.Deleted = true
		w.cp.Files[name] = st
		w.Stats.FilesDeleted++
		w.log.Info("deleted fully ingested archive", "file", name, "bytes", st.Size, "reason", reason)
	}
	if err := w.save(); err != nil {
		w.log.Error("checkpoint save after reclaim failed", "err", err)
	}
}
