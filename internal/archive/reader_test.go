package archive

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// liveFixture returns a real captured archive hour, or skips. These live under
// testdata/live/ and are gitignored - they contain full conversation content.
func liveFixture(t *testing.T) []byte {
	t.Helper()
	p := filepath.Join("..", "..", "testdata", "live", "2026-08-06T18.jsonl.gz")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Skipf("live fixture absent (%v); pull one from camden to run this", err)
	}
	return data
}

// appendMembers builds a multi-member file the same way codex-lb does: one closed
// gzip member per batch, appended.
func appendMembers(t *testing.T, batches ...[]string) []byte {
	t.Helper()
	var out bytes.Buffer
	for _, lines := range batches {
		zw := gzip.NewWriter(&out)
		for _, l := range lines {
			if _, err := zw.Write([]byte(l + "\n")); err != nil {
				t.Fatal(err)
			}
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return out.Bytes()
}

func TestDecodeMembers_Synthetic(t *testing.T) {
	data := appendMembers(t, []string{`{"a":1}`}, []string{`{"b":2}`, `{"c":3}`})

	res, err := DecodeMembers(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Consumed != len(data) {
		t.Errorf("Consumed = %d, want %d (all members complete)", res.Consumed, len(data))
	}
	if res.Members != 2 {
		t.Errorf("Members = %d, want 2", res.Members)
	}
	const want = "{\"a\":1}\n{\"b\":2}\n{\"c\":3}\n"
	if string(res.Data) != want {
		t.Errorf("Data = %q, want %q", res.Data, want)
	}
}

// The steady state while codex-lb is mid-append is a truncated final member. That
// must not error, and must not advance the offset past the good data. Checked at
// every possible truncation point, since the failure mode differs between a cut in
// the header, the deflate body, and the 8-byte trailer.
func TestDecodeMembers_PartialTailIsNotAnError(t *testing.T) {
	firstOnly := appendMembers(t, []string{`{"a":1}`})
	full := appendMembers(t, []string{`{"a":1}`}, []string{`{"b":2}`})

	for cut := len(firstOnly) + 1; cut < len(full); cut++ {
		res, err := DecodeMembers(full[:cut])
		if err != nil {
			t.Fatalf("cut=%d: unexpected error: %v", cut, err)
		}
		if res.Consumed != len(firstOnly) {
			t.Fatalf("cut=%d: Consumed = %d, want %d (only the first member is complete)",
				cut, res.Consumed, len(firstOnly))
		}
		if res.Members != 1 {
			t.Fatalf("cut=%d: Members = %d, want 1", cut, res.Members)
		}
		if string(res.Data) != "{\"a\":1}\n" {
			t.Fatalf("cut=%d: Data = %q, want only the first member", cut, res.Data)
		}
	}
}

// An empty input is the state before codex-lb has written anything.
func TestDecodeMembers_Empty(t *testing.T) {
	res, err := DecodeMembers(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Consumed != 0 || res.Members != 0 || len(res.Data) != 0 {
		t.Errorf("got %+v, want zero result", res)
	}
}

// Decoding in two passes from the checkpointed offset must equal one pass.
// This is exactly what a restart does.
func TestDecodeMembers_ResumeMatchesSinglePass(t *testing.T) {
	data := liveFixture(t)

	whole, err := DecodeMembers(data)
	if err != nil {
		t.Fatalf("single pass: %v", err)
	}
	if whole.Consumed != len(data) {
		t.Fatalf("Consumed = %d, want %d; a complete file should be fully consumed",
			whole.Consumed, len(data))
	}

	// A cut at an arbitrary point almost certainly lands mid-member.
	cut := len(data) / 3
	first, err := DecodeMembers(data[:cut])
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if first.Consumed > cut {
		t.Fatalf("Consumed = %d exceeds input %d", first.Consumed, cut)
	}

	second, err := DecodeMembers(data[first.Consumed:])
	if err != nil {
		t.Fatalf("resumed pass: %v", err)
	}
	if got := first.Consumed + second.Consumed; got != len(data) {
		t.Errorf("consumed %d + %d = %d, want %d",
			first.Consumed, second.Consumed, got, len(data))
	}
	if got := first.Members + second.Members; got != whole.Members {
		t.Errorf("members %d + %d = %d, want %d",
			first.Members, second.Members, got, whole.Members)
	}
	if got := append(append([]byte{}, first.Data...), second.Data...); !bytes.Equal(got, whole.Data) {
		t.Errorf("resumed output differs from single-pass output (%d vs %d bytes)",
			len(got), len(whole.Data))
	}
}

// Guards the counts this design was derived from, so a change in codex-lb's writer
// batching shows up here rather than as silently wrong telemetry.
func TestDecodeMembers_LiveFixtureShape(t *testing.T) {
	data := liveFixture(t)

	res, err := DecodeMembers(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Consumed != len(data) {
		t.Errorf("Consumed = %d, want %d", res.Consumed, len(data))
	}

	const wantLines = 16552
	if got := bytes.Count(res.Data, []byte("\n")); got != wantLines {
		t.Errorf("decoded %d lines, want %d", got, wantLines)
	}
	const wantMembers = 15106
	if res.Members != wantMembers {
		t.Errorf("Members = %d, want %d; if codex-lb changed its write batching, "+
			"re-derive the tailing assumptions before updating this number",
			res.Members, wantMembers)
	}
}

// Corrupt data mid-file must surface as an error while preserving what came before,
// so a caller can still make progress rather than stalling forever.
func TestDecodeMembers_CorruptMemberReportsAndPreservesPrefix(t *testing.T) {
	good := appendMembers(t, []string{`{"a":1}`})
	bad := appendMembers(t, []string{`{"b":2}`})
	// Corrupt the deflate body of the second member, past its 10-byte header.
	bad[len(bad)/2] ^= 0xFF

	res, err := DecodeMembers(append(append([]byte{}, good...), bad...))
	if err == nil {
		t.Fatal("expected an error for a corrupt member")
	}
	if res.Consumed != len(good) {
		t.Errorf("Consumed = %d, want %d (the good prefix)", res.Consumed, len(good))
	}
	if string(res.Data) != "{\"a\":1}\n" {
		t.Errorf("Data = %q, want the good prefix preserved", res.Data)
	}
}
