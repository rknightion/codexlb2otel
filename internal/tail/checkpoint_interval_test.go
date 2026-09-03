package tail

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rknightion/codexlb2otel/internal/turn"
)

// noEmit is a sink that accepts everything and keeps nothing.
func noEmit(context.Context, []*turn.Turn) error { return nil }

// newTestWatcher builds a watcher over an empty archive dir with a controllable clock.
func newTestWatcher(t *testing.T, interval time.Duration, clock *time.Time) *Watcher {
	t.Helper()
	dir := t.TempDir()
	w, err := New(Config{
		Dir:                dir,
		CheckpointPath:     filepath.Join(dir, "checkpoint.json"),
		CheckpointInterval: interval,
		now:                func() time.Time { return *clock },
	}, turn.New())
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func mtime(t *testing.T, path string) (time.Time, bool) {
	t.Helper()
	fi, err := os.Stat(path)
	if os.IsNotExist(err) {
		return time.Time{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return fi.ModTime(), true
}

// Saving on every poll is what this replaces: at a 5s poll and a ~600 KB checkpoint that
// was ~10 GB/day of writes and fsyncs on camden, to persist state that mostly had not
// changed.
func TestPoll_DoesNotCheckpointOnEveryPass(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	w := newTestWatcher(t, 15*time.Minute, &now)
	ctx := context.Background()

	// First poll establishes the checkpoint.
	if err := w.Poll(ctx, noEmit); err != nil {
		t.Fatal(err)
	}
	if w.Stats.CheckpointSaves != 1 {
		t.Fatalf("first poll wrote %d checkpoints, want 1", w.Stats.CheckpointSaves)
	}

	// Twelve more polls a few seconds apart, well inside the interval.
	for range 12 {
		now = now.Add(5 * time.Second)
		if err := w.Poll(ctx, noEmit); err != nil {
			t.Fatal(err)
		}
	}
	if w.Stats.CheckpointSaves != 1 {
		t.Errorf("polls inside the interval wrote %d checkpoints, want the original 1",
			w.Stats.CheckpointSaves)
	}
}

// The interval must actually elapse rather than the save being skipped forever.
func TestPoll_CheckpointsOnceTheIntervalElapses(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	w := newTestWatcher(t, 15*time.Minute, &now)
	ctx := context.Background()

	if err := w.Poll(ctx, noEmit); err != nil {
		t.Fatal(err)
	}
	before := w.Stats.CheckpointSaves

	now = now.Add(16 * time.Minute)
	if err := w.Poll(ctx, noEmit); err != nil {
		t.Fatal(err)
	}

	// Nothing changed, so the content check should still suppress the WRITE - but the
	// due-check must have fired, which is what moves lastSaveAt.
	if !w.lastSaveAt.Equal(now) {
		t.Errorf("lastSaveAt = %s, want it advanced to %s", w.lastSaveAt, now)
	}
	if w.Stats.CheckpointSaves != before {
		t.Errorf("wrote %d checkpoints for unchanged state, want the original %d",
			w.Stats.CheckpointSaves, before)
	}
}

// An unchanged render must not be written. An idle host should cost no fsyncs at all,
// however long it idles.
func TestSave_SkipsAnUnchangedCheckpoint(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	w := newTestWatcher(t, time.Nanosecond, &now) // always due
	ctx := context.Background()

	if err := w.Poll(ctx, noEmit); err != nil {
		t.Fatal(err)
	}
	first, ok := mtime(t, w.cfg.CheckpointPath)
	if !ok {
		t.Fatal("no checkpoint was written at all")
	}
	saves := w.Stats.CheckpointSaves

	for range 5 {
		now = now.Add(time.Hour)
		if err := w.Poll(ctx, noEmit); err != nil {
			t.Fatal(err)
		}
	}

	if w.Stats.CheckpointSaves != saves {
		t.Errorf("wrote %d checkpoints for unchanged state, want the original %d",
			w.Stats.CheckpointSaves, saves)
	}
	if second, _ := mtime(t, w.cfg.CheckpointPath); !second.Equal(first) {
		t.Errorf("checkpoint file was rewritten despite unchanged content (%s -> %s)", first, second)
	}
}

// The failsafe that makes the interval acceptable: the ordinary exit is a clean SIGTERM,
// and that path must checkpoint whatever the interval says.
func TestShutdown_ForcesACheckpointWhateverTheInterval(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	w := newTestWatcher(t, 24*time.Hour, &now) // never due within the test
	ctx := context.Background()

	if _, ok := mtime(t, w.cfg.CheckpointPath); ok {
		t.Fatal("checkpoint existed before anything ran")
	}
	if err := w.shutdown(ctx, noEmit); err != nil {
		t.Fatal(err)
	}
	if _, ok := mtime(t, w.cfg.CheckpointPath); !ok {
		t.Error("a clean shutdown did not write a checkpoint")
	}
}

// render must not stamp Updated, or every render differs from the last and the
// unchanged-content check never fires - which would silently restore the old behaviour.
func TestRender_IsStableForUnchangedState(t *testing.T) {
	c := Checkpoint{Version: checkpointVersion, Files: map[string]FileState{"a": {Offset: 1}}}

	first, err := c.render()
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.render()
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("two renders of unchanged state differ:\n%s\n%s", first, second)
	}
}

// Save still stamps Updated, so the file records when it was last actually written.
func TestSave_StampsUpdated(t *testing.T) {
	dir := t.TempDir()
	c := Checkpoint{Files: map[string]FileState{}}
	if err := c.Save(filepath.Join(dir, "checkpoint.json")); err != nil {
		t.Fatal(err)
	}
	if c.Updated.IsZero() {
		t.Error("Save did not stamp Updated")
	}
}

func TestLoadCheckpoint_AcceptsVersionOneAndWritesCurrentVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "checkpoint.json")
	raw := []byte(`{"version":1,"files":{"2026-08-10T10.jsonl.gz":{"offset":1,"size":1}}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := LoadCheckpoint(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Version != checkpointVersion {
		t.Fatalf("loaded version = %d, want current %d", c.Version, checkpointVersion)
	}
	if _, ok := c.Files["2026-08-10T10.jsonl.gz"]; !ok {
		t.Fatal("version 1 checkpoint offsets were discarded instead of upgraded")
	}
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	var onDisk Checkpoint
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &onDisk); err != nil {
		t.Fatal(err)
	}
	if onDisk.Version != checkpointVersion {
		t.Fatalf("saved checkpoint version = %d, want %d", onDisk.Version, checkpointVersion)
	}
}

func TestCheckpoint_PrunesOldFileTombstonesByFilenameDay(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	c := Checkpoint{Files: map[string]FileState{
		"2026-08-06T23.jsonl.gz": {Deleted: true},
		"2026-08-07T00.jsonl.gz": {Deleted: true},
		"2026-08-06T22.jsonl.gz": {Offset: 1, Size: 1},
		"not-an-archive-name":    {Deleted: true},
	}}

	if got := c.PruneFileTombstones(now); got != 1 {
		t.Fatalf("pruned %d tombstones, want 1", got)
	}
	if _, ok := c.Files["2026-08-06T23.jsonl.gz"]; ok {
		t.Fatal("tombstone with filename day more than three days old survived")
	}
	for _, keep := range []string{"2026-08-07T00.jsonl.gz", "2026-08-06T22.jsonl.gz", "not-an-archive-name"} {
		if _, ok := c.Files[keep]; !ok {
			t.Fatalf("%s was pruned but should have been kept", keep)
		}
	}
}
