package profile

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeArchive builds a file the way codex-lb does: one closed gzip member per
// batch, appended. That is what allows a reader to resynchronise mid-file.
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
	path := filepath.Join(t.TempDir(), "2026-08-06T10.jsonl.gz")
	if err := os.WriteFile(path, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// padded keeps each record large enough that the file needs many windows, so the
// sampling path is genuinely exercised rather than falling back to a full read.
func padded(event string, i int) string {
	return fmt.Sprintf(`{"kind":"responses","direction":"server_to_codex","transport":"websocket",`+
		`"request_id":"ws_%08d","pad":"%s","payload":{"text":%s}}`,
		i, strings.Repeat("x", 400), mustJSON(event))
}

func mustJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func TestScanFile_MatchesLineByLineProfiling(t *testing.T) {
	lines := make([]string, 500)
	for i := range lines {
		lines[i] = padded(`{"type":"response.completed","response":{"status":"completed"}}`, i)
	}
	path := writeArchive(t, lines)

	p, cov, err := ScanFile(path, 1<<16)
	if err != nil {
		t.Fatal(err)
	}
	if p.Lines != int64(len(lines)) {
		t.Errorf("read %d lines, wrote %d", p.Lines, len(lines))
	}
	if p.Members != int64(len(lines)) {
		t.Errorf("saw %d members, wrote %d", p.Members, len(lines))
	}
	if p.Bad != 0 {
		t.Errorf("%d undecodable lines in a file we just wrote", p.Bad)
	}
	if cov.Sampled || cov.Fraction() != 1 {
		t.Errorf("full scan reported coverage %+v", cov)
	}
}

// The point of sampling is answering "has the format changed?" without paying for
// the whole file. It must read materially less, and everything it reports must be
// real - a resync landing on a false boundary would manufacture shapes that are not
// in the archive, which is worse than missing one.
func TestScanSampled_ReadsLessAndInventsNothing(t *testing.T) {
	lines := make([]string, 8000)
	for i := range lines {
		lines[i] = padded(`{"type":"response.completed","response":{"status":"completed"}}`, i)
	}
	path := writeArchive(t, lines)

	full, _, err := ScanFile(path, DefaultChunk)
	if err != nil {
		t.Fatal(err)
	}
	sampled, cov, err := ScanSampled(path, 8, 8<<10)
	if err != nil {
		t.Fatal(err)
	}

	if !cov.Sampled {
		t.Fatal("sampled scan did not report itself as sampled")
	}
	if cov.Fraction() >= 0.5 {
		t.Errorf("sampling read %.1f%% of the file; that is not a saving", 100*cov.Fraction())
	}
	if sampled.Lines == 0 {
		t.Fatal("sampled scan decoded nothing")
	}
	if sampled.Bad != 0 {
		t.Errorf("%d undecodable lines - a resync landed off a member boundary", sampled.Bad)
	}
	if cov.Resynced == 0 {
		t.Error("no window resynchronised; the sampling path was not exercised")
	}

	for name := range sampled.Events {
		if _, ok := full.Events[name]; !ok {
			t.Errorf("sampled scan reported event %q that a full scan does not contain", name)
		}
	}
	for key := range sampled.RecordKeys {
		if _, ok := full.RecordKeys[key]; !ok {
			t.Errorf("sampled scan reported record key %q that a full scan does not contain", key)
		}
	}
	t.Logf("read %d of %d bytes (%.1f%%), %d lines, %d/%d windows resynced",
		cov.ReadBytes, cov.FileBytes, 100*cov.Fraction(), sampled.Lines, cov.Resynced, cov.Windows)
}

// Sampling a file smaller than the requested budget would cost the same as reading
// it and answer less, so it must degrade to a full scan rather than sample.
func TestScanSampled_SmallFileFallsBackToFull(t *testing.T) {
	path := writeArchive(t, []string{padded(`{"type":"response.completed"}`, 0)})
	p, cov, err := ScanSampled(path, 16, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if cov.Sampled {
		t.Error("sampled a file smaller than one window")
	}
	if p.Lines != 1 {
		t.Errorf("got %d lines, want 1", p.Lines)
	}
}

// Offset 0 needs no resync and is where a change to the file header would appear;
// the tail is the freshest traffic, where a protocol change shows up first. Both
// must always be sampled.
func TestWindowOffsets_CoverBothEnds(t *testing.T) {
	const size, window = 1000, 100
	offs := windowOffsets(size, window, 5)
	if offs[0] != 0 {
		t.Errorf("first window at %d, want 0", offs[0])
	}
	if last := offs[len(offs)-1]; last != size-window {
		t.Errorf("last window at %d, want %d", last, size-window)
	}
	for i := 1; i < len(offs); i++ {
		if offs[i] <= offs[i-1] {
			t.Fatalf("offsets not increasing: %v", offs)
		}
	}
}
