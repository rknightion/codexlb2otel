package profile

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// record wraps an inner protocol event the way the archive does: the event is a
// JSON *string* inside payload.text, double-encoded.
func record(event string) string {
	inner, err := json.Marshal(event)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf(`{"kind":"responses","direction":"server_to_codex","transport":"websocket",`+
		`"request_id":"ws_%032x","payload":{"text":%s}}`, 1, inner)
}

func profileOf(t *testing.T, lines ...string) *Profile {
	t.Helper()
	p := New()
	fp := FileProfile{Name: "2026-08-06T10.jsonl.gz"}
	for _, l := range lines {
		p.AddLine([]byte(l), &fp)
	}
	p.Lines = fp.Lines
	p.Members = fp.Lines // one record per member, as codex-lb writes
	p.Files = []FileProfile{fp}
	return p
}

func sigOf(t *testing.T, lines ...string) *Signature {
	t.Helper()
	return profileOf(t, lines...).Signature(Coverage{Files: 1})
}

func findingsFor(f []Finding, kind string) []Finding {
	var out []Finding
	for _, x := range f {
		if x.Kind == kind {
			out = append(out, x)
		}
	}
	return out
}

// This is the finding the whole tool exists for. A field that gains a second JSON
// type is not a cosmetic protocol addition: Go's decoder abandons the ENTIRE event
// on one mismatched field, so when inputItem.Output started arriving as a list of
// parts as well as a string, every prompt, the model and the effort for that turn
// were discarded - and the response.created fallback refilled the model, hiding it.
// Nothing else in the pipeline reports that, so it must be BREAKING here.
func TestDiff_APathGainingASecondTypeIsBreaking(t *testing.T) {
	base := sigOf(t, record(`{"type":"response.create","input":[{"output":"ok"}]}`))
	cur := sigOf(t,
		record(`{"type":"response.create","input":[{"output":"ok"}]}`),
		record(`{"type":"response.create","input":[{"output":[{"type":"text"}]}]}`),
	)

	f := findingsFor(Diff(base, cur), "event.path.type")
	if len(f) == 0 {
		t.Fatal("a path that gained a second JSON type produced no finding")
	}
	for _, x := range f {
		if x.Severity != SevBreaking {
			t.Errorf("%s reported as %s, want BREAKING", x.Subject, x.Level)
		}
	}
}

func TestDiff_ReportsAdditions(t *testing.T) {
	base := sigOf(t, record(`{"type":"response.completed","response":{"status":"completed"}}`))
	cur := sigOf(t,
		record(`{"type":"response.completed","response":{"status":"completed"}}`),
		record(`{"type":"response.completed","response":{"status":"incomplete","refusal_reason_code":"x"}}`),
		record(`{"type":"response.retried","attempt":2}`),
		`{"kind":"responses","transport":"websocket","request_id":"ws_1","novel_field":1,`+
			`"headers":{"X-Codex-New-Thing":"a"},"payload":{"text":"{\"type\":\"response.completed\"}"}}`,
	)

	f := Diff(base, cur)
	for _, want := range []string{"event.type", "event.path", "event.path.value", "record.key", "record.header"} {
		if len(findingsFor(f, want)) == 0 {
			t.Errorf("no %s finding; got %v", want, kinds(f))
		}
	}
	for _, x := range f {
		if x.Severity == SevBreaking {
			t.Errorf("pure addition reported as breaking: %s %s", x.Kind, x.Subject)
		}
	}
}

// Absence is the one direction that must never fail a build. A sampled scan reads a
// few megabytes of a gigabyte file, and the rarest real shapes occur ~10 times in
// 1.3M records, so "not in this capture" is almost always the sample rather than a
// protocol removal.
func TestDiff_AbsenceIsOnlyInformational(t *testing.T) {
	base := sigOf(t,
		record(`{"type":"response.completed","response":{"status":"completed"}}`),
		record(`{"type":"error","code":"server_is_overloaded"}`),
	)
	cur := sigOf(t,
		record(`{"type":"response.completed","response":{"status":"completed"}}`),
		record(`{"type":"response.completed","response":{"status":"completed"}}`),
	)

	f := Diff(base, cur)
	if len(findingsFor(f, "event.type.absent")) == 0 {
		t.Fatalf("a vanished event type was not reported at all; got %v", kinds(f))
	}
	for _, x := range f {
		if x.Severity != SevInfo {
			t.Errorf("absence reported as %s: %s %s", x.Level, x.Kind, x.Subject)
		}
	}
}

// A transport change is invisible in the record's own fields - transport reads
// "websocket" for the HTTP family too - so framing is the only place it surfaces.
func TestDiff_NewPayloadFramingIsBreaking(t *testing.T) {
	base := sigOf(t, record(`{"type":"response.completed"}`))
	cur := sigOf(t,
		record(`{"type":"response.completed"}`),
		record("data: {\"type\":\"response.completed\"}\n\n"),
	)

	f := findingsFor(Diff(base, cur), "storage.payload_framing")
	if len(f) != 1 || f[0].Severity != SevBreaking {
		t.Fatalf("SSE framing not reported as breaking; got %+v", f)
	}
	if f[0].Subject != FramingSSE {
		t.Errorf("framing classified as %q, want %q", f[0].Subject, FramingSSE)
	}
}

