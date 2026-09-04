package frame

import (
	"encoding/json"
	"testing"
)

func TestPayloadTextAcceptsStringAndObjectShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		blob string
		want string
	}{
		{name: "protocol string", blob: `{"payload":{"text":"{\"type\":\"response.created\"}"}}`, want: `{"type":"response.created"}`},
		{name: "HTTP request object", blob: `{"payload":{"text":{"model":"test","stream":true}}}`, want: `{"model":"test","stream":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var r Record
			if err := json.Unmarshal([]byte(tc.blob), &r); err != nil {
				t.Fatal(err)
			}
			if r.Payload.Text != tc.want {
				t.Fatalf("payload text = %q, want %q", r.Payload.Text, tc.want)
			}
		})
	}
}

func TestParseEventAcceptsSSEDataEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
	}{
		{name: "CRLF", text: "event: response.completed\r\n" +
			"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\r\n\r\n"},
		{name: "BOM and CR", text: "\uFEFFevent: response.completed\r" +
			"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\r\r"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := Record{Payload: Payload{Text: tc.text}}
			ev, ok := rec.ParseEvent()
			if !ok {
				t.Fatal("SSE data envelope was not decoded")
			}
			if ev.Type != EvResponseCompleted {
				t.Fatalf("event type = %q, want %q", ev.Type, EvResponseCompleted)
			}
		})
	}
}

func TestPayloadErrorEnvelopeBecomesProtocolError(t *testing.T) {
	var rec Record
	blob := `{"kind":"responses","direction":"server_to_codex","transport":"http",` +
		`"request_id":"00000000-0000-0000-0000-000000000001","status_code":429,` +
		`"payload":{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded",` +
		`"message":"synthetic fixture"}}}`
	if err := json.Unmarshal([]byte(blob), &rec); err != nil {
		t.Fatal(err)
	}

	ev, ok := rec.ParseEvent()
	if !ok {
		t.Fatal("HTTP error payload was not decoded")
	}
	if ev.Type != EvError {
		t.Fatalf("event type = %q, want %q", ev.Type, EvError)
	}
	var got struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := ev.Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Type != "rate_limit_error" || got.Error.Code != "rate_limit_exceeded" {
		t.Fatalf("error identity = %#v", got.Error)
	}
}

// Header casing is not stable in the capture: the websocket family sends
// "chatgpt-account-id", the CLI family sends "ChatGPT-Account-Id", and Authorization
// is capitalised everywhere. A case-sensitive lookup reads as absent with no error,
// so identity fields silently vanish for whichever family you did not test against.
func TestHeadersAreCaseInsensitive(t *testing.T) {
	var r Record
	blob := `{"headers":{"Authorization":"[redacted]","ChatGPT-Account-Id":"acct",
	  "Session-Id":"sess","x-codex-parent-thread-id":"parent"}}`
	if err := json.Unmarshal([]byte(blob), &r); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, want string }{
		{HdrAccountID, "acct"},
		{HdrSession, "sess"},
		{HdrParentThread, "parent"},
		{"authorization", "[redacted]"},
	} {
		if got := r.Header(c.name); got != c.want {
			t.Errorf("Header(%q) = %q, want %q", c.name, got, c.want)
		}
	}
	if !r.IsSubagent() {
		t.Error("IsSubagent() = false despite a parent-thread header")
	}
}

// Transport reads "websocket" for every record in the capture including the HTTP ones,
// so Family must not be derived from it.
func TestFamilyIgnoresTransport(t *testing.T) {
	cases := []struct {
		name      string
		requestID string
		orig      string
		want      string
	}{
		{"websocket id", "ws_fe13556d952440008d5ada95b128530a", "codex-tui", FamilyWebsocket},
		{"uuid id", "96e0dbd1-48cf-44f1-89a3-c76b2de7488e", "codex-tui", FamilyHTTP},
		{"health probe", "e98e207b-f982-4396-b2b6-378e20bee028", OriginatorProbe, FamilyProbe},
		// A probe is a probe whatever transport it arrives on.
		{"probe over websocket", "ws_abc", OriginatorProbe, FamilyProbe},
		// An absent id says nothing about the family, so it must not be answered with a
		// guess. This case asserted FamilyHTTP until 2026-08-07, which meant the
		// connection-level close and error frames - which carry no request_id, and which
		// exist precisely to report a websocket dying - were reported as an HTTP-family
		// error rate. Every one of the seven such turns in the corpus was mislabelled.
		{"absent id", "", "codex-tui", FamilyUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Record{
				RequestID: c.requestID,
				Transport: "websocket", // always, for every family
				Headers:   Headers{HdrOriginator: c.orig},
			}
			if got := r.Family(); got != c.want {
				t.Errorf("Family() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestParseTurnMeta(t *testing.T) {
	const blob = `{"installation_id":"inst","session_id":"sess","thread_id":"thread",
	  "turn_id":"019fd93c-5693-7631-a7e4-50efe8915dba","window_id":"win:0",
	  "request_kind":"turn","thread_source":"subagent","sandbox":"none",
	  "parent_thread_id":"parent","subagent_kind":"collab_spawn",
	  "turn_started_at_unix_ms":1786057213397,
	  "code_mode_tool_names":{"apply_patch":{"name":"apply_patch","namespace":null}}}`

	m, ok := ParseTurnMeta(blob)
	if !ok {
		t.Fatal("ParseTurnMeta reported failure on a valid blob")
	}
	if m.TurnID == "" || m.RequestKind != KindTurn || m.ThreadSource != "subagent" {
		t.Errorf("turn meta not decoded: %+v", m)
	}
	// code_mode_tool_names is a large irrelevant subtree that must not break decoding.
	if m.SubagentKind != "collab_spawn" || m.ParentThreadID != "parent" {
		t.Errorf("fields after the tool-name subtree were lost: %+v", m)
	}
	start, ok := m.TurnStart()
	if !ok || start.UnixMilli() != 1786057213397 {
		t.Errorf("TurnStart() = %v, %v", start, ok)
	}

	// Absence and malformation must both be non-fatal: this is a wire capture.
	for _, bad := range []string{"", "{", "null", "not json"} {
		if _, ok := ParseTurnMeta(bad); ok && bad != "null" {
			t.Errorf("ParseTurnMeta(%q) reported success", bad)
		}
	}
	// A prewarm legitimately carries an empty turn id rather than omitting the field.
	m, _ = ParseTurnMeta(`{"turn_id":"","request_kind":"prewarm"}`)
	if m.TurnID != "" || m.RequestKind != KindPrewarm {
		t.Errorf("prewarm meta: %+v", m)
	}
	if _, ok := m.TurnStart(); ok {
		t.Error("TurnStart() reported a start with no turn_started_at_unix_ms")
	}
}

// Transport-level frames carry no payload, so a decoder that only reads payloads
// cannot see a connection close at all.
func TestExtraCarriesConnectionLifecycle(t *testing.T) {
	var r Record
	if err := json.Unmarshal([]byte(
		`{"extra":{"frame_type":"close","close_code":1012},"payload":{"text":""}}`), &r); err != nil {
		t.Fatal(err)
	}
	if r.Extra.FrameType != FrameClose {
		t.Errorf("frame type = %q, want %q", r.Extra.FrameType, FrameClose)
	}
	if r.Extra.CloseCode == nil || *r.Extra.CloseCode != 1012 {
		t.Errorf("close code = %v, want 1012", r.Extra.CloseCode)
	}
	// A null close code must stay distinguishable from code 0.
	if err := json.Unmarshal([]byte(`{"extra":{"frame_type":"error","close_code":null}}`), &r); err != nil {
		t.Fatal(err)
	}
	if r.Extra.CloseCode != nil {
		t.Errorf("null close code decoded as %v", *r.Extra.CloseCode)
	}
}
