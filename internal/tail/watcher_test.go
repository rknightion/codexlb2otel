package tail

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rknightion/codexlb2otel/internal/archive"
	"github.com/rknightion/codexlb2otel/internal/fixture"
	"github.com/rknightion/codexlb2otel/internal/frame"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

// Fixtures are discovered, not named - see internal/fixture. corpusFile(1) is a
// DIFFERENT capture from corpusFile(0), which is what the same-path replacement
// tests need: an unrelated archive landing at a filename we already hold an offset
// for. That collision is not hypothetical; two distinct captures both named
// 2026-08-06T18.jsonl.gz exist in the corpus today.
// Indexed into the size-ordered list rather than fixture.Any, which reorders its
// selection chronologically: these tests need two files that are DIFFERENT, and
// nothing about when they were captured.
func corpusFile(t *testing.T, i int) []byte {
	t.Helper()
	files := fixture.Files(t)
	if len(files) <= i {
		t.Fatalf("corpus has %d archives, test needs %d", len(files), i+1)
	}
	return fixture.Load(t, files[i])
}

func loadFixture(t *testing.T) []byte { return corpusFile(t, 0) }

// archiveName is the name the temp-dir archive is written under. Only its shape
// matters - the watcher keys its checkpoint by path, not by content of the name.
const archiveName = "2026-08-06T21.jsonl.gz"

