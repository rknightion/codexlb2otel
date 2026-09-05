package loki

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/rknightion/codexlb2otel/internal/attr"
	"github.com/rknightion/codexlb2otel/internal/sink"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

// truncMarkerFmt is appended to a content field this sink cut down to fit
// MaxLineBytes. It carries the ORIGINAL length so a reader never has to guess how
// much was lost - unlike Loki's own line_too_long, which discards the whole line
// and leaves nothing behind at all.
const truncMarkerFmt = "…[truncated by codexlb2otel loki sink; original %d chars]"

// buildLines turns one reduced Turn into the Loki lines it produces: zero or more
// per record type, one line per array element for the six content-bearing types.
//
// This is where issue #6's central invariant is enforced STRUCTURALLY rather than
// by convention. turnLine below is built from a copy of Turn with every content
// slice nilled out before marshaling, so nothing those slices hold - however large -
// can ever be counted against the turn-metadata line's own budget. A turn whose tool
// output is 10x over budget still produces a turn-metadata line of a few hundred
// bytes, because that line was never given the tool output to lose in the first
// place.
func buildLines(t *turn.Turn, guard *attr.Guard, serviceName string, labelKeys []string, maxLineBytes int, enabled map[string]bool, rej rejecter) []outLine {
	var out []outLine
	baseMeta := guard.Metadata(t, labelKeys)

	labelCache := map[string][]attr.KV{}
	labelsFor := func(recordType string) []attr.KV {
		if l, ok := labelCache[recordType]; ok {
			return l
		}
		l := guard.Labels(t, serviceName, recordType, labelKeys)
		labelCache[recordType] = l
		return l
	}

	emit := func(recordType string, ts time.Time, body []byte, ok bool, extra ...attr.KV) {
		if !ok {
			// Nothing left to cut without corrupting the JSON envelope. This is the
			// same outcome Loki's own max_line_size would produce - the line is
			// gone - just decided locally instead of discovered via a 400, so it is
			// counted the same way a server-reported rejection is.
			rej.addRejected(sink.ReasonLineTooLong, 1)
			return
		}
		md := baseMeta
		if len(extra) > 0 {
			md = guard.With(baseMeta, extra...)
		}
		out = append(out, outLine{
			recordType: recordType,
			ts:         safeTS(ts),
			labels:     labelsFor(recordType),
			metadata:   md,
			body:       body,
		})
	}

	if isEnabled(enabled, attr.RecordTurn) {
		body, ok := turnLine(t, maxLineBytes)
		emit(attr.RecordTurn, turnTS(t), body, ok)
	}

	// Prompts and instructions share one source array (Turn.Prompts) but are two
	// record types - see the RecordInstructions doc comment in internal/attr/names.go
	// for why the 67KB system preamble needs its own line kind rather than counting
	// as just another prompt.
	if isEnabled(enabled, attr.RecordPrompt) || isEnabled(enabled, attr.RecordInstructions) {
		for _, p := range t.Prompts {
			rt := attr.RecordPrompt
			if p.Role == "instructions" {
				rt = attr.RecordInstructions
			}
			if !isEnabled(enabled, rt) {
				continue
			}
			body, ok, err := fitLine(maxLineBytes, p.Text, func(text string) ([]byte, error) {
				pp := p
				pp.Text = text
				return json.Marshal(pp)
			})
			emit(rt, inputTS(t), body, ok && err == nil, contentMetadata(p.Ordinal, p.ItemID, p.CapturedAt, p.Provenance)...)
		}
	}

	if isEnabled(enabled, attr.RecordMessage) {
		for _, m := range t.Messages {
			body, ok, err := fitLine(maxLineBytes, m.Text, func(text string) ([]byte, error) {
				mm := m
				mm.Text = text
				return json.Marshal(mm)
			})
			emit(attr.RecordMessage, outputTS(t), body, ok && err == nil, contentMetadata(m.Ordinal, m.ItemID, m.CapturedAt, m.Provenance)...)
		}
	}

	if isEnabled(enabled, attr.RecordToolCall) {
		for _, tc := range t.ToolCalls {
			body, ok := toolCallLine(tc, maxLineBytes)
			// ToolName is not on the Turn-level attribute contract (a turn can have
			// several tool calls; attr.Field.Of returns one value per turn) - it is
			// exactly the one-off, not-Turn-derived case attr.With exists for.
			extra := append([]attr.KV{{Key: attr.ToolName, Value: tc.Name}}, contentMetadata(tc.Ordinal, tc.ItemID, tc.CapturedAt, tc.Provenance)...)
			emit(attr.RecordToolCall, outputTS(t), body, ok, extra...)
		}
	}

	if isEnabled(enabled, attr.RecordToolOutput) {
		for _, to := range t.ToolOutputs {
			body, ok, err := fitLine(maxLineBytes, to.Text, func(text string) ([]byte, error) {
				oo := to
				oo.Text = text
				return json.Marshal(oo)
			})
			// Tool outputs are recovered from response.create alongside prompts -
			// see turn.ToolOutput's doc comment - so they are input-side content
			// timestamped the same way.
			emit(attr.RecordToolOutput, inputTS(t), body, ok && err == nil, contentMetadata(to.Ordinal, to.ItemID, to.CapturedAt, to.Provenance)...)
		}
	}

	if isEnabled(enabled, attr.RecordAgentMessage) {
		for _, am := range t.AgentMessages {
			body, ok, err := fitLine(maxLineBytes, am.Text, func(text string) ([]byte, error) {
				aa := am
				aa.Text = text
				return json.Marshal(aa)
			})
			// Agent-to-agent messages are captured by captureInput from the same
			// input items as prompts and tool outputs - input-side, not output-side.
			emit(attr.RecordAgentMessage, inputTS(t), body, ok && err == nil, contentMetadata(am.Ordinal, am.ItemID, am.CapturedAt, am.Provenance)...)
		}
	}

	if isEnabled(enabled, attr.RecordTransport) && hasTransportSignal(t) {
		body, ok := transportLine(t, maxLineBytes)
		emit(attr.RecordTransport, lifecycleTS(t), body, ok)
	}

	if isEnabled(enabled, attr.RecordError) && hasErrorSignal(t) {
		body, ok := errorLine(t, maxLineBytes)
		emit(attr.RecordError, lifecycleTS(t), body, ok)
	}

	// Content records retain their established inputTS/outputTS event timestamp. If
	// two records share that timestamp, Ordinal provides the archive-capture order;
	// it is also sent as structured metadata so a Loki query can apply the same tie
	// break after ingestion. CapturedAt is deliberately separate: it is when the
	// archive frame was observed, not the record event time used for this timestamp.
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].ts.Equal(out[j].ts) {
			return out[i].ts.Before(out[j].ts)
		}
		return contentOrdinal(out[i].metadata) < contentOrdinal(out[j].metadata)
	})
	return out
}

