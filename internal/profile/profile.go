// Package profile induces the shape of an archive without assuming a schema.
//
// It exists because the archive is a wire capture of somebody else's protocol: the
// record types, event types and field sets all change without notice, and anything
// the typed decoders in internal/frame do not name is silently dropped. Running this
// over a fresh capture answers "what are we throwing away, and what is new?".
package profile

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Caps bound memory on adversarial input. A capped histogram still reports how many
// distinct values it saw, so hitting a cap is itself a finding rather than a silent
// truncation.
const (
	maxDistinctValues = 200 // per histogram
	maxPathsPerType   = 600 // per event type
	sampleDepth       = 400 // full schema induction per event type, then count only
	maxSampleLen      = 220 // truncation for example values
)

// Hist counts the values of one low-cardinality field.
type Hist struct {
	Counts   map[string]int64 `json:"counts"`
	Distinct int64            `json:"distinct"`
	Capped   bool             `json:"capped,omitempty"`
}

func (h *Hist) add(v string) {
	if h.Counts == nil {
		h.Counts = map[string]int64{}
	}
	if _, ok := h.Counts[v]; !ok {
		h.Distinct++
		if len(h.Counts) >= maxDistinctValues {
			h.Capped = true
			return
		}
	}
	h.Counts[v]++
}

func (h *Hist) merge(o *Hist) {
	for v, n := range o.Counts {
		if h.Counts == nil {
			h.Counts = map[string]int64{}
		}
		if _, ok := h.Counts[v]; !ok {
			h.Distinct++
			if len(h.Counts) >= maxDistinctValues {
				h.Capped = true
				continue
			}
		}
		h.Counts[v] += n
	}
	if o.Capped {
		h.Capped = true
	}
}

// Path is one JSON path observed inside an event, with the types it took.
type Path struct {
	Count  int64            `json:"count"`
	Types  map[string]int64 `json:"types"`
	Sample string           `json:"sample,omitempty"`
	Values *Hist            `json:"values,omitempty"` // only for low-cardinality strings
}

// EventProfile is the induced schema of one inner protocol event type.
type EventProfile struct {
	Count       int64            `json:"count"`
	Sampled     int64            `json:"sampled"`
	Paths       map[string]*Path `json:"paths"`
	PathsCapped bool             `json:"paths_capped,omitempty"`
}

// FileProfile is the per-file summary, used to spot rotation and replacement.
type FileProfile struct {
	Name    string    `json:"name"`
	Bytes   int64     `json:"bytes"`
	Members int64     `json:"members"`
	Lines   int64     `json:"lines"`
	FirstTS time.Time `json:"first_ts"`
	LastTS  time.Time `json:"last_ts"`
	Bad     int64     `json:"undecodable_lines,omitempty"`
}

// Profile is the whole induced shape.
type Profile struct {
	mu sync.Mutex

	Files []FileProfile `json:"files"`

	Lines   int64 `json:"lines"`
	Bad     int64 `json:"undecodable_lines"`
	Members int64 `json:"members"`
	Bytes   int64 `json:"decompressed_bytes"`

	// RecordKeys is every top-level key seen on a record. A key here that internal/frame
	// does not name is data being dropped on the floor.
	RecordKeys map[string]int64 `json:"record_keys"`

	// RecordFields histograms the low-cardinality record fields.
	RecordFields map[string]*Hist `json:"record_fields"`

	// Headers maps header name to a value histogram. Header names are the routing
	// surface, so a new one appearing is worth noticing.
	Headers map[string]*Hist `json:"headers"`

	// PayloadShapes counts the JSON shape of the payload field itself, since not every
	// record is a {"text": "..."} websocket frame.
	PayloadShapes map[string]int64 `json:"payload_shapes"`

	// PayloadFraming counts how payload.text is framed on the wire.
	//
	// Today it is a bare JSON object for every record, because codex-lb multiplexes
	// the Responses API over a websocket. The same API is also served over HTTP with
	// server-sent events, and if codex-lb ever captures that path the payload becomes
	// "data: {...}" lines instead - a change the record's transport field would NOT
	// show, since it already reads "websocket" for the HTTP family too. Classifying
	// the framing is therefore the only way a transport change surfaces at all.
	PayloadFraming map[string]int64 `json:"payload_framing"`

	// PayloadUnparsed counts payloads whose text was not JSON, with samples.
	PayloadUnparsed int64    `json:"payload_text_not_json"`
	UnparsedSamples []string `json:"payload_text_not_json_samples,omitempty"`

	// Events is the induced schema per inner event type.
	Events map[string]*EventProfile `json:"events"`

	// RequestIDShapes classifies request_id, which is the reducer's join key.
	RequestIDShapes map[string]int64 `json:"request_id_shapes"`

	// Cross-cut: which record kinds carry which event types.
	KindEvents map[string]map[string]int64 `json:"kind_events"`

	// Samples holds one whole record per (kind, direction) combination that is not
	// the well-understood websocket responses stream.
	Samples map[string]string `json:"samples,omitempty"`
}

