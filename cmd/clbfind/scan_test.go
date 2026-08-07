package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/rknightion/codexlb2otel/internal/frame"
)

// writeArchive builds a file the way codex-lb does: one closed gzip member per
// batch, appended. Self-contained members are the only reason a shard can seek into
// the middle of the file and resynchronise at all.
func writeArchive(t *testing.T, lines []string) string {
	t.Helper()
	var out bytes.Buffer
	for _, l := range lines {
		zw := gzip.NewWriter(&out)
		if _, err := zw.Write([]byte(l + "\n")); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "2026-08-07T10.jsonl.gz")
	if err := os.WriteFile(path, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// fixture builds records carrying a sequence number, padded so the file is large
// enough to shard.
func fixture(t *testing.T, n int) (path string, requestID string) {
	t.Helper()
	requestID = "ws_481fa8fa32724155a2ff8f372d7448ce"
	lines := make([]string, n)
	for i := range lines {
		id := requestID
		if i%3 == 0 {
			id = "ws_0000000000000000000000000000other" // interleaved noise
		}
		ev, err := json.Marshal(fmt.Sprintf(`{"type":"response.output_text.delta","seq":%d}`, i))
		if err != nil {
			t.Fatal(err)
		}
		lines[i] = fmt.Sprintf(
			`{"kind":"responses","direction":"server_to_codex","transport":"websocket",`+
				`"request_id":%q,"seq":%d,"pad":%q,"payload":{"text":%s}}`,
			id, i, strings.Repeat("x", 300), ev)
	}
	return writeArchive(t, lines), requestID
}

// The reducer is a state machine over a response's frames, so replaying them out of
// sequence produces a different turn. Sharding the scan across goroutines must
// therefore preserve exact file order - which is why shards write into their own
// slice and are concatenated by index, rather than being ordered by timestamp.
func TestCollect_PreservesFileOrderAcrossShards(t *testing.T) {
	old := minShard
	minShard = 4 << 10 // force many shards on a small fixture
	t.Cleanup(func() { minShard = old })

	const n = 4000
	path, requestID := fixture(t, n)

	recs, err := collect(path, func(rec *frame.Record) bool { return rec.RequestID == requestID })
	if err != nil {
		t.Fatal(err)
	}

	// Every matching record, exactly once.
	var want int
	for i := range n {
		if i%3 != 0 {
			want++
		}
	}
	if len(recs) != want {
		t.Fatalf("collected %d records, want %d", len(recs), want)
	}

	// And in ascending file order, with nothing duplicated or transposed.
	last := -1
	for _, rec := range recs {
		var probe struct {
			Seq int `json:"seq"`
		}
		if err := json.Unmarshal([]byte(rec.Payload.Text), &probe); err != nil {
			t.Fatal(err)
		}
		if probe.Seq <= last {
			t.Fatalf("record seq %d followed %d: shards were concatenated out of order", probe.Seq, last)
		}
		last = probe.Seq
	}
}

// A sharded scan must see every line exactly once. A resync that overshot would
// drop the records between two shards; one that undershot would double them.
func TestScanFile_ShardsCoverEveryLineExactlyOnce(t *testing.T) {
	old := minShard
	minShard = 4 << 10
	t.Cleanup(func() { minShard = old })

	const n = 4000
	path, _ := fixture(t, n)

	// scanFile calls fn from several goroutines - guarding this is the caller's job,
	// and forgetting it here crashed the test with "concurrent map writes".
	var mu sync.Mutex
	seen := map[int]int{}
	serial := 0
	if err := streamLines(path, func([]byte) error { serial++; return nil }); err != nil {
		t.Fatal(err)
	}
	if serial != n {
		t.Fatalf("serial scan saw %d lines, wrote %d", serial, n)
	}

	var total int
	err := scanFile(path, func(_ int, line []byte) error {
		var rec struct {
			Seq int `json:"seq"`
		}
		if err := json.Unmarshal(line, &rec); err != nil {
			return err
		}
		mu.Lock()
		seen[rec.Seq]++
		total++
		mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != serial {
		t.Errorf("sharded scan saw %d lines, serial saw %d", total, serial)
	}
	for i := range n {
		if seen[i] != 1 {
			t.Fatalf("line %d seen %d times, want exactly 1", i, seen[i])
		}
	}
}

func TestShards(t *testing.T) {
	old := minShard
	minShard = 1000
	t.Cleanup(func() { minShard = old })

	// Ranges must tile the file: contiguous, no gaps, ending at the size.
	got := shards(10_000, 4)
	if len(got) != 4 {
		t.Fatalf("got %d shards, want 4: %v", len(got), got)
	}
	if got[0][0] != 0 {
		t.Errorf("first shard starts at %d, want 0", got[0][0])
	}
	for i := 1; i < len(got); i++ {
		if got[i][0] != got[i-1][1] {
			t.Errorf("gap or overlap between shard %d and %d: %v", i-1, i, got)
		}
	}
	if last := got[len(got)-1][1]; last != 10_000 {
		t.Errorf("last shard ends at %d, want 10000", last)
	}

	// A file too small to be worth splitting stays whole - the resync costs more
	// than the concurrency returns.
	if s := shards(500, 8); len(s) != 1 || s[0] != [2]int64{0, 500} {
		t.Errorf("small file sharded into %v, want one whole range", s)
	}
}
