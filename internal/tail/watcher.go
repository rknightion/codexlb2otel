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
	// Logger receives operational events. Defaults to slog.Default().
	Logger *slog.Logger
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

	// Stats are cumulative counters for self-observability.
	Stats Stats
}

// Stats are the watcher's own operational counters.
type Stats struct {
	FilesSeen     int64
	FilesDeleted  int64
	BytesRead     int64
	MembersRead   int64
	FramesRead    int64
	TurnsEmitted  int64
	DecodeErrors  int64
	ParseErrors   int64
	EvictedOpen   int64
	CheckpointErr int64
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

	// A file smaller than our offset was replaced or truncated. Re-reading from the
	// start would duplicate; the honest response is to restart it and say so, since
	// codex-lb only ever appends and this means something unexpected happened.
	if fi.Size() < st.Offset {
		w.log.Warn("archive file shrank; restarting it",
			"file", name, "was", st.Offset, "now", fi.Size())
		st = FileState{}
	}
	if fi.Size() == st.Offset {
		return nil
	}

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
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
	w.cp.Reducer = w.reducer.Snapshot()
	if err := w.cp.Save(w.cfg.CheckpointPath); err != nil {
		w.Stats.CheckpointErr++
		return err
	}
	return nil
}

// reclaim deletes archive files that have been fully ingested.
//
// Three conditions, all required. The file must be fully consumed. It must not be
// the newest file, which is the one codex-lb is currently appending to - offset
// equalling size there only means we have caught up, not that it is finished. And
// it must be older than DeleteAfter, which is the operator's margin for pulling a
// copy before it goes.
func (w *Watcher) reclaim(files []string) {
	if w.cfg.DeleteAfter <= 0 || len(files) < 2 {
		return
	}
	cutoff := time.Now().Add(-w.cfg.DeleteAfter)

	// Never the last entry: sorted chronologically, that is the live file.
	for _, path := range files[:len(files)-1] {
		name := filepath.Base(path)
		st, ok := w.cp.Files[name]
		if !ok || st.Deleted || st.Offset < st.Size || st.Size == 0 {
			continue
		}
		fi, err := os.Stat(path)
		if err != nil || fi.ModTime().After(cutoff) {
			continue
		}
		if err := os.Remove(path); err != nil {
			w.log.Error("could not delete ingested archive", "file", name, "err", err)
			continue
		}
		st.Deleted = true
		w.cp.Files[name] = st
		w.Stats.FilesDeleted++
		w.log.Info("deleted fully ingested archive",
			"file", name, "bytes", st.Size, "age", time.Since(fi.ModTime()).Round(time.Minute))
	}
	if err := w.save(); err != nil {
		w.log.Error("checkpoint save after reclaim failed", "err", err)
	}
}
