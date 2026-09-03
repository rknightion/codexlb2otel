package profile

import _ "embed"

// embeddedBaseline is the full-scan, content-free archive signature used by the
// in-process drift runner. Keeping it beside the profiler makes the service image
// self-contained and guarantees the scheduled probe compares against the same
// baseline as the CLI and CI gates.
//
//go:embed baseline/corpus.sig.json
var embeddedBaseline []byte

// EmbeddedBaseline returns a private copy of the committed archive signature.
// Callers may decode or retain it without being able to mutate the package's embed.
func EmbeddedBaseline() []byte {
	return append([]byte(nil), embeddedBaseline...)
}