// contentMetadata is structured metadata for one content item. These describe
// archive observation and ordering, never a tool's execution or authorisation.
func contentMetadata(ordinal int, itemID string, capturedAt time.Time, provenance string) []attr.KV {
	if provenance == "" {
		provenance = "unknown"
	}
	return []attr.KV{
		{Key: attr.ContentOrdinal, Value: strconv.Itoa(ordinal)},
		{Key: attr.ContentItemID, Value: itemID},
		{Key: attr.ContentCapturedAt, Value: capturedAt.UTC().Format(time.RFC3339Nano)},
		{Key: attr.ContentProvenance, Value: provenance},
	}
}

func contentOrdinal(metadata []attr.KV) int {
	for _, kv := range metadata {
		if kv.Key == attr.ContentOrdinal {
			ordinal, err := strconv.Atoi(kv.Value)
			if err == nil {
				return ordinal
			}
		}
	}
	return 0
}

// toolCallLine preserves the inner JSON contract when a Loki line budget forces an
// input replacement. fitLine is appropriate for free text, but slicing an argument
// document produces invalid JSON. The replacement is itself a valid JSON string and
// InputTruncated records that this sink, after reduction, made the replacement.
func toolCallLine(tc turn.ToolCall, budget int) ([]byte, bool) {
	body, err := json.Marshal(tc)
	if err != nil {
		return nil, false
	}
	if len(body) <= budget {
		return body, true
	}
	cc := tc
	if cc.InputChars == 0 {
		cc.InputChars = len(tc.Input)
	}
	cc.InputTruncated = true
	cc.Input = strconv.Quote(fmt.Sprintf("[truncated by codexlb2otel loki sink; original %d chars]", len(tc.Input)))
	body, err = json.Marshal(cc)
	if err != nil {
		return nil, false
	}
	return body, len(body) <= budget
}

func isEnabled(enabled map[string]bool, recordType string) bool {
	return enabled == nil || enabled[recordType]
}

func hasTransportSignal(t *turn.Turn) bool {
	return t.Status == "transport" || t.CloseCode != nil || t.FrameErrors > 0 || t.TransportEvent != ""
}

