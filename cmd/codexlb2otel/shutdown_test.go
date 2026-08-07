package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rknightion/codexlb2otel/internal/tail"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

// fakeSink lets a test fail Emit and Flush independently - exactly the distinction
// the shutdown-ordering invariant turns on. A sink is allowed to accept a batch and
// only fail later when asked to actually deliver it.
type fakeSink struct {
	failFlush bool
	emitted   int
	flushed   int
}

func (f *fakeSink) Name() string { return "fake" }

func (f *fakeSink) Emit(_ context.Context, turns []*turn.Turn) error {
	f.emitted += len(turns)
	return nil
}

func (f *fakeSink) Flush(context.Context) error {
	f.flushed++
	if f.failFlush {
		return errors.New("boom: sink unreachable")
	}
	return nil
}

func (f *fakeSink) Close(context.Context) error { return nil }

const archiveName = "2026-08-06T21.jsonl.gz"

// buildArchive writes one complete gzip member holding a single frame.Record whose
// payload is a terminal "error" event. That is the cheapest record shape that closes
// a turn in a single Reducer.Add call (see internal/turn's EvError case), which
// means this test needs no corpus fixture to prove the property below.
func buildArchive(t *testing.T, requestID string) []byte {
	t.Helper()
	rec := map[string]any{
		"account_id": "acct1",
		"direction":  "server_to_codex",
		"headers":    map[string]string{},
		"kind":       "frame",
		"method":     "",
		"payload": map[string]string{
			"text": `{"type":"error","error":{"code":"server_is_overloaded","message":"boom"}}`,
		},
		"request_id":  requestID,
		"status_code": nil,
		"timestamp":   time.Now().UTC().Format(time.RFC3339Nano),
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

// This is the acceptance criterion from issue #4, exercised through the service's own
// wiring rather than restated as a unit test of internal/tail (which already proves
// the synchronous-Emit-error case in TestWatcher_FailedEmitDoesNotAdvanceCheckpoint).
// sinkEmit's whole reason to exist is closing the gap that test cannot see: a sink
// whose Emit "accepts" a batch (returns nil, per the interface's buffering allowance)
// but then fails to actually deliver it on Flush must not let the checkpoint advance
// either - and Watcher's own checkpointing only ever consults the callback it was
// given, which for this service is sinkEmit.
func TestSinkEmit_FailedFlushDoesNotAdvanceCheckpoint(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, archiveName), buildArchive(t, "req-1"), 0o644); err != nil {
		t.Fatal(err)
	}
	cp := filepath.Join(dir, "state", "checkpoint.json")

	w, err := tail.New(tail.Config{
		Dir:            dir,
		CheckpointPath: cp,
		PollInterval:   time.Hour, // Poll is driven directly in this test
		ChunkSize:      1 << 20,
	}, turn.New())
	if err != nil {
		t.Fatal(err)
	}

	fs := &fakeSink{failFlush: true}
	if err := w.Poll(context.Background(), sinkEmit(fs)); err == nil {
		t.Fatal("expected the failed flush to surface from Poll")
	}
	if fs.emitted == 0 {
		t.Fatal("sink never saw the turn; this test isn't exercising the flush failure path")
	}

	got, err := tail.LoadCheckpoint(cp)
	if err != nil {
		t.Fatal(err)
	}
	if st := got.Files[archiveName]; st.Offset != 0 {
		t.Errorf("checkpoint advanced to offset %d despite the flush failing", st.Offset)
	}

	// Flip to a healthy sink and confirm the positive case too: a checkpoint that
	// NEVER advances is just as broken as one that advances too early - it would
	// re-ship the whole archive on every restart.
	fs.failFlush = false
	if err := w.Poll(context.Background(), sinkEmit(fs)); err != nil {
		t.Fatalf("poll with a healthy sink: %v", err)
	}
	got, err = tail.LoadCheckpoint(cp)
	if err != nil {
		t.Fatal(err)
	}
	if st := got.Files[archiveName]; st.Offset == 0 {
		t.Error("checkpoint did not advance once the flush started succeeding")
	}
}