// New builds an empty Profile.
func New() *Profile {
	return &Profile{
		RecordKeys:      map[string]int64{},
		RecordFields:    map[string]*Hist{},
		Headers:         map[string]*Hist{},
		PayloadShapes:   map[string]int64{},
		PayloadFraming:  map[string]int64{},
		Events:          map[string]*EventProfile{},
		RequestIDShapes: map[string]int64{},
		KindEvents:      map[string]map[string]int64{},
		Samples:         map[string]string{},
	}
}

// lowCardFields are histogrammed by value. Anything not listed is either high
// cardinality (ids, urls with query strings) or free text.
var lowCardFields = map[string]bool{
	"direction": true, "kind": true, "method": true, "transport": true,
	"status_code": true, "url": true,
}

// headerValueFields are headers whose values are worth histogramming. The rest are
// identifiers, so only their presence is counted.
var headerValueFields = map[string]bool{
	"originator": true, "x-codex-beta-features": true, "user-agent": true,
	"content-type": true, "accept": true, "x-openai-subagent": true,
	"version": true, "openai-beta": true, "x-codex-turn-id": true,
}

// AddLine folds one archive line into the profile. Unparseable lines are counted, not
// fatal: this tool must survive exactly the input it exists to characterise.
func (p *Profile) AddLine(line []byte, fp *FileProfile) {
	var rec map[string]json.RawMessage
	if err := json.Unmarshal(line, &rec); err != nil {
		p.Bad++
		fp.Bad++
		return
	}
	p.Lines++
	fp.Lines++

	for k := range rec {
		p.RecordKeys[k]++
	}

	for k, raw := range rec {
		if lowCardFields[k] {
			p.field(k).add(scalar(raw))
		}
	}

	if ts, ok := rec["timestamp"]; ok {
		var s string
		if json.Unmarshal(ts, &s) == nil {
			if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
				if fp.FirstTS.IsZero() || t.Before(fp.FirstTS) {
					fp.FirstTS = t
				}
				if t.After(fp.LastTS) {
					fp.LastTS = t
				}
			}
		}
	}

	// extra is an open bag; flatten whatever is in it.
	if raw, ok := rec["extra"]; ok {
		var m map[string]any
		if json.Unmarshal(raw, &m) == nil {
			for k, v := range m {
				p.field("extra." + k).add(fmt.Sprint(v))
			}
		} else {
			p.field("extra").add(jsonKind(raw))
		}
	}

	var kind string
	if raw, ok := rec["kind"]; ok {
		kind = scalar(raw)
	}

	if raw, ok := rec["request_id"]; ok {
		p.RequestIDShapes[idShape(scalar(raw))]++
	}

	if raw, ok := rec["headers"]; ok {
		var h map[string]string
		if json.Unmarshal(raw, &h) == nil {
			for name, v := range h {
				name = strings.ToLower(name)
				hist := p.header(name)
				if headerValueFields[name] {
					hist.add(v)
				} else {
					hist.add("<set>")
				}
			}
		} else {
			p.PayloadShapes["headers:"+jsonKind(raw)]++
		}
	}

	p.sampleRecord(rec, kind, line)
	p.payload(rec, kind)
}

