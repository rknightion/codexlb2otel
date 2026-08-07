package profile

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

var sprintf = fmt.Sprintf

// SignatureVersion is bumped when the signature's own layout changes, so an old
// baseline is rejected outright rather than diffed against a shape it predates.
const SignatureVersion = 1

// Signature is the committable, content-free summary of an archive's shape.
//
// Two properties make it different from a Profile, and both are load-bearing:
//
//  1. It carries NO conversation content. The baseline lives in git, and the archive
//     holds full prompts, tool output and assistant messages. Every value that
//     survives into a signature must pass contentSafe, and the test suite asserts
//     that on real records.
//
//  2. It is stable across runs. Absolute counts churn on every capture and would
//     make the committed baseline a permanent diff. Volumes are therefore reduced to
//     an order-of-magnitude bucket, which still moves when an event type all but
//     disappears but not when yesterday was busier than today.
type Signature struct {
	Version  int      `json:"version"`
	Coverage Coverage `json:"coverage"`

	Storage StorageSig          `json:"storage"`
	Record  RecordSig           `json:"record"`
	Events  map[string]EventSig `json:"events"`
}

// StorageSig describes the container: how the bytes are framed before any protocol
// event is visible. A change here breaks the reader, not the reducer.
type StorageSig struct {
	// MultiMember is the property that makes safe tailing possible at all. If a
	// capture ever arrives as one member per file, byte-offset resume is unsound.
	MultiMember bool `json:"multi_member"`
	// LinesPerMember was ~1.1 across the captured corpus. A large move means the
	// writer changed its batching.
	LinesPerMember float64 `json:"lines_per_member"`
	// FilenamePatterns are basenames with the variable parts masked, so a rotation
	// scheme change (hourly -> minutely, a new suffix) shows up as a new pattern.
	FilenamePatterns []string `json:"filename_patterns"`
	// PayloadFraming is how the inner event body is framed - see Profile.PayloadFraming.
	PayloadFraming []string `json:"payload_framing"`
	// PayloadShapes is the key set of the payload object itself.
	PayloadShapes []string `json:"payload_shapes"`
	// UndecodableShare is the fraction of lines that were not JSON at all.
	UndecodableShare float64 `json:"undecodable_line_share"`
	// UnparsedPayloadShare is the fraction of payloads whose text did not parse.
	UnparsedPayloadShare float64 `json:"unparsed_payload_share"`
}

// RecordSig describes the outer archive record.
type RecordSig struct {
	Keys            []string            `json:"keys"`
	Fields          map[string][]string `json:"fields"`
	Headers         []string            `json:"headers"`
	HeaderValues    map[string][]string `json:"header_values,omitempty"`
	RequestIDShapes []string            `json:"request_id_shapes"`
	KindEvents      map[string][]string `json:"kind_events"`
}

// EventSig is the induced schema of one protocol event type.
type EventSig struct {
	// Magnitude is floor(log10(count)) - a stable stand-in for volume.
	Magnitude int                `json:"magnitude"`
	Paths     map[string]PathSig `json:"paths"`
	Capped    bool               `json:"paths_capped,omitempty"`
}

// PathSig is one JSON path and the types it was observed holding.
//
// Types is the field that matters most. A path that gains a second type is the
// exact failure that silently discarded 1,500 input items: inputItem.Output was
// declared string, the protocol also sent an array, and Go's decoder abandons the
// whole event on one mismatched field. A new type here is a breaking finding.
type PathSig struct {
	Types  []string `json:"types"`
	Values []string `json:"values,omitempty"`
}

