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
		// The embedded-document marker must not shield the segment carrying it. Every
		// one of these is unsafe with the "{}" removed, so every one must stay unsafe
		// with it present - otherwise descending into a document simply turns the
		// guard off for the document's own name.
		"client_metadata.x-codex-turn-metadata{}.request_kind": true,
		"client_metadata.user{}.name":                          false,
		"header.x-codex-turn-metadata{}.parent_turn_id":        false,
		"response.instructions{}.foo":                          false,
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

// A tool's `parameters` is a JSON Schema the CLIENT writes to describe its own tool
// config. Walking it induces one path per property of every tool, so the first
// capture containing a tool the baseline lacked produced 217 "new field" findings -
// burying the two that mattered, a new client_metadata.parent_turn_id and a new
// originator. Tool identity must still be tracked, at tools[].name.
func TestSignature_ToolSchemasAreOpaqueButToolIdentityIsNot(t *testing.T) {
	base := sigOf(t, record(`{"type":"response.completed","response":{"tools":[`+
		`{"name":"exec","type":"custom","parameters":{"properties":{"cmd":{"type":"string"}}}}]}}`))
	cur := sigOf(t, record(`{"type":"response.completed","response":{"tools":[`+
		`{"name":"exec","type":"custom","parameters":{"properties":{"cmd":{"type":"string"},`+
		`"timeout":{"type":"number"},"cwd":{"type":"string"}}}}]}}`))

	if f := Diff(base, cur); len(f) != 0 {
		t.Errorf("a client changing its tool parameter schema produced %d finding(s): %v", len(f), kinds(f))
	}

	// ...but a genuinely new tool still has to surface.
	withNewTool := sigOf(t, record(`{"type":"response.completed","response":{"tools":[`+
		`{"name":"exec","type":"custom","parameters":{}},`+
		`{"name":"spawn_agent","type":"function","parameters":{}}]}}`))
	if len(findingsFor(Diff(base, withNewTool), "event.path.value")) == 0 {
		t.Error("a new tool name produced no finding; drift detection is now blind to tool changes")
	}
}

// controlRecord wraps a websocket CONTROL frame. Its payload.text is a close reason
// written for a human - or empty on a clean close - not a protocol event.
func controlRecord(frameType, text string) string {
	inner, err := json.Marshal(text)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf(`{"kind":"responses","direction":"server_to_codex","transport":"websocket",`+
		`"extra":{"frame_type":%q,"close_code":1000},"payload":{"text":%s}}`, frameType, inner)
}

// Control frames must not reach the framing classifier. 20 of them in 1.5M lines
// were enough to hold a permanent BREAKING finding open - "an SSE or chunked
// transport would look like this" - against a transport that has never existed.
func TestProfile_ControlFramesAreNotProtocolPayloads(t *testing.T) {
	p := profileOf(t,
		record(`{"type":"response.completed"}`),
		controlRecord("close", ""),
		controlRecord("error", "no close frame received or sent"),
	)

	if p.TransportFrames != 2 {
		t.Errorf("counted %d control frames, want 2", p.TransportFrames)
	}
	if p.PayloadUnparsed != 0 {
		t.Errorf("%d control frame(s) counted as unparseable payloads", p.PayloadUnparsed)
	}
	if got := p.PayloadFraming; len(got) != 1 || got[FramingJSONObject] != 1 {
		t.Errorf("framing histogram = %v, want only one %s from the protocol frame",
			got, FramingJSONObject)
	}

	// Excluded from framing, but their vocabulary still has to be tracked: a new
	// control frame type is a real finding, just not a transport change.
	vals := p.Signature(Coverage{Files: 1}).Record.Fields["extra.frame_type"]
	for _, want := range []string{"close", "error"} {
		if !strings.Contains(strings.Join(vals, ","), want) {
			t.Errorf("extra.frame_type enum = %v, missing %q", vals, want)
		}
	}
}

func TestDiff_AFirstControlFrameIsNotATransportChange(t *testing.T) {
	base := sigOf(t, record(`{"type":"response.completed"}`))
	cur := sigOf(t,
		record(`{"type":"response.completed"}`),
		controlRecord("close", ""),
		controlRecord("error", "received 1012 (service restart)"),
	)

	f := Diff(base, cur)
	for _, x := range f {
		if x.Severity == SevBreaking {
			t.Errorf("a websocket control frame reported as breaking: %s %s", x.Kind, x.Subject)
		}
	}
	if len(findingsFor(f, "record.field")) == 0 {
		t.Errorf("control frames appeared and nothing was reported at all; got %v", kinds(f))
	}
}

// A field turning nullable loses no data, so it must not spend the budget of the one
// severity that has to stay believable. See PathSig.
func TestDiff_AFieldTurningNullableIsNotBreaking(t *testing.T) {
	base := sigOf(t, record(`{"type":"codex.rate_limits","rate_limits":{"used_percent":1}}`))
	cur := sigOf(t,
		record(`{"type":"codex.rate_limits","rate_limits":{"used_percent":1}}`),
		record(`{"type":"codex.rate_limits","rate_limits":null}`),
	)

	f := Diff(base, cur)
	nullable := findingsFor(f, "event.path.nullable")
	if len(nullable) != 1 {
		t.Fatalf("a field that started arriving as null produced %d nullable finding(s); got %v",
			len(nullable), kinds(f))
	}
	if nullable[0].Severity != SevNew {
		t.Errorf("nullable reported as %s, want NEW", nullable[0].Level)
	}
	for _, x := range f {
		if x.Severity == SevBreaking {
			t.Errorf("null addition reported as breaking: %s %s", x.Kind, x.Subject)
		}
	}
}

