package tail

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
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

	// Keyed by (request_id, response_id), NOT by request_id alone.
	//
	// One websocket connection carries many responses: the reducer emits a Turn per
	// response.completed and reopens on the next frame with the same request_id, so
	// two Turns sharing a request_id is the normal case and never meant a replay.
	// Asserting on request_id alone passed only for as long as the cheapest archive
	// in the corpus happened not to reuse one - a 2026-08-08 sync of two quiet hours
	// ended that, and the test then reported a data-duplication bug in the ingest
	// path that did not exist (issue #34).
	//
	// The real anti-replay check is the length comparison against singlePass above.
	// This one is the finer-grained version of it, kept because it names WHICH record
	// was duplicated rather than only that the counts differ.
	seen := map[[2]string]int{}
	for _, x := range got {
		if x.RequestID == "" {
			continue
		}
		seen[[2]string{x.RequestID, x.ResponseID}]++
	}
	for k, n := range seen {
		if n > 1 {
			t.Errorf("response %s (request %s) emitted %d times; offsets are being replayed",
				k[1], k[0], n)
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

	// (request_id, response_id), for the reason spelled out in the incremental test
	// above: a websocket carries many responses, so the same request_id legitimately
	// appears on both sides of the restart. What must not repeat is a RESPONSE.
	firstIDs := map[[2]string]bool{}
	for _, x := range first {
		firstIDs[[2]string{x.RequestID, x.ResponseID}] = true
	}
	for _, x := range second {
		if x.RequestID != "" && firstIDs[[2]string{x.RequestID, x.ResponseID}] {
			t.Fatalf("response %s (request %s) re-emitted after restart", x.ResponseID, x.RequestID)
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

// RetainDays is a calendar rule, not a rolling one: at 12:00 on the 8th, every file
// dated the 7th goes and every file dated the 8th stays, regardless of how many hours
// old any of them is.
func TestWatcher_RetainDaysDropsPriorDays(t *testing.T) {
	data := loadFixture(t)
	dir := t.TempDir()
	names := []string{
		"2026-08-06T09.jsonl.gz", // two days back
		"2026-08-07T20.jsonl.gz", // yesterday
		"2026-08-08T09.jsonl.gz", // today
		"2026-08-08T10.jsonl.gz", // today, and the live file
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	var got []*turn.Turn
	w := newWatcher(t, dir, func(c *Config) {
		c.RetainDays = 1
		c.now = func() time.Time { return now }
	})
	if err := w.Poll(context.Background(), collect(&got)); err != nil {
		t.Fatal(err)
	}

	for _, n := range names[:2] {
		if _, err := os.Stat(filepath.Join(dir, n)); !os.IsNotExist(err) {
			t.Errorf("%s is from a prior day and should be gone (err=%v)", n, err)
		}
	}
	for _, n := range names[2:] {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("%s is from today and must be kept: %v", n, err)
		}
	}
	if w.Stats.FilesDeleted != 2 {
		t.Errorf("FilesDeleted = %d, want 2", w.Stats.FilesDeleted)
	}
}

// The day in an archive filename is UTC, because codex-lb builds it from
// datetime.now(UTC). Doing the arithmetic in local time is invisible in Britain for
// half the year and wrong everywhere west of Greenwich, so pin it with a local zone
// whose date differs from UTC's at the chosen instant.
func TestWatcher_RetainDaysIsUTC(t *testing.T) {
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("no tzdata for America/New_York: %v", err)
	}
	saved := time.Local
	time.Local = newYork
	t.Cleanup(func() { time.Local = saved })

	dir := t.TempDir()
	// 00:30 on the 8th in UTC is still 20:30 on the 7th in New York.
	now := time.Date(2026, 8, 8, 0, 30, 0, 0, time.UTC)
	if got := now.In(time.Local).Day(); got != 7 {
		t.Fatalf("test premise broken: local day is %d, want 7", got)
	}

	const stale = "2026-08-07T23.jsonl.gz" // last hour of the prior UTC day
	const live = "2026-08-08T00.jsonl.gz"
	for _, n := range []string{stale, live} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	w := newWatcher(t, dir, func(c *Config) {
		c.RetainDays = 1
		c.now = func() time.Time { return now }
	})
	// Hand-built checkpoint rather than a Poll: this test is about the calendar
	// arithmetic, and the files hold no real archive data to ingest.
	w.cp.Files[stale] = FileState{Offset: 1, Size: 1}
	w.cp.Files[live] = FileState{Offset: 1, Size: 1}
	w.reclaim([]string{filepath.Join(dir, stale), filepath.Join(dir, live)})

	if _, err := os.Stat(filepath.Join(dir, stale)); !os.IsNotExist(err) {
		t.Errorf("%s is the prior UTC day and should be gone; local-time arithmetic would keep it (err=%v)", stale, err)
	}
	if _, err := os.Stat(filepath.Join(dir, live)); err != nil {
		t.Errorf("%s is the current UTC day and must be kept: %v", live, err)
	}
}

// Retention must never outrun ingest. A prior-day file that has not been fully read -
// the shape a restart behind a backlog produces - stays until it has been.
func TestWatcher_RetainDaysSparesUnreadFile(t *testing.T) {
	dir := t.TempDir()
	const unread = "2026-08-07T20.jsonl.gz"
	const live = "2026-08-08T10.jsonl.gz"
	for _, n := range []string{unread, live} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("xxxx"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	w := newWatcher(t, dir, func(c *Config) {
		c.RetainDays = 1
		c.now = func() time.Time { return now }
	})
	w.cp.Files[unread] = FileState{Offset: 1, Size: 4} // read one byte of four
	w.cp.Files[live] = FileState{Offset: 4, Size: 4}
	w.reclaim([]string{filepath.Join(dir, unread), filepath.Join(dir, live)})

	if _, err := os.Stat(filepath.Join(dir, unread)); err != nil {
		t.Errorf("a partially read file must survive its calendar day: %v", err)
	}
	if w.Stats.FilesDeleted != 0 {
		t.Errorf("FilesDeleted = %d, want 0", w.Stats.FilesDeleted)
	}
}

// Progress is read by an OTel async-instrument callback that the metrics SDK runs
// from inside ForceFlush - which sinkEmit calls from inside the emit callback, which
// Poll calls while holding the write lock. Reading Progress under mu.RLock there is a
// self-deadlock, and it is not a theoretical one: it took the whole service down on
// its first production run, presenting as an otlpmetric flush timing out at exactly
// 30s (the PeriodicReader's default) on every poll forever, with nothing ingested and
// the checkpoint correctly refusing to advance. Issue #29.
//
// The 10s bound is what makes this a test rather than a hang: the failure mode is
// "never returns", so a plain call would wedge the suite instead of failing it.
func TestWatcher_ProgressIsSafeFromInsideEmit(t *testing.T) {
	data := loadFixture(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, archiveName), data, 0o644); err != nil {
		t.Fatal(err)
	}

	w := newWatcher(t, dir, nil)
	done := make(chan error, 1)
	go func() {
		done <- w.Poll(context.Background(), func(_ context.Context, _ []*turn.Turn) error {
			// Exactly what internal/selfobs does from the SDK's collection callback.
			_ = w.Progress()
			return nil
		})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Poll: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Poll never returned: Progress blocked on the lock Poll itself holds")
	}

	// And the published snapshot is real, not merely non-blocking.
	if got := w.Progress(); got.Stats.TurnsEmitted == 0 {
		t.Error("Progress reports no turns emitted after a successful poll")
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

// buildTimestampedArchive writes one complete gzip member holding a single
// frame.Record whose only load-bearing field is its timestamp. No request_id and no
// event payload are needed: reduce() (called from readFile) advances the watermark
// for every frame it sees, correlated or not, before it ever asks the reducer to do
// anything with it - see reduce's own body. This is the cheapest record shape that
// moves the watermark to an exact, test-controlled value.
func buildTimestampedArchive(t *testing.T, ts time.Time) []byte {
	t.Helper()
	rec := map[string]any{
		"account_id":  "",
		"direction":   "server_to_codex",
		"headers":     map[string]string{},
		"kind":        "frame",
		"method":      "",
		"payload":     map[string]string{"text": ""},
		"request_id":  "",
		"status_code": nil,
		"timestamp":   ts.UTC().Format(time.RFC3339Nano),
		"transport":   "websocket",
		"url":         "",
		"extra":       map[string]any{},
	}
	line, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(line); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestWatcher_IngestLagReflectsArchiveTimeNotWallClock is issue #8's central
// acceptance criterion: ingest_lag_seconds is wall-clock now minus the newest
// record's OWN timestamp, verified against a DELIBERATELY STALE directory - a
// synthetic archive whose one record is stamped hours in the past, independent of
// whenever this test happens to run. If the watermark tracked wall-clock processing
// time instead of the record's own stamp, the lag computed from it would always read
// near zero here, never near the ~6h actually injected - which is exactly the trap
// the issue's Notes section records for EVICTION (never measured against time.Now();
// see TestWatcher_ReclaimSparesTheLiveFile and the package's own Poll comment) and
// which this test pins for the other clock, ingest lag, that deliberately DOES use
// time.Now() - once, at the metrics layer (internal/selfobs), never here.
func TestWatcher_IngestLagReflectsArchiveTimeNotWallClock(t *testing.T) {
	dir := t.TempDir()
	stale := time.Now().Add(-6 * time.Hour).UTC()
	path := filepath.Join(dir, archiveName)
	if err := os.WriteFile(path, buildTimestampedArchive(t, stale), 0o644); err != nil {
		t.Fatal(err)
	}

	w := newWatcher(t, dir, nil)
	var got []*turn.Turn
	if err := w.Poll(context.Background(), collect(&got)); err != nil {
		t.Fatal(err)
	}

	watermark := w.Progress().Watermark
	if watermark.IsZero() {
		t.Fatal("Progress().Watermark is zero after polling a file with a record")
	}
	if diff := watermark.Sub(stale); diff < -time.Second || diff > time.Second {
		t.Errorf("Watermark = %v, want %v (the record's own stamped time)", watermark, stale)
	}

	lag := time.Since(watermark)
	if lag < 5*time.Hour {
		t.Fatalf("lag = %v, want >= ~6h (the archive's OWN staleness) - this looks like the "+
			"watermark tracked wall-clock processing time instead of the record's own "+
			"stamped timestamp", lag)
	}
}

// TestWatcher_PartialMemberIsNotADecodeError is issue #8's other explicit acceptance
// criterion: a gzip member cut off mid-write - the normal state of a file codex-lb is
// still appending to - must be counted separately from genuine corruption.
func TestWatcher_PartialMemberIsNotADecodeError(t *testing.T) {
	data := loadFixture(t)
	dir := t.TempDir()
	path := filepath.Join(dir, archiveName)

	// An arbitrary cut lands mid-member: members run ~1KB apart (archive package's
	// own doc comment), far smaller than a third of any corpus file.
	if err := os.WriteFile(path, data[:len(data)/3], 0o644); err != nil {
		t.Fatal(err)
	}

	w := newWatcher(t, dir, nil)
	var got []*turn.Turn
	if err := w.Poll(context.Background(), collect(&got)); err != nil {
		t.Fatal(err)
	}

	if w.Stats.DecodeErrors != 0 {
		t.Errorf("DecodeErrors = %d, want 0 - a truncated tail is not corruption", w.Stats.DecodeErrors)
	}
	if w.Stats.PartialMemberReads == 0 {
		t.Error("PartialMemberReads = 0, want > 0 - the cut landed mid-member and should have been counted")
	}
}

// TestWatcher_CorruptMemberIsCountedSeparately is the other half of the same
// acceptance criterion: genuine corruption - a complete member whose body was
// scrambled - must increment DecodeErrors and must stop before ever reaching the
// partial-member accounting (readFile's derr branch breaks the read loop), so the two
// counters can never both fire off the same event.
func TestWatcher_CorruptMemberIsCountedSeparately(t *testing.T) {
	good := buildTimestampedArchive(t, time.Now())
	bad := buildTimestampedArchive(t, time.Now())
	// Corrupt the deflate body of the second member, past its 10-byte header - same
	// technique as internal/archive's own
	// TestDecodeMembers_CorruptMemberReportsAndPreservesPrefix.
	bad[len(bad)/2] ^= 0xFF
	data := append(append([]byte{}, good...), bad...)

	dir := t.TempDir()
	path := filepath.Join(dir, archiveName)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	w := newWatcher(t, dir, nil)
	var got []*turn.Turn
	if err := w.Poll(context.Background(), collect(&got)); err != nil {
		t.Fatal(err)
	}

	if w.Stats.DecodeErrors == 0 {
		t.Error("DecodeErrors = 0, want > 0 - the second member is genuinely corrupt")
	}
}

// TestWatcher_ProgressReportsCurrentFileAndOpenResponses is a smoke test for the
// self-observability accessors this package exposes (issue #8): CurrentFile is the
// live (chronologically last) archive file, matching reclaim's own "never the newest
// file" rule, and OpenResponses mirrors the reducer's own in-flight count.
func TestWatcher_ProgressReportsCurrentFileAndOpenResponses(t *testing.T) {
	data := loadFixture(t)
	dir := t.TempDir()
	older := filepath.Join(dir, "2026-08-06T20.jsonl.gz")
	live := filepath.Join(dir, archiveName)
	for _, p := range []string{older, live} {
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	w := newWatcher(t, dir, nil)
	var got []*turn.Turn
	if err := w.Poll(context.Background(), collect(&got)); err != nil {
		t.Fatal(err)
	}

	p := w.Progress()
	if p.CurrentFile != archiveName {
		t.Errorf("CurrentFile = %q, want %q (the chronologically-last file)", p.CurrentFile, archiveName)
	}
	if p.CurrentFileOffset <= 0 {
		t.Errorf("CurrentFileOffset = %d, want > 0 after a full read", p.CurrentFileOffset)
	}
	// OpenResponses cannot be pinned to an exact count without depending on the
	// fixture's own content shape, but it must never panic or deadlock reading a live
	// reducer mid-use - that is what this call is actually proving, per the mutex
	// this method exists to exercise.
	_ = p.OpenResponses
}

func TestWatcher_StateRetainShrinksPublishedReducerCounts(t *testing.T) {
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	first := filepath.Join(dir, "2026-08-10T12.jsonl.gz")
	later := filepath.Join(dir, "2026-08-10T14.jsonl.gz")
	if err := os.WriteFile(first, buildTimedTurnArchive(t, base, "thread-retain", "req-base", 1, 10), 0o600); err != nil {
		t.Fatal(err)
	}

	w := newWatcher(t, dir, func(c *Config) {
		c.StateRetain = 0
		c.now = func() time.Time { return base }
	})
	if err := w.Poll(context.Background(), noEmit); err != nil {
		t.Fatal(err)
	}
	before := w.Progress()
	if before.ReducerSeriesCount != 1 || before.ReducerThreadCount != 1 {
		t.Fatalf("before eviction reducer counts = series %d threads %d, want 1 and 1",
			before.ReducerSeriesCount, before.ReducerThreadCount)
	}

	if err := os.WriteFile(later, buildTimestampedArchive(t, base.Add(2*time.Hour)), 0o600); err != nil {
		t.Fatal(err)
	}
	w.cfg.StateRetain = 30 * time.Minute
	if err := w.Poll(context.Background(), noEmit); err != nil {
		t.Fatal(err)
	}
	after := w.Progress()
	if after.ReducerSeriesCount != 0 || after.ReducerThreadCount != 0 {
		t.Fatalf("after eviction reducer counts = series %d threads %d, want 0 and 0",
			after.ReducerSeriesCount, after.ReducerThreadCount)
	}
}

// buildTimedTurnArchive writes one complete gzip member holding a response.create,
// one logical-turn timing event, and response.completed. It is intentionally tiny:
// state-retention tests need one reducer baseline, not fixture-shaped coverage.
func buildTimedTurnArchive(t *testing.T, ts time.Time, thread, req string, calls, prompt int) []byte {
	t.Helper()
	meta, err := json.Marshal(map[string]string{
		"thread_id": thread, "request_kind": frame.KindTurn,
	})
	if err != nil {
		t.Fatal(err)
	}
	events := []string{
		`{"type":"response.create","model":"gpt-5.6-sol","client_metadata":{"thread_id":"` +
			thread + `","x-codex-turn-metadata":` + jsonString(t, string(meta)) + `}}`,
		`{"type":"responsesapi.websocket_timing","timing_metrics":{"timing_scope":"logical_turn",` +
			`"num_engine_calls":` + jsonInt(t, calls) + `,"engine_total_prompt_tokens_total":` + jsonInt(t, prompt) + `}}`,
		`{"type":"response.completed","response":{"status":"completed","model":"gpt-5.6-sol"}}`,
	}
	lines := make([]byte, 0, len(events)*256)
	for i, event := range events {
		rec := map[string]any{
			"account_id":  "",
			"direction":   frame.ToCodex,
			"headers":     map[string]string{"thread-id": thread, "originator": "codex-tui"},
			"kind":        "responses",
			"method":      "",
			"payload":     map[string]string{"text": event},
			"request_id":  req,
			"status_code": nil,
			"timestamp":   ts.Add(time.Duration(i) * time.Millisecond).UTC().Format(time.RFC3339Nano),
			"transport":   "websocket",
			"url":         "",
			"extra":       map[string]any{},
		}
		line, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line...)
		lines = append(lines, '\n')
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(lines); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func jsonString(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func jsonInt(t *testing.T, n int) string {
	t.Helper()
	b, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