// Signature reduces a Profile to its committable shape.
func (p *Profile) Signature(cov Coverage) *Signature {
	sig := &Signature{
		Version:  SignatureVersion,
		Coverage: cov,
		Events:   map[string]EventSig{},
		Record: RecordSig{
			Keys:            sortedKeysOf(p.RecordKeys),
			Fields:          map[string][]string{},
			Headers:         sortedKeysOf(p.Headers),
			HeaderValues:    map[string][]string{},
			RequestIDShapes: sortedKeysOf(p.RequestIDShapes),
			KindEvents:      map[string][]string{},
		},
	}

	for k, h := range p.RecordFields {
		sig.Record.Fields[k] = enumValues(k, h)
	}
	for k, h := range p.Headers {
		if v := enumValues("header."+k, h); len(v) > 0 {
			sig.Record.HeaderValues[k] = v
		}
	}
	for kind, evs := range p.KindEvents {
		sig.Record.KindEvents[kind] = sortedKeysOf(evs)
	}

	sig.Storage = StorageSig{
		MultiMember:          multiMember(p.Files),
		LinesPerMember:       round(ratio(p.Lines, p.Members), 2),
		FilenamePatterns:     filenamePatterns(p.Files),
		PayloadFraming:       sortedKeysOf(p.PayloadFraming),
		PayloadShapes:        sortedKeysOf(p.PayloadShapes),
		UndecodableShare:     round(ratio(p.Bad, p.Lines+p.Bad), 4),
		UnparsedPayloadShare: round(ratio(p.PayloadUnparsed, p.Lines), 4),
	}

	for name, ep := range p.Events {
		es := EventSig{Magnitude: magnitude(ep.Count), Paths: map[string]PathSig{}, Capped: ep.PathsCapped}
		for path, pi := range ep.Paths {
			ps := PathSig{Types: sortedKeysOf(pi.Types)}
			if pi.Values != nil {
				ps.Values = enumValues(path, pi.Values)
			}
			es.Paths[path] = ps
		}
		sig.Events[name] = es
	}
	return sig
}

// Severity ranks a finding. The ordering is what -fail-on thresholds against.
type Severity int

// Severities, ascending.
const (
	SevInfo Severity = iota
	SevNew
	SevBreaking
)

func (s Severity) String() string {
	switch s {
	case SevBreaking:
		return "BREAKING"
	case SevNew:
		return "NEW"
	default:
		return "info"
	}
}

// ParseSeverity maps a flag value onto a threshold.
func ParseSeverity(s string) (Severity, bool) {
	switch strings.ToLower(s) {
	case "info":
		return SevInfo, true
	case "new":
		return SevNew, true
	case "breaking":
		return SevBreaking, true
	}
	return 0, false
}

// Finding is one difference between a baseline and a fresh capture.
type Finding struct {
	Severity Severity `json:"-"`
	Level    string   `json:"severity"`
	Kind     string   `json:"kind"`
	Subject  string   `json:"subject"`
	Detail   string   `json:"detail,omitempty"`
}

