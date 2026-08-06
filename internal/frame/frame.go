// Package frame types the raw records codex-lb writes to its conversation archive.
//
// Each line is one websocket frame. The interesting content is double-encoded: the
// outer record carries routing metadata, and payload.text is a JSON *string* holding
// the actual Codex protocol event, which must be parsed separately.
package frame

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

// Direction of a websocket frame.
const (
	ToServer = "codex_to_server"
	ToCodex  = "server_to_codex"
)

// Record is one line of the archive.
type Record struct {
	AccountID  string    `json:"account_id"`
	Direction  string    `json:"direction"`
	Headers    Headers   `json:"headers"`
	Kind       string    `json:"kind"`
	Method     string    `json:"method"`
	Payload    Payload   `json:"payload"`
	RequestID  string    `json:"request_id"`
	StatusCode *int      `json:"status_code"`
	Timestamp  time.Time `json:"timestamp"`
	Transport  string    `json:"transport"`
	URL        string    `json:"url"`
	Extra      Extra     `json:"extra"`
}

// Headers is the captured request header set, keyed by LOWERCASE name.
//
// codex-lb preserves whatever casing the client sent, and that casing is not stable:
// the websocket family sends "chatgpt-account-id" while the CLI family sends
// "ChatGPT-Account-Id", and "Authorization" is capitalised everywhere. A plain map
// lookup therefore reads as absent for whichever spelling you did not guess, silently
// and with no error. Normalising once on decode removes the whole class of bug.
type Headers map[string]string

// UnmarshalJSON lowercases every header name.
func (h *Headers) UnmarshalJSON(b []byte) error {
	var raw map[string]string
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	out := make(Headers, len(raw))
	for k, v := range raw {
		out[strings.ToLower(k)] = v
	}
	*h = out
	return nil
}

// Payload wraps the inner protocol event, which arrives as a JSON string.
type Payload struct {
	Text string `json:"text"`
}

// Websocket frame types, from the archive's extra block.
const (
	FrameText  = "text"
	FrameError = "error"
	FrameClose = "close"
)

// Extra carries connection-level events. Nothing here appears in payload.text, so a
// decoder that ignores it cannot see a websocket close or a transport-level error at
// all - which is how close code 1012 (service restart) stayed invisible.
type Extra struct {
	FrameType string `json:"frame_type"`
	CloseCode *int   `json:"close_code"`
}

// Record families. These are NOT distinguishable by the record's own transport field,
// which reads "websocket" for all of them.
const (
	// FamilyWebsocket is the Codex TUI over a multiplexed websocket: request_id is
	// "ws_<hex32>", and turn metadata rides in x-codex-turn-metadata.
	FamilyWebsocket = "websocket"
	// FamilyHTTP is the same client over per-request HTTP: request_id is a UUID
	// matching x-request-id, and x-codex-turn-state is prefixed "http_turn_".
	FamilyHTTP = "http"
	// FamilyProbe is codex-lb's own synthetic health check - originator codex_cli_rs,
	// a tiny model, and the prompt "say OK". Real user metrics must exclude it.
	FamilyProbe = "probe"
)

// OriginatorProbe identifies the synthetic health-check client.
const OriginatorProbe = "codex_cli_rs"

// Family classifies the record. Callers should label metrics with it rather than with
// Transport, which does not vary.
func (r *Record) Family() string {
	if r.Header(HdrOriginator) == OriginatorProbe {
		return FamilyProbe
	}
	if strings.HasPrefix(r.RequestID, "ws_") {
		return FamilyWebsocket
	}
	return FamilyHTTP
}

// Header names carrying identity and agent-tree structure. Authorization is already
// redacted by codex-lb's _redact_headers before the record is written.
const (
	HdrSession      = "session-id"
	HdrThread       = "thread-id"
	HdrParentThread = "x-codex-parent-thread-id"
	HdrSubagent     = "x-openai-subagent"
	HdrInstallation = "x-codex-installation-id"
	HdrWindow       = "x-codex-window-id"
	HdrTraceparent  = "traceparent"
	HdrOriginator   = "originator"
	HdrBetaFeatures = "x-codex-beta-features"
	HdrTurnMetadata = "x-codex-turn-metadata"
	HdrTurnState    = "x-codex-turn-state"
	HdrRequestID    = "x-request-id"
	HdrClientReqID  = "x-client-request-id"
	HdrVersion      = "version"
	HdrAccountID    = "chatgpt-account-id"
)

// Header returns a header value, tolerating absence. Names must be lowercase; see
// the Headers type.
func (r *Record) Header(name string) string { return r.Headers[name] }

// TurnMeta is the decoded x-codex-turn-metadata blob.
//
// IMPORTANT: prefer the copy nested in response.create's client_metadata over the one
// in the request HEADER. The header is written when the websocket opens and is never
// updated, so on a long-lived connection it reports a stale turn - measured on one
// hour of live traffic, 177 of 199 real turns carried a header saying
// request_kind="prewarm" with an empty turn_id while the inner copy had the true
// turn_id. Reading the header would mislabel 89% of turns as prewarm.
type TurnMeta struct {
	InstallationID string `json:"installation_id"`
	SessionID      string `json:"session_id"`
	ThreadID       string `json:"thread_id"`
	TurnID         string `json:"turn_id"`
	WindowID       string `json:"window_id"`
	// RequestKind is "turn" for real work or "prewarm" for a speculative warmup that
	// does no engine work and reports no counters.
	RequestKind string `json:"request_kind"`
	// ThreadSource is "user" or "subagent".
	ThreadSource        string `json:"thread_source"`
	Sandbox             string `json:"sandbox"`
	ParentThreadID      string `json:"parent_thread_id"`
	ForkedFromThreadID  string `json:"forked_from_thread_id"`
	SubagentKind        string `json:"subagent_kind"`
	TurnStartedAtUnixMs int64  `json:"turn_started_at_unix_ms"`
}