// payload characterises the payload field and, when it holds a JSON event, induces
// that event's schema.
func (p *Profile) payload(rec map[string]json.RawMessage, kind string) {
	raw, ok := rec["payload"]
	if !ok {
		p.PayloadShapes["<absent>"]++
		return
	}
	k := jsonKind(raw)
	if k != "object" {
		p.PayloadShapes[k]++
		return
	}

	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		p.PayloadShapes["object:unparseable"]++
		return
	}
	keys := make([]string, 0, len(obj))
	for kk := range obj {
		keys = append(keys, kk)
	}
	sort.Strings(keys)
	p.PayloadShapes["object{"+strings.Join(keys, ",")+"}"]++

	// The websocket shape: {"text": "<json string>"}.
	inner, ok := obj["text"]
	if !ok {
		// Non-websocket payloads carry their body directly. Induce it under a
		// synthetic type so it is not lost.
		p.induce("<payload:"+kind+">", raw)
		return
	}
	var s string
	if json.Unmarshal(inner, &s) != nil {
		p.PayloadShapes["text:not-a-string"]++
		return
	}
	p.PayloadFraming[framing(s)]++
	evRaw := json.RawMessage(s)
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(evRaw, &probe); err != nil {
		p.PayloadUnparsed++
		if len(p.UnparsedSamples) < 8 {
			p.UnparsedSamples = append(p.UnparsedSamples, truncate(s))
		}
		return
	}
	t := probe.Type
	if t == "" {
		t = "<no type field>"
	}
	if p.KindEvents[kind] == nil {
		p.KindEvents[kind] = map[string]int64{}
	}
	p.KindEvents[kind][t]++
	p.induce(t, evRaw)
}

// induce walks an event and records every path it contains, for the first sampleDepth
// occurrences of that type. Beyond that only the count advances - the deltas alone are
// ~87% of frames and their shape is settled after a handful.
func (p *Profile) induce(t string, raw json.RawMessage) {
	ep := p.Events[t]
	if ep == nil {
		ep = &EventProfile{Paths: map[string]*Path{}}
		p.Events[t] = ep
	}
	ep.Count++
	if ep.Sampled >= sampleDepth {
		return
	}
	ep.Sampled++

	var v any
	if json.Unmarshal(raw, &v) != nil {
		return
	}
	walk(ep, "", v)
}

func walk(ep *EventProfile, prefix string, v any) {
	switch x := v.(type) {
	case map[string]any:
		if prefix != "" {
			ep.note(prefix, "object", "")
		}
		for k, sub := range x {
			walk(ep, join(prefix, k), sub)
		}
	case []any:
		ep.note(prefix, "array", strconv.Itoa(len(x)))
		for _, sub := range x {
			walk(ep, prefix+"[]", sub)
		}
	case string:
		ep.note(prefix, "string", x)
	case float64:
		ep.note(prefix, "number", strconv.FormatFloat(x, 'g', -1, 64))
	case bool:
		ep.note(prefix, "bool", strconv.FormatBool(x))
	case nil:
		ep.note(prefix, "null", "")
	}
}

func (ep *EventProfile) note(path, kind, sample string) {
	if path == "" {
		return
	}
	pi := ep.Paths[path]
	if pi == nil {
		if len(ep.Paths) >= maxPathsPerType {
			ep.PathsCapped = true
			return
		}
		pi = &Path{Types: map[string]int64{}}
		ep.Paths[path] = pi
	}
	pi.Count++
	pi.Types[kind]++
	if pi.Sample == "" && sample != "" {
		pi.Sample = truncate(sample)
	}
	// Track values for short strings and bools: these are the candidate metric
	// attributes, and the histogram is what proves they are low cardinality.
	if (kind == "string" && len(sample) <= 48) || kind == "bool" {
		if pi.Values == nil {
			pi.Values = &Hist{}
		}
		pi.Values.add(sample)
	}
}

func (p *Profile) sampleRecord(rec map[string]json.RawMessage, kind string, line []byte) {
	dir := ""
	if raw, ok := rec["direction"]; ok {
		dir = scalar(raw)
	}
	transport := ""
	if raw, ok := rec["transport"]; ok {
		transport = scalar(raw)
	}
	key := transport + "/" + kind + "/" + dir
	if _, ok := p.Samples[key]; !ok && len(p.Samples) < 40 {
		p.Samples[key] = truncateN(string(line), 4000)
	}
}

func (p *Profile) field(k string) *Hist {
	h := p.RecordFields[k]
	if h == nil {
		h = &Hist{}
		p.RecordFields[k] = h
	}
	return h
}

func (p *Profile) header(k string) *Hist {
	h := p.Headers[k]
	if h == nil {
		h = &Hist{}
		p.Headers[k] = h
	}
	return h
}

