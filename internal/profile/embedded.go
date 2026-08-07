package profile

import (
	"encoding/json"

	"github.com/rknightion/codexlb2otel/internal/frame"
)

// embeddedDocPaths is the explicit allowlist of paths whose STRING value is itself a
// JSON document worth inducing into.
//
// It MUST stay explicit rather than sniffed (testing whether a string starts with
// "{"). corpus.sig.json is committed to git ONLY because it is content-free by
// construction: sniffing would descend into user prompts and tool output, which are
// free text that quite often happens to start with "{" (a pasted config, a tool's
// JSON stdout), and there would be no allowlist left to keep conversation content out
// of a file whose whole point is that it can be public. Naming three paths costs
// nothing and keeps that guarantee absolute.
//
// x-codex-turn-metadata carries the field that caused #20: request_kind gaining the
// value "memory" silently multiplied engine-token accounting by 4.3x, and the
// profiler could not see it because the value lived inside a string leaf. See
// frame.TurnMeta for what is inside once descended.
var embeddedDocPaths = map[string]bool{
	"client_metadata." + frame.HdrTurnMetadata: true,
	"header." + frame.HdrTurnMetadata:          true,
	"header." + frame.HdrTurnState:             true,
}

// embeddedSyntheticEvent buckets header-embedded documents the way Profile.payload
// already buckets a non-websocket payload body under "<payload:kind>" - a
// bracket-prefixed name that Signature, Diff and clbprobe's summary already treat as
// synthetic rather than a real inner-protocol event type (see the "<" check in
// cmd/clbprobe's summarise). Headers accompany every record rather than one event
// type, so there is no single EventProfile they naturally belong to; this gives them
// one, shared across both allowlisted header names since the path itself (via the
// "header.<name>{}" prefix) is what disambiguates.
const embeddedSyntheticEvent = "<header>"

// descendEmbedded parses raw as JSON and walks it into ep at path+"{}" when path is
// on the allowlist and raw actually parses as JSON.
//
// The "{}" suffix, not ".", is deliberate: "[]" already means "array element" in this
// package's path notation (see walk), so "{}" reads as "inside the embedded document"
// and cannot collide with a real JSON key, however that key is spelled.
//
// A value that fails to parse is not an error - AddLine's whole contract is to
// survive exactly the input it exists to characterise - so descendEmbedded silently
// declines and the caller's existing plain-string leaf (already recorded before this
// is called) stands unchanged.
func descendEmbedded(ep *EventProfile, path, raw string) {
	if ep == nil || !embeddedDocPaths[path] {
		return
	}
	var v any
	if json.Unmarshal([]byte(raw), &v) != nil {
		return
	}
	walk(ep, path+"{}", v)
}

// embeddedEvent returns the synthetic bucket embedded-header documents are recorded
// under, gated by the same sampleDepth/inductionStride budget induce() applies to a
// real event. Without that budget, parsing a JSON document out of a header on every
// one of a corpus's ~2M lines would become induction's dominant cost long after the
// shape has settled - headers accompany every record, unlike an inner event type
// which is one of many possible payload.text shapes per line.
//
// Returns nil past the sampled occurrences that fall outside the stride, which
// descendEmbedded treats as "skip this occurrence".
func (p *Profile) embeddedEvent() *EventProfile {
	ep := p.Events[embeddedSyntheticEvent]
	if ep == nil {
		ep = &EventProfile{Paths: map[string]*Path{}}
		p.Events[embeddedSyntheticEvent] = ep
	}
	ep.Count++
	if ep.Sampled >= sampleDepth && ep.Count%inductionStride != 0 {
		return nil
	}
	ep.Sampled++
	return ep
}