// Request kinds.
const (
	KindTurn    = "turn"
	KindPrewarm = "prewarm"
	// KindCompaction is the server compacting the thread's context. It does real
	// engine work but is not a user turn, so counting it as one overstates turn rates
	// and understates cost per turn.
	KindCompaction = "compaction"
)

// IsTransport reports whether this frame is a websocket-level event rather than a
// protocol message. Their payload.text is PLAIN TEXT, not JSON - "no close frame
// received or sent", "received 1012 (service restart); then sent 1012" - so
// ParseEvent rejects them and they are invisible to any payload-only decoder.
func (r *Record) IsTransport() bool {
	return r.Extra.FrameType == FrameError || r.Extra.FrameType == FrameClose
}

// ParseTurnMeta decodes a turn-metadata blob, tolerating absence and malformation.
func ParseTurnMeta(s string) (TurnMeta, bool) {
	if s == "" {
		return TurnMeta{}, false
	}
	var m TurnMeta
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return TurnMeta{}, false
	}
	return m, true
}

// TurnStart converts TurnStartedAtUnixMs into a time, reporting whether it was set.
// This is the client's own turn start, so it measures the turn end to end rather than
// from when the server began work.
func (m TurnMeta) TurnStart() (time.Time, bool) {
	if m.TurnStartedAtUnixMs <= 0 {
		return time.Time{}, false
	}
	return time.UnixMilli(m.TurnStartedAtUnixMs).UTC(), true
}

// IsSubagent reports whether this frame belongs to a spawned sub-agent thread
// rather than the user's main thread.
func (r *Record) IsSubagent() bool { return r.Headers[HdrParentThread] != "" }

// Trace splits the W3C traceparent into its trace and span ids. Returns empty
// strings when the header is missing or malformed.
//
// Note this is the *client's* traceparent for the websocket connection, so it is
// constant across every frame of a connection rather than per-response.
func (r *Record) Trace() (traceID, spanID string) {
	parts := strings.Split(r.Headers[HdrTraceparent], "-")
	if len(parts) != 4 {
		return "", ""
	}
	return parts[1], parts[2]
}

// Event is the inner Codex protocol event from payload.text.
type Event struct {
	Type string
	// Raw is the undecoded event, for handlers that need fields beyond Type.
	Raw json.RawMessage
}

// typeProbe pulls just the discriminator so we avoid fully decoding the ~87% of
// frames that are streaming deltas we only need to count.
type typeProbe struct {
	Type string `json:"type"`
}

// ParseEvent decodes payload.text. A frame whose payload is not JSON yields ok=false
// rather than an error: the archive is a wire capture and may contain anything.
func (r *Record) ParseEvent() (Event, bool) {
	if r.Payload.Text == "" {
		return Event{}, false
	}
	var p typeProbe
	raw := json.RawMessage(r.Payload.Text)
	if err := json.Unmarshal(raw, &p); err != nil {
		return Event{}, false
	}
	return Event{Type: p.Type, Raw: raw}, true
}

// Event type discriminators. The delta types dominate by volume and carry no
// per-frame value beyond their count.
const (
	EvResponseCreate     = "response.create"
	EvResponseCreated    = "response.created"
	EvResponseInProgress = "response.in_progress"
	EvResponseCompleted  = "response.completed"
	EvOutputItemAdded    = "response.output_item.added"
	EvOutputItemDone     = "response.output_item.done"
	EvRateLimits         = "codex.rate_limits"
	EvResponseMetadata   = "codex.response.metadata"
	EvWebsocketTiming    = "responsesapi.websocket_timing"

	// EvError terminates a response without a response.completed frame. Observed
	// carrying upstream overload, the 60-minute websocket connection cap, and
	// protocol desync - all worth alerting on, and all previously invisible.
	EvError = "error"

	EvOutputTextDelta      = "response.output_text.delta"
	EvCustomToolInputDelta = "response.custom_tool_call_input.delta"
	EvFunctionArgsDelta    = "response.function_call_arguments.delta"
	EvCustomToolInputDone  = "response.custom_tool_call_input.done"
	EvFunctionArgsDone     = "response.function_call_arguments.done"
	EvOutputTextDone       = "response.output_text.done"
	EvContentPartAdded     = "response.content_part.added"
	EvContentPartDone      = "response.content_part.done"
)

// IsDelta reports whether an event is streaming chatter. These are counted, never
// stored: they were 87% of frames in the captured sample.
func IsDelta(t string) bool {
	switch t {
	case EvOutputTextDelta, EvCustomToolInputDelta, EvFunctionArgsDelta:
		return true
	}
	return false
}

// Decode unmarshals the raw event into v.
func (e Event) Decode(v any) error { return json.Unmarshal(e.Raw, v) }

// Lines decodes the JSON records in a decompressed archive chunk, calling fn for
// each. The stream decoder tolerates the records being newline-delimited or not.
func Lines(data []byte, fn func(*Record) error) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	for {
		var r Record
		if err := dec.Decode(&r); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if err := fn(&r); err != nil {
			return err
		}
	}
}