// Diff compares a fresh signature against a baseline.
//
// Direction matters. Something APPEARING is a change to react to; something
// disappearing is usually the sample not containing it, which is why absence is
// only ever reported as info. A sampled scan cannot prove absence at all.
func Diff(base, cur *Signature) []Finding {
	var f []Finding
	add := func(sev Severity, kind, subject, detail string) {
		f = append(f, Finding{Severity: sev, Level: sev.String(), Kind: kind, Subject: subject, Detail: detail})
	}

	if base.Version != cur.Version {
		add(SevBreaking, "signature.version", "baseline",
			sprintf("baseline is version %d, this build writes %d - regenerate it", base.Version, cur.Version))
		return f
	}

	// --- storage ---
	if base.Storage.MultiMember && !cur.Storage.MultiMember {
		add(SevBreaking, "storage.multi_member", "gzip",
			"members no longer exceed files: byte-offset resume assumes one closed member per batch")
	}
	if base.Storage.LinesPerMember > 0 {
		if r := cur.Storage.LinesPerMember / base.Storage.LinesPerMember; r > 4 || r < 0.25 {
			add(SevNew, "storage.batching", "lines_per_member",
				sprintf("%.2f -> %.2f", base.Storage.LinesPerMember, cur.Storage.LinesPerMember))
		}
	}
	for _, v := range added(base.Storage.PayloadFraming, cur.Storage.PayloadFraming) {
		add(SevBreaking, "storage.payload_framing", v,
			"payload body is framed a way the decoder has never seen (an SSE or chunked transport would look like this)")
	}
	for _, v := range added(base.Storage.PayloadShapes, cur.Storage.PayloadShapes) {
		add(SevBreaking, "storage.payload_shape", v, "payload object has an unseen key set")
	}
	for _, v := range added(base.Storage.FilenamePatterns, cur.Storage.FilenamePatterns) {
		add(SevNew, "storage.filename", v, "rotation naming the watcher has not seen")
	}
	if cur.Storage.UndecodableShare > base.Storage.UndecodableShare+0.01 {
		add(SevBreaking, "storage.undecodable", "lines",
			sprintf("%.2f%% -> %.2f%% of lines are not JSON",
				100*base.Storage.UndecodableShare, 100*cur.Storage.UndecodableShare))
	}
	if cur.Storage.UnparsedPayloadShare > base.Storage.UnparsedPayloadShare+0.01 {
		add(SevBreaking, "storage.unparsed_payload", "payload.text",
			sprintf("%.2f%% -> %.2f%% of payloads do not parse",
				100*base.Storage.UnparsedPayloadShare, 100*cur.Storage.UnparsedPayloadShare))
	}

	// --- record ---
	for _, k := range added(base.Record.Keys, cur.Record.Keys) {
		add(SevNew, "record.key", k, "top-level archive field internal/frame does not name")
	}
	for _, k := range added(cur.Record.Keys, base.Record.Keys) {
		add(SevInfo, "record.key.absent", k, "present in baseline, not in this capture")
	}
	for _, k := range added(base.Record.Headers, cur.Record.Headers) {
		add(SevNew, "record.header", k, "header not seen before")
	}
	for _, s := range added(base.Record.RequestIDShapes, cur.Record.RequestIDShapes) {
		add(SevNew, "record.request_id_shape", s, "the reducer keys on request_id; a new shape may key differently")
	}
	diffValueMaps(base.Record.Fields, cur.Record.Fields, "record.field", SevNew, add)
	diffValueMaps(base.Record.HeaderValues, cur.Record.HeaderValues, "record.header_value", SevNew, add)
	diffValueMaps(base.Record.KindEvents, cur.Record.KindEvents, "record.kind_event", SevNew, add)

	// --- events ---
	for name, ce := range cur.Events {
		be, known := base.Events[name]
		if !known {
			add(SevNew, "event.type", name, sprintf("unseen protocol event, ~1e%d frames", ce.Magnitude))
			continue
		}
		if ce.Capped && !be.Capped {
			add(SevInfo, "event.paths_capped", name, "path budget exhausted; some fields not compared")
		}
		if d := ce.Magnitude - be.Magnitude; d <= -2 {
			add(SevInfo, "event.volume", name, sprintf("~1e%d -> ~1e%d frames", be.Magnitude, ce.Magnitude))
		}
		for path, cp := range ce.Paths {
			bp, seen := be.Paths[path]
			if !seen {
				add(SevNew, "event.path", name+" "+path, sprintf("new field (%s)", strings.Join(cp.Types, "|")))
				continue
			}
			for _, t := range added(bp.Types, cp.Types) {
				add(SevBreaking, "event.path.type", name+" "+path,
					sprintf("was %s, now also %s - a typed decoder abandons the whole event on a mismatch here",
						strings.Join(bp.Types, "|"), t))
			}
			for _, v := range added(bp.Values, cp.Values) {
				add(SevNew, "event.path.value", name+" "+path, sprintf("new value %q", v))
			}
		}
		for path := range be.Paths {
			if _, ok := ce.Paths[path]; !ok {
				add(SevInfo, "event.path.absent", name+" "+path, "present in baseline, not in this capture")
			}
		}
	}
	for name := range base.Events {
		if _, ok := cur.Events[name]; !ok {
			add(SevInfo, "event.type.absent", name, "present in baseline, not in this capture")
		}
	}

	sort.SliceStable(f, func(i, j int) bool {
		if f[i].Severity != f[j].Severity {
			return f[i].Severity > f[j].Severity
		}
		if f[i].Kind != f[j].Kind {
			return f[i].Kind < f[j].Kind
		}
		return f[i].Subject < f[j].Subject
	})
	return f
}

func diffValueMaps(base, cur map[string][]string, kind string, sev Severity, add func(Severity, string, string, string)) {
	for k, cv := range cur {
		bv, known := base[k]
		if !known {
			add(sev, kind, k, sprintf("new grouping: %s", strings.Join(cv, ",")))
			continue
		}
		for _, v := range added(bv, cv) {
			add(sev, kind, k, sprintf("new value %q", v))
		}
	}
}