// Merge folds another profile in, so files can be processed concurrently.
func (p *Profile) Merge(o *Profile) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.Files = append(p.Files, o.Files...)
	p.Lines += o.Lines
	p.Bad += o.Bad
	p.Members += o.Members
	p.Bytes += o.Bytes
	p.PayloadUnparsed += o.PayloadUnparsed

	mergeCounts(p.RecordKeys, o.RecordKeys)
	mergeCounts(p.PayloadShapes, o.PayloadShapes)
	mergeCounts(p.PayloadFraming, o.PayloadFraming)
	mergeCounts(p.RequestIDShapes, o.RequestIDShapes)
	for k, v := range o.RecordFields {
		p.field(k).merge(v)
	}
	for k, v := range o.Headers {
		p.header(k).merge(v)
	}
	for k, v := range o.KindEvents {
		if p.KindEvents[k] == nil {
			p.KindEvents[k] = map[string]int64{}
		}
		mergeCounts(p.KindEvents[k], v)
	}
	for k, v := range o.Samples {
		if _, ok := p.Samples[k]; !ok && len(p.Samples) < 40 {
			p.Samples[k] = v
		}
	}
	for _, s := range o.UnparsedSamples {
		if len(p.UnparsedSamples) < 8 {
			p.UnparsedSamples = append(p.UnparsedSamples, s)
		}
	}
	for t, oe := range o.Events {
		pe := p.Events[t]
		if pe == nil {
			pe = &EventProfile{Paths: map[string]*Path{}}
			p.Events[t] = pe
		}
		pe.Count += oe.Count
		pe.Sampled += oe.Sampled
		pe.PathsCapped = pe.PathsCapped || oe.PathsCapped
		for path, opi := range oe.Paths {
			ppi := pe.Paths[path]
			if ppi == nil {
				if len(pe.Paths) >= maxPathsPerType {
					pe.PathsCapped = true
					continue
				}
				ppi = &Path{Types: map[string]int64{}}
				pe.Paths[path] = ppi
			}
			ppi.Count += opi.Count
			for k, n := range opi.Types {
				ppi.Types[k] += n
			}
			if ppi.Sample == "" {
				ppi.Sample = opi.Sample
			}
			if opi.Values != nil {
				if ppi.Values == nil {
					ppi.Values = &Hist{}
				}
				ppi.Values.merge(opi.Values)
			}
		}
	}
}

func mergeCounts(dst, src map[string]int64) {
	for k, v := range src {
		dst[k] += v
	}
}

// Payload framing classes. Everything captured so far is JSONObject; the rest exist
// so that a change to the wire format is reported as a new bucket rather than as a
// flood of unparseable payloads with no explanation.
const (
	FramingJSONObject = "json-object"
	FramingJSONArray  = "json-array"
	FramingSSE        = "sse-event-stream"
	FramingJSONScalar = "json-scalar"
	FramingPlainText  = "plain-text"
	FramingEmpty      = "empty"
)

// framing classifies the wire framing of a payload body.
func framing(s string) string {
	t := strings.TrimLeft(s, " \t\r\n")
	switch {
	case t == "":
		return FramingEmpty
	case strings.HasPrefix(t, "data:"), strings.HasPrefix(t, "event:"),
		strings.HasPrefix(t, "id:"), strings.HasPrefix(t, "retry:"),
		strings.HasPrefix(t, ":"):
		// SSE field lines, per the WHATWG event-stream grammar. A leading ":" is a
		// comment, which is how most servers send their keepalive.
		return FramingSSE
	case strings.HasPrefix(t, "{"):
		return FramingJSONObject
	case strings.HasPrefix(t, "["):
		return FramingJSONArray
	case strings.HasPrefix(t, `"`), t == "true", t == "false", t == "null":
		return FramingJSONScalar
	default:
		return FramingPlainText
	}
}

// idShape classifies an identifier so a change of format shows up as a new bucket
// rather than as 100k distinct values.
func idShape(s string) string {
	switch {
	case s == "":
		return "<empty>"
	case strings.HasPrefix(s, "ws_"):
		return "ws_<hex32>"
	case strings.HasPrefix(s, "resp_"):
		return "resp_<...>"
	case isUUID(s):
		return "<uuid>"
	default:
		return "<other:len" + strconv.Itoa(len(s)) + ">"
	}
}

func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
				return false
			}
		}
	}
	return true
}

func jsonKind(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return "<empty>"
	}
	switch s[0] {
	case '{':
		return "object"
	case '[':
		return "array"
	case '"':
		return "string"
	case 'n':
		return "null"
	case 't', 'f':
		return "bool"
	default:
		return "number"
	}
}

func scalar(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return strings.TrimSpace(string(raw))
}

func join(prefix, k string) string {
	if prefix == "" {
		return k
	}
	return prefix + "." + k
}

func truncate(s string) string { return truncateN(s, maxSampleLen) }

func truncateN(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", "\\n")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…(" + strconv.Itoa(len(s)) + "B)"
}