// The reason the case above is not breaking, pinned rather than left on trust: the
// string-vs-array case abandons the whole event, and null simply does not.
func TestNullDecodesIntoAnyTypeWithoutAbandoningTheEvent(t *testing.T) {
	var v struct {
		RateLimits map[string]float64 `json:"rate_limits"`
		Balance    string             `json:"balance"`
		Model      string             `json:"model"`
	}
	v.Balance = "kept"

	err := json.Unmarshal([]byte(`{"rate_limits":null,"balance":null,"model":"gpt-5.6-sol"}`), &v)
	if err != nil {
		t.Fatalf("null broke the decode: %v", err)
	}
	if v.Model != "gpt-5.6-sol" {
		t.Errorf("model = %q; a null sibling discarded the rest of the event", v.Model)
	}
	if v.Balance != "kept" {
		t.Errorf("balance = %q, want the value untouched - null into a non-pointer is a no-op", v.Balance)
	}

	// The genuine breaking case, for contrast.
	if err := json.Unmarshal([]byte(`{"balance":["a"],"model":"gpt-5.6-sol"}`), &v); err == nil {
		t.Error("a type mismatch decoded cleanly; the breaking grading would be unjustified")
	}
}

func TestOpaque(t *testing.T) {
	for path, want := range map[string]bool{
		"response.tools[].parameters":                          true,
		"response.tools[].tools[].parameters":                  true,
		"response.tools[].format":                              true,
		"input[].tools[].parameters":                           true,
		"response.tools[].name":                                false,
		"response.tools[].parameters.required":                 false,
		"response.usage.attribution.items":                     true,
		"response.usage.attribution.items.rs_abc.input_tokens": false,
		"parameters":       false,
		"reasoning.format": false,
		// The client's per-installation tool catalog, embedded inside
		// x-codex-turn-metadata - see opaque's doc comment for why this floods the
		// same path budget tools[].parameters already needed protecting from.
		"client_metadata.x-codex-turn-metadata{}.code_mode_tool_names":           true,
		"header.x-codex-turn-metadata{}.code_mode_tool_names":                    true,
		"client_metadata.x-codex-turn-metadata{}.code_mode_tool_names.exec.name": false,
		"client_metadata.x-codex-turn-metadata{}.request_kind":                   false,
		"code_mode_tool_names": false,
	} {
		if got := opaqueKind(path) != ""; got != want {
			t.Errorf("opaqueKind(%q) opaque = %v, want %v", path, got, want)
		}
	}
}

// TestOpaque_WorkspacesNeverBecomesAPath is the PII guard, and it is a different
// claim from the rest of TestOpaque: not "this would be noisy" but "this must never
// reach a committed file". workspaces is keyed by absolute filesystem path, so
// walking it writes the user's home directory layout, their private repository names
// and their personal forge hostname straight into corpus.sig.json. See opaqueKind.
func TestOpaque_WorkspacesNeverBecomesAPath(t *testing.T) {
	if got := opaqueKind("client_metadata.x-codex-turn-metadata{}.workspaces"); got != "user-keyed-map" {
		t.Errorf("opaqueKind(workspaces) = %q, want user-keyed-map", got)
	}

	ep := &EventProfile{Paths: map[string]*Path{}}
	var v any
	if err := json.Unmarshal([]byte(`{"workspaces":{
		"/Users/someone/repos/private-thing":{
			"associated_remote_urls":{"origin":"git@github.com:someone/private-thing.git"},
			"has_changes":true}}}`), &v); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	walk(ep, "client_metadata.x-codex-turn-metadata{}", v)

	for path, p := range ep.Paths {
		if strings.Contains(path, "/Users/") {
			t.Errorf("a filesystem path became a signature path: %q", path)
		}
		if p.Values == nil {
			continue
		}
		for v := range p.Values.Counts {
			if strings.Contains(v, "github.com") || strings.Contains(v, "private-thing") {
				t.Errorf("path %q recorded the value %q", path, v)
			}
		}
	}
	if _, ok := ep.Paths["client_metadata.x-codex-turn-metadata{}.workspaces"]; !ok {
		t.Error("workspaces vanished entirely; presence and type must still be recorded " +
			"so it appearing or changing shape is still a finding")
	}
}

// TestOpaque_AttributionItemsNeverBecomesIdentifierPaths covers the same privacy
// boundary as workspaces, but for usage attribution. The object is keyed by
// per response item IDs, so walking it turns those IDs into path segments and can
// exhaust the profiler budget before protocol fields are compared.
func TestOpaque_AttributionItemsNeverBecomesIdentifierPaths(t *testing.T) {
	if got := opaqueKind("response.usage.attribution.items"); got != "identifier_map" {
		t.Errorf("opaqueKind(attribution items) = %q, want identifier_map", got)
	}

	ep := &EventProfile{Paths: map[string]*Path{}}
	var v any
	if err := json.Unmarshal([]byte(`{"response":{"usage":{"attribution":{"items":{
		"rs_private_identifier":{"input_tokens":10,"output_tokens":2},
		"amsg_private_identifier":{"cache_write_tokens":4}}}}}}`), &v); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	walk(ep, "", v)

	for path := range ep.Paths {
		if strings.Contains(path, "private_identifier") {
			t.Errorf("an attribution identifier became a signature path: %q", path)
		}
	}
	if _, ok := ep.Paths["response.usage.attribution.items"]; !ok {
		t.Error("attribution items vanished entirely; presence and type must still be recorded")
	}
}
