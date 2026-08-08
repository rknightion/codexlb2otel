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
// The archive count was pinned at n=7 until 2026-08-08, chosen empirically as the
// smallest fixture.Any() that reached "memory" - with a note saying to widen it if the
// corpus composition changed. It did: a sync brought in two quiet overnight hours
// (146 and 250 lines), fixture.Any returns the CHEAPEST n, and the seven cheapest then
// held only prewarm and turn. So the test now widens ITSELF rather than carrying a
// hand-tuned constant that decays every time the corpus grows a small file (#34).
//
// Exhausting the corpus without all four values is still a hard failure, not a skip -
// that would mean this guard against #20's silent 4.3x over-count is asserting nothing.
func TestInduceEmbedded_RequestKindValuesFoundInRealCorpus(t *testing.T) {
	want := []string{frame.KindTurn, frame.KindPrewarm, frame.KindCompaction, frame.KindMemory}
	paths := []string{
		"client_metadata." + frame.HdrTurnMetadata + "{}.request_kind",
		"header." + frame.HdrTurnMetadata + "{}.request_kind",
	}

	all := fixture.Files(t)
	p := New()
	var sig *Signature
	for i, path := range all {
		fp, _, err := ScanFile(path, DefaultChunk)
		if err != nil {
			t.Fatalf("scanning %s: %v", fixture.Name(path), err)
		}
		p.Merge(fp)
		sig = p.Signature(Coverage{})
		if haveAllKinds(sig, paths, want) {
			t.Logf("all four request kinds found after %d/%d archives", i+1, len(all))
			break
		}
		if i == len(all)-1 {
			t.Fatalf("the corpus under %s does not contain all of %v across its %d "+
				"archives - add an archive hour that does, otherwise this guard is "+
				"asserting nothing", fixture.Root(t), want, len(all))
		}
	}

	// Both allowlisted sources are checked: the client_metadata copy nested in
	// response.create (the one frame.TurnMeta's doc comment says is authoritative)
	// and the header copy, which is stale but still on the allowlist per the issue.
	for _, path := range paths {
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

// haveAllKinds is the stopping condition for the widening loop above, and it asks
// exactly what the assertions afterwards assert - so the loop cannot stop one archive
// before the thing the test then demands.
func haveAllKinds(sig *Signature, paths, want []string) bool {
	for _, path := range paths {
		got := valuesAt(sig, path)
		if got == nil {
			return false
		}
		for _, w := range want {
			if !containsStr(got, w) {
				return false
			}
		}
	}
	return true
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
