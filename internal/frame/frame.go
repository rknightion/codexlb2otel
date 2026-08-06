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
	AccountID  string            `json:"account_id"`
	Direction  string            `json:"direction"`
	Headers    map[string]string `json:"headers"`
	Kind       string            `json:"kind"`
	Method     string            `json:"method"`
	Payload    Payload           `json:"payload"`
	RequestID  string            `json:"request_id"`
	StatusCode *int              `json:"status_code"`
	Timestamp  time.Time         `json:"timestamp"`
	Transport  string            `json:"transport"`
	URL        string            `json:"url"`
}

// Payload wraps the inner protocol event, which arrives as a JSON string.
type Payload struct {
	Text string `json:"text"`
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
)

// Header returns a header value, tolerating absence.
func (r *Record) Header(name string) string { return r.Headers[name] }

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