func TestFraming(t *testing.T) {
	for in, want := range map[string]string{
		`{"type":"x"}`:      FramingJSONObject,
		`  {"type":"x"}`:    FramingJSONObject,
		"data: {}\n\n":      FramingSSE,
		"event: delta\n":    FramingSSE,
		": keepalive\n":     FramingSSE,
		"[1,2]":             FramingJSONArray,
		`"a string"`:        FramingJSONScalar,
		"no close frame":    FramingPlainText,
		"":                  FramingEmpty,
		"   \n":             FramingEmpty,
		"retry: 1000\n":     FramingSSE,
		"id: 42\ndata: {}":  FramingSSE,
		"received 1012 (x)": FramingPlainText,
	} {
		if got := framing(in); got != want {
			t.Errorf("framing(%q) = %q, want %q", in, got, want)
		}
	}
}

// The baseline is committed. The archive holds real prompts, tool output and
// assistant messages, so a signature that carried example values would put personal
// data into git history permanently - and this repository is intended to go public.
func TestSignature_CarriesNoConversationContent(t *testing.T) {
	const secret = "SENSITIVE-PROMPT-BODY"
	sig := sigOf(t,
		record(`{"type":"response.create","instructions":"`+secret+`","input":[`+
			`{"role":"user","content":"`+secret+`"},`+
			`{"type":"function_call","arguments":"`+secret+`","name":"exec"}],`+
			`"thread_id":"`+secret+`","prompt_cache_key":"`+secret+`",`+
			`"message":{"text":"`+secret+`","author":"`+secret+`"},`+
			`"error_message":"`+secret+`","url":"https://x/`+secret+`",`+
			`"model":"gpt-5.6-sol"}`),
	)

	blob, err := json.Marshal(sig)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), secret) {
		t.Fatalf("signature leaked conversation content:\n%s", blob)
	}

	// The safety rule must not be so blunt that it swallows the enum values the
	// diff depends on - a new model appearing is a finding worth having.
	if !strings.Contains(string(blob), "gpt-5.6-sol") {
		t.Fatalf("model value was dropped, so a new model would not be detected:\n%s", blob)
	}
}

func TestContentSafe(t *testing.T) {
	for path, want := range map[string]bool{
		"model":                      true,
		"response.status":            true,
		"error.code":                 true,
		"reasoning.effort":           true,
		"input[].content":            false,
		"instructions":               false,
		"response.output[].text":     false,
		"thread_id":                  false,
		"client_metadata.session_id": false,
		"prompt_cache_key":           false,
		"headers.authorization":      true,
		// Hyphenated, header-derived paths: an underscore-only separator class let a
		// raw thread UUID into a committed baseline.
		"client_metadata.x-codex-parent-thread-id": false,
		"x-codex-window-id":                        false,
		"client_metadata.safety-identifier":        false,
		"safety_buffering.use_cases":               true,
		"message.author":                           false,
		"tool.name":                                true,
		"usage.input_tokens":                       true,
		"response.instructions_chars":              true,
	} {
		if got := contentSafe(path); got != want {
			t.Errorf("contentSafe(%q) = %v, want %v", path, got, want)
		}
	}
}

// High-cardinality values must not sneak in just because a small sample made them
// look enum-like, and neither must long ones.
func TestEnumValues_BoundedBothWays(t *testing.T) {
	many := &Hist{Counts: map[string]int64{}}
	for i := range maxEnumValues + 1 {
		many.add(fmt.Sprintf("v%d", i))
	}
	if v := enumValues("model", many); v != nil {
		t.Errorf("accepted %d distinct values", len(v))
	}

	long := &Hist{Counts: map[string]int64{}}
	long.add(strings.Repeat("a", maxEnumLen+1))
	if v := enumValues("model", long); v != nil {
		t.Errorf("accepted a %d-char value", maxEnumLen+1)
	}

	ok := &Hist{Counts: map[string]int64{}}
	ok.add("high")
	ok.add("medium")
	if v := enumValues("reasoning.effort", ok); len(v) != 2 {
		t.Errorf("rejected a genuine enum: %v", v)
	}
}

func TestSignature_VolumeIsBucketedSoTheBaselineDoesNotChurn(t *testing.T) {
	quiet := sigOf(t, repeatRecord(`{"type":"response.completed"}`, 30)...)
	busy := sigOf(t, repeatRecord(`{"type":"response.completed"}`, 95)...)

	if q, b := quiet.Events["response.completed"].Magnitude, busy.Events["response.completed"].Magnitude; q != b {
		t.Fatalf("30 vs 95 frames bucketed differently (%d vs %d); the baseline would churn", q, b)
	}
	if f := Diff(quiet, busy); len(f) != 0 {
		t.Fatalf("a pure volume change produced findings: %v", f)
	}
}

func repeatRecord(event string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = record(event)
	}
	return out
}

func kinds(f []Finding) []string {
	out := make([]string, 0, len(f))
	for _, x := range f {
		out = append(out, x.Kind)
	}
	return out
}