// ---- content safety ----

// contentDenylist are path segments that carry conversation text. A value under any
// of them never enters a signature, whatever its length or cardinality, because the
// signature is committed to git and the archive holds real prompts and tool output.
var contentDenylist = map[string]bool{
	"text": true, "content": true, "delta": true, "arguments": true,
	"input": true, "output": true, "instructions": true, "message": true,
	"prompt": true, "query": true, "description": true, "summary": true,
	"body": true, "data": true, "value": true, "args": true, "reason": true,
	"error_message": true, "detail": true, "title": true, "label": true,
	"author": true, "recipient": true, "command": true, "path": true,
	"file": true, "url": true, "note": true, "comment": true,
}

// identifierish matches field names that hold identifiers, keys or digests. These
// are unbounded even when a small sample makes them look enum-like.
//
// The separator class must include "-", not just "_". Header-derived paths are
// hyphenated, and an underscore-only rule let x-codex-parent-thread-id through: a
// short sample held only one thread, so a raw UUID passed the cardinality test and
// was written into the committed baseline. Caught by running a sampled scan against
// the full baseline, which reported the id as a "new enum value".
var identifierish = regexp.MustCompile(`(^|[_-])(id|ids|key|keys|hash|token|secret|uuid|guid|fingerprint|signature|sig|nonce|email|user|username|account|identifier)$`)

// enumSafety bounds. A field is only an enum if it is small in BOTH directions:
// few values, and each value short.
const (
	maxEnumValues = 32
	maxEnumLen    = 48
)

// enumValues returns a histogram's values when they are provably an enum and free
// of conversation content, and nil otherwise. Returning nil loses a little diff
// resolution; returning content would leak prompts into a public repo.
func enumValues(path string, h *Hist) []string {
	if h == nil || h.Capped || h.Distinct == 0 || h.Distinct > maxEnumValues {
		return nil
	}
	if !contentSafe(path) {
		return nil
	}
	out := make([]string, 0, len(h.Counts))
	for v := range h.Counts {
		if len(v) > maxEnumLen {
			return nil
		}
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// contentSafe reports whether values at this path may be recorded.
func contentSafe(path string) bool {
	for _, seg := range strings.Split(strings.TrimSuffix(path, "[]"), ".") {
		seg = strings.TrimSuffix(seg, "[]")
		if seg == "" {
			continue
		}
		if contentDenylist[seg] || identifierish.MatchString(seg) {
			return false
		}
	}
	return true
}

// ---- helpers ----

// filenamePatterns masks the variable parts of a basename so hourly rotation
// collapses to one pattern rather than one per file.
func filenamePatterns(files []FileProfile) []string {
	set := map[string]bool{}
	for _, f := range files {
		set[maskDigits(f.Name)] = true
	}
	return sortedKeysOf(set)
}

// multiMember reports whether any file holds more than one gzip member.
//
// Checked per file rather than in aggregate: a corpus of many single-member files
// would satisfy an aggregate test while breaking byte-offset resume completely.
func multiMember(files []FileProfile) bool {
	for _, f := range files {
		if f.Members > 1 {
			return true
		}
	}
	return false
}

var digitRun = regexp.MustCompile(`[0-9]+`)

func maskDigits(s string) string { return digitRun.ReplaceAllString(s, "N") }

// magnitude buckets a count by order of magnitude so the committed baseline does
// not churn on every capture.
func magnitude(n int64) int {
	if n <= 0 {
		return -1
	}
	return int(math.Floor(math.Log10(float64(n))))
}

func ratio(a, b int64) float64 {
	if b <= 0 {
		return 0
	}
	return float64(a) / float64(b)
}

func round(f float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(f*p) / p
}

// added returns the members of cur that are not in base.
func added(base, cur []string) []string {
	if len(cur) == 0 {
		return nil
	}
	have := make(map[string]bool, len(base))
	for _, v := range base {
		have[v] = true
	}
	var out []string
	for _, v := range cur {
		if !have[v] {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func sortedKeysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