func newWatcher(t *testing.T, dir string, cfg func(*Config)) *Watcher {
	t.Helper()
	c := Config{
		Dir:            dir,
		CheckpointPath: filepath.Join(dir, "state", "checkpoint.json"),
		PollInterval:   time.Hour, // Poll is driven directly in tests
		ChunkSize:      1 << 20,
	}
	if cfg != nil {
		cfg(&c)
	}
	w, err := New(c, turn.New())
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func collect(turns *[]*turn.Turn) Emit {
	return func(_ context.Context, batch []*turn.Turn) error {
		*turns = append(*turns, batch...)
		return nil
	}
}

// Tailing a file as it is appended must produce exactly what reading it once
// produces - no duplicates from re-reading, and nothing lost at a chunk boundary.
// The append sizes deliberately land mid-gzip-member, which is the case that breaks
// naive offset tracking.
func TestWatcher_IncrementalAppendMatchesSinglePass(t *testing.T) {
	data := loadFixture(t)
	dir := t.TempDir()
	path := filepath.Join(dir, archiveName)

	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var got []*turn.Turn
	w := newWatcher(t, dir, nil)
	ctx := context.Background()

	// Sizes are coprime-ish and unaligned to any member boundary on purpose.
	for off := 0; off < len(data); {
		end := off + 997_003
		if end > len(data) {
			end = len(data)
		}
		if _, err := f.Write(data[off:end]); err != nil {
			t.Fatal(err)
		}
		if err := w.Poll(ctx, collect(&got)); err != nil {
			t.Fatal(err)
		}
		off = end
	}
	got = append(got, w.reducer.Flush()...)

	want := singlePass(t, data)
	if len(got) != len(want) {
		t.Errorf("tailed %d turns, single pass produced %d", len(got), len(want))
	}

	seen := map[string]int{}
	for _, x := range got {
		if x.RequestID != "" {
			seen[x.RequestID]++
		}
	}
	for id, n := range seen {
		if n > 1 {
			t.Errorf("request %s emitted %d times; offsets are being replayed", id, n)
		}
	}
}

func singlePass(t *testing.T, data []byte) []*turn.Turn {
	t.Helper()
	res, err := archive.DecodeMembers(data)
	if err != nil {
		t.Fatal(err)
	}
	r := turn.New()
	var out []*turn.Turn
	err = frame.Lines(res.Data, func(rec *frame.Record) error {
		done, err := r.Add(rec)
		if done != nil {
			out = append(out, done)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return append(out, r.Flush()...)
}

// A restart must resume from the checkpoint rather than replaying the file. Shipping
// the same turns twice is the failure this guards: Loki deduplicates poorly and OTLP
// counters would double-count outright.
func TestWatcher_ResumesWithoutReplaying(t *testing.T) {
	data := loadFixture(t)
	dir := t.TempDir()
	path := filepath.Join(dir, archiveName)
	half := len(data) / 2

	if err := os.WriteFile(path, data[:half], 0o644); err != nil {
		t.Fatal(err)
	}
	var first []*turn.Turn
	w1 := newWatcher(t, dir, nil)
	if err := w1.Poll(context.Background(), collect(&first)); err != nil {
		t.Fatal(err)
	}
	if len(first) == 0 {
		t.Fatal("first pass produced no turns")
	}

	// Append the rest, then start a completely fresh watcher over the same state.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(data[half:]); err != nil {
		t.Fatal(err)
	}
	f.Close()

	var second []*turn.Turn
	w2 := newWatcher(t, dir, nil)
	if err := w2.Poll(context.Background(), collect(&second)); err != nil {
		t.Fatal(err)
	}

	firstIDs := map[string]bool{}
	for _, x := range first {
		firstIDs[x.RequestID] = true
	}
	for _, x := range second {
		if x.RequestID != "" && firstIDs[x.RequestID] {
			t.Fatalf("request %s re-emitted after restart", x.RequestID)
		}
	}
	if len(second) == 0 {
		t.Error("resumed watcher produced nothing from the appended half")
	}
	t.Logf("%d turns before restart, %d after, no overlap", len(first), len(second))
}

// Reclaim must never touch the newest file: that is the one codex-lb is still
// appending to, where offset==size only means we have caught up.
func TestWatcher_ReclaimSparesTheLiveFile(t *testing.T) {
	data := loadFixture(t)
	dir := t.TempDir()
	older := filepath.Join(dir, "2026-08-06T20.jsonl.gz")
	live := filepath.Join(dir, "2026-08-06T21.jsonl.gz")
	for _, p := range []string{older, live} {
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Backdate both well past the retention window, so only the live-file rule can
	// be what saves the newest one.
	old := time.Now().Add(-48 * time.Hour)
	for _, p := range []string{older, live} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}

	var got []*turn.Turn
	w := newWatcher(t, dir, func(c *Config) { c.DeleteAfter = time.Hour })
	if err := w.Poll(context.Background(), collect(&got)); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(older); !os.IsNotExist(err) {
		t.Errorf("fully ingested older file was not reclaimed (err=%v)", err)
	}
	if _, err := os.Stat(live); err != nil {
		t.Errorf("newest file must never be deleted: %v", err)
	}
	if w.Stats.FilesDeleted != 1 {
		t.Errorf("FilesDeleted = %d, want 1", w.Stats.FilesDeleted)
	}
}

// Deletion is opt-in. A zero DeleteAfter must leave everything on disk: this removes
// the only copy of the raw capture, so it must never happen by default.
func TestWatcher_ReclaimIsOptIn(t *testing.T) {
	data := loadFixture(t)
	dir := t.TempDir()
	for _, n := range []string{"2026-08-06T20.jsonl.gz", "2026-08-06T21.jsonl.gz"} {
		if err := os.WriteFile(filepath.Join(dir, n), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var got []*turn.Turn
	w := newWatcher(t, dir, nil) // DeleteAfter unset
	if err := w.Poll(context.Background(), collect(&got)); err != nil {
		t.Fatal(err)
	}
	if w.Stats.FilesDeleted != 0 {
		t.Errorf("deleted %d files with retention disabled", w.Stats.FilesDeleted)
	}
	entries, _ := filepath.Glob(filepath.Join(dir, "*.jsonl.gz"))
	if len(entries) != 2 {
		t.Errorf("%d archive files remain, want 2", len(entries))
	}
}

// A sink failure must not advance the checkpoint, or the rejected turns are lost.
func TestWatcher_FailedEmitDoesNotAdvanceCheckpoint(t *testing.T) {
	data := loadFixture(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, archiveName), data, 0o644); err != nil {
		t.Fatal(err)
	}

	w := newWatcher(t, dir, nil)
	boom := func(context.Context, []*turn.Turn) error { return os.ErrClosed }
	if err := w.Poll(context.Background(), boom); err == nil {
		t.Fatal("expected the sink failure to surface")
	}

	cp, err := LoadCheckpoint(w.cfg.CheckpointPath)
	if err != nil {
		t.Fatal(err)
	}
	if st := cp.Files[archiveName]; st.Offset != 0 {
		t.Errorf("checkpoint advanced to %d despite the sink failing", st.Offset)
	}
}

// codex-lb reopens the archive with O_APPEND|O_CREAT for every batch, so moving a file
// away makes it recreate the same name from scratch. Live proof: 2026-08-06T18 exists
// as two entirely different files, 18:00-18:27 and 18:43-18:52.
//
// Detecting that by size alone only works while the replacement is still SMALLER than
// the old offset. This test replaces the file with a LARGER one, which the size check
// cannot see: it would seek the stale offset into an unrelated gzip stream and either
// error or, worse, decode garbage.
func TestWatcher_DetectsReplacementAtTheSamePath(t *testing.T) {
	data := loadFixture(t)
	if len(data) < 4096 {
		t.Fatalf("smallest corpus archive is %d bytes; too small to split into generations", len(data))
	}
	dir := t.TempDir()
	path := filepath.Join(dir, archiveName)

	// First generation: a short prefix, fully consumed.
	head := data[:len(data)/4]
	if err := os.WriteFile(path, head, 0o600); err != nil {
		t.Fatal(err)
	}
	var first []*turn.Turn
	w := newWatcher(t, dir, nil)
	if err := w.Poll(context.Background(), collect(&first)); err != nil {
		t.Fatal(err)
	}
	consumed := w.cp.Files[archiveName].Offset
	if consumed == 0 {
		t.Fatal("nothing consumed from the first generation")
	}

	// Second generation: the same path, a genuinely different archive, and LARGER than
	// the offset we hold - so file size gives no hint that anything changed. This is
	// the real 2026-08-06T18 case, where one filename held two unrelated captures.
	replacement := corpusFile(t, 1)
	// fixture.Any is smallest-first, so the replacement is at least as large as the
	// file whose quarter-prefix we consumed. If that ever stops holding, the test is
	// no longer exercising the larger-replacement case it was written for.
	if int64(len(replacement)) <= consumed {
		t.Fatalf("replacement is %d bytes, not larger than the %d consumed - this test "+
			"only proves anything when size gives no hint of the swap", len(replacement), consumed)
	}
	if err := os.WriteFile(path, replacement, 0o600); err != nil {
		t.Fatal(err)
	}

	var second []*turn.Turn
	if err := w.Poll(context.Background(), collect(&second)); err != nil {
		t.Fatalf("poll after replacement: %v", err)
	}
	if w.Stats.FilesReplaced != 1 {
		t.Errorf("FilesReplaced = %d, want 1 - the replacement went unnoticed",
			w.Stats.FilesReplaced)
	}
	// Restarted from zero, so everything decodable in the new file was read.
	if got := w.cp.Files[archiveName].Offset; got == 0 || got <= consumed {
		t.Errorf("offset after replacement = %d (was %d); the file was not restarted",
			got, consumed)
	}
	if len(second) == 0 {
		t.Error("no turns from the replacement file")
	}
}

// A file that is merely appended to must NOT be treated as replaced, or every poll
// would re-ship the whole archive.
func TestWatcher_AppendIsNotAReplacement(t *testing.T) {
	data := loadFixture(t)
	dir := t.TempDir()
	path := filepath.Join(dir, archiveName)
	if err := os.WriteFile(path, data[:len(data)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	var turns []*turn.Turn
	w := newWatcher(t, dir, nil)
	if err := w.Poll(context.Background(), collect(&turns)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := w.Poll(context.Background(), collect(&turns)); err != nil {
		t.Fatal(err)
	}
	if w.Stats.FilesReplaced != 0 {
		t.Errorf("FilesReplaced = %d on a plain append", w.Stats.FilesReplaced)
	}
}