func hasErrorSignal(t *turn.Turn) bool {
	return t.Status == "error" || t.ErrorType != "" || t.ErrorCode != "" || t.ErrorMessage != ""
}

// turnLine marshals the turn-metadata record: every scalar field on Turn, none of
// its five content arrays.
func turnLine(t *turn.Turn, budget int) ([]byte, bool) {
	cp := *t
	cp.ToolCalls, cp.Messages, cp.Prompts, cp.ToolOutputs, cp.AgentMessages = nil, nil, nil, nil, nil
	body, err := json.Marshal(cp)
	if err != nil {
		return nil, false
	}
	if len(body) <= budget {
		return body, true
	}
	// Defensive only: what remains after stripping the content arrays is scalars
	// plus two small maps bounded by distinct models and item types, not by user
	// content, and should never approach the budget. If it somehow does, drop those
	// two maps and try once more rather than hand-truncating metadata JSON, which
	// would either corrupt it or require re-implementing this same fit logic for a
	// shape with no single field to shrink.
	cp.ExtraRateLimits = nil
	cp.ItemCounts = nil
	body, err = json.Marshal(cp)
	if err != nil {
		return nil, false
	}
	return body, len(body) <= budget
}

// transportBody is the RecordTransport line: just the fields that describe a
// websocket lifecycle event, not the whole turn.
type transportBody struct {
	RequestID      string `json:"request_id,omitempty"`
	ThreadID       string `json:"thread_id,omitempty"`
	Status         string `json:"status,omitempty"`
	CloseCode      *int   `json:"close_code,omitempty"`
	FrameErrors    int    `json:"frame_errors,omitempty"`
	TransportEvent string `json:"transport_event,omitempty"`
}

func transportLine(t *turn.Turn, budget int) ([]byte, bool) {
	body, err := json.Marshal(transportBody{
		RequestID: t.RequestID, ThreadID: t.ThreadID, Status: t.Status,
		CloseCode: t.CloseCode, FrameErrors: t.FrameErrors, TransportEvent: t.TransportEvent,
	})
	if err != nil {
		return nil, false
	}
	return body, len(body) <= budget
}

// errorBody is the RecordError line.
type errorBody struct {
	RequestID    string `json:"request_id,omitempty"`
	ThreadID     string `json:"thread_id,omitempty"`
	ErrorType    string `json:"error_type,omitempty"`
	ErrorCode    string `json:"error_code,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
}

func errorLine(t *turn.Turn, budget int) ([]byte, bool) {
	body, ok, err := fitLine(budget, t.ErrorMessage, func(msg string) ([]byte, error) {
		return json.Marshal(errorBody{
			RequestID: t.RequestID, ThreadID: t.ThreadID,
			ErrorType: t.ErrorType, ErrorCode: t.ErrorCode, ErrorMessage: msg,
		})
	})
	if err != nil {
		return nil, false
	}
	return body, ok
}

// fitLine marshals a line via build, shrinking the free-text piece `full` until the
// marshaled result fits within budget bytes - truncating deliberately, with an
// explicit marker, before Loki's own max_line_size ever gets a chance to discard the
// line outright. Reports (body, ok, err): ok is false when even an empty text plus
// the marker does not fit, meaning the caller has nothing left to ship.
//
// A binary search over the byte length of full is used rather than computing a
// budget for the text and cutting once, because JSON escaping is not 1:1 with
// source bytes - command output routinely contains quotes, backslashes and control
// characters that expand under encoding/json, and only re-marshaling and measuring
// the actual result is guaranteed correct against that.
func fitLine(budget int, full string, build func(text string) ([]byte, error)) (body []byte, ok bool, err error) {
	body, err = build(full)
	if err != nil {
		return nil, false, err
	}
	if len(body) <= budget {
		return body, true, nil
	}

	marker := fmt.Sprintf(truncMarkerFmt, len(full))
	lo, hi := 0, len(full)
	var best []byte
	for lo <= hi {
		mid := (lo + hi) / 2
		cand := safeCut(full, mid) + marker
		b, buildErr := build(cand)
		if buildErr != nil {
			return nil, false, buildErr
		}
		if len(b) <= budget {
			best = b
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return best, best != nil, nil
}

// safeCut cuts s to at most n bytes without splitting a UTF-8 rune in half, which a
// blind s[:n] can do to any non-ASCII content (every continuation byte in UTF-8 has
// its top two bits set to 10, which is what the scan below walks back past).
func safeCut(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if n >= len(s) {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}
