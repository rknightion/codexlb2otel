package profile

import (
	"testing"

	"github.com/rknightion/codexlb2otel/internal/fixture"
	"github.com/rknightion/codexlb2otel/internal/frame"
)

// This is the exact miss #20 documents: request_kind gaining the value "memory"
// inside x-codex-turn-metadata caused a silent 4.3x engine-token over-count, and the
// profiler could not see it because it recorded the embedded document as an opaque
// string leaf. Scanning real captured archives and asserting all four known
// request_kind values surface in the induced signature is the guard against that
// specific miss recurring silently.
//
// n=7 is the smallest fixture.Any() count that reaches "memory" in this corpus,
// checked empirically (2026-08-07): n=1..6 only ever produce prewarm/turn, or
// occasionally compaction; "memory" first appears once the 7th-smallest archive is
// included. If the corpus composition changes and this starts failing for lack of
// coverage rather than a real regression, widen n rather than deleting the assertion.
func TestInduceEmbedded_RequestKindValuesFoundInRealCorpus(t *testing.T) {
	files := fixture.Any(t, 7)

	p := New()
	for _, path := range files {
		fp, _, err := ScanFile(path, DefaultChunk)
		if err != nil {
			t.Fatalf("scanning %s: %v", fixture.Name(path), err)
		}
		p.Merge(fp)
	}
	sig := p.Signature(Coverage{})

	want := []string{frame.KindTurn, frame.KindPrewarm, frame.KindCompaction, frame.KindMemory}

	// Both allowlisted sources are checked: the client_metadata copy nested in
	// response.create (the one frame.TurnMeta's doc comment says is authoritative)
	// and the header copy, which is stale but still on the allowlist per the issue.
	for _, path := range []string{
		"client_metadata." + frame.HdrTurnMetadata + "{}.request_kind",
		"header." + frame.HdrTurnMetadata + "{}.request_kind",
	} {
		got := valuesAt(sig, path)
		if got == nil {
			t.Fatalf("path %q not found in any induced event; embedded descent did not run", path)
		}
		for _, w := range want {
			if !containsStr(got, w) {
				t.Errorf("path %q values = %v, missing request_kind %q", path, got, w)
			}
		}
	}
}

// A prompt or tool argument that happens to look like JSON must never be descended
// into - that is the whole reason the allowlist is explicit rather than sniffed (see
// embedded.go). client_metadata.some_other_field is not on the allowlist, so its
// value must be recorded as a plain string leaf and produce no "{}"-suffixed paths,
// even though the value parses as JSON just fine.
func TestDescendEmbedded_OnlyAllowlistedPathsAreDescended(t *testing.T) {
	notAllowlisted := `{"secret":"user prompt content that happens to look like json"}`
	sig := sigOf(t, record(
		`{"type":"response.create","client_metadata":{"some_other_field":`+mustJSON(notAllowlisted)+`}}`,
	))

	es, ok := sig.Events["response.create"]
	if !ok {
		t.Fatal("response.create not induced at all")
	}
	if ps, ok := es.Paths["client_metadata.some_other_field"]; !ok || ps.Types[0] != "string" {
		t.Fatalf("client_metadata.some_other_field = %+v, want a plain string leaf", ps)
	}
	for path := range es.Paths {
		if hasEmbeddedSuffix(path) {
			t.Errorf("non-allowlisted field was descended into: found path %q", path)
		}
	}
}

// Requirement: a string on the allowlist that does NOT parse as JSON is recorded the
// way it is today (a plain string leaf) and must not error - AddLine exists to
// survive exactly the input it characterises, and malformed data is exactly that.
func TestDescendEmbedded_MalformedJSONStaysAPlainStringLeaf(t *testing.T) {
	sig := sigOf(t, record(
		`{"type":"response.create","client_metadata":{"x-codex-turn-metadata":"not-json{"}}`,
	))

	es, ok := sig.Events["response.create"]
	if !ok {
		t.Fatal("response.create not induced at all")
	}
	path := "client_metadata." + frame.HdrTurnMetadata
	ps, ok := es.Paths[path]
	if !ok {
		t.Fatalf("path %q missing entirely", path)
	}
	if len(ps.Types) != 1 || ps.Types[0] != "string" {
		t.Errorf("path %q types = %v, want only string", path, ps.Types)
	}
	for p := range es.Paths {
		if hasEmbeddedSuffix(p) {
			t.Errorf("malformed JSON was descended into: found path %q", p)
		}
	}
}

func hasEmbeddedSuffix(path string) bool {
	for i := 0; i+1 < len(path); i++ {
		if path[i] == '{' && path[i+1] == '}' {
			return true
		}
	}
	return false
}

func valuesAt(sig *Signature, path string) []string {
	for _, es := range sig.Events {
		if ps, ok := es.Paths[path]; ok {
			return ps.Values
		}
	}
	return nil
}

func containsStr(hay []string, want string) bool {
	for _, v := range hay {
		if v == want {
			return true
		}
	}
	return false
}
