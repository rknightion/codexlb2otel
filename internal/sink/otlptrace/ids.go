package otlptrace

import (
	"context"
	"crypto/sha256"
	"strings"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// This file is the answer to the one hard problem the OTLP trace sink has to solve:
// this service replays HISTORICAL records off disk, so there is no live parent
// context to propagate and no tracer.Start(ctx, ...) call that would produce the
// right tree on its own. Every id emitted here is instead a pure function of the
// archive's own identity fields, so the SAME turn processed twice - by the same
// process after a restart, or by two processes racing during a checkpoint handover -
// produces the IDENTICAL trace and span ids. That is what makes re-emission
// idempotent rather than duplicating: Tempo sees the same span twice and treats the
// second as a no-op, not as a new event.
//
// hashTraceID and hashSpanID never take a time.Now() or a random source as input -
// only strings already sitting on a turn.Turn.

// hashTraceID derives a 16-byte trace id from the given identity parts, joined with a
// NUL separator so ("ab","c") and ("a","bc") never collide.
//
// TraceID.IsValid() rejects the all-zero id, and a NUL-terminated SHA-256 preimage
// of live archive ids landing on exactly zero is not something a test can force -
// but the guard costs one branch and the alternative is a span silently dropped by
// every OTel exporter, so it stays.
func hashTraceID(parts ...string) trace.TraceID {
	sum := sha256.Sum256([]byte(joinParts(parts)))
	var id trace.TraceID
	copy(id[:], sum[:16])
	if !id.IsValid() {
		id[len(id)-1] ^= 0x01
	}
	return id
}

// hashSpanID derives an 8-byte span id. It reads a DIFFERENT slice of the digest
// than hashTraceID (bytes 16:24 rather than 0:16) so that deriving both a trace id
// and a span id from the same identity string - which happens for a thread's very
// first turn, whose trace id and turn-span id are both seeded from ThreadID - never
// makes the span id a prefix-duplicate of the trace id.
func hashSpanID(parts ...string) trace.SpanID {
	sum := sha256.Sum256([]byte(joinParts(parts)))
	var id trace.SpanID
	copy(id[:], sum[16:24])
	if !id.IsValid() {
		id[len(id)-1] ^= 0x01
	}
	return id
}

func joinParts(parts []string) string {
	var b strings.Builder
	for i, p := range parts {
		if i > 0 {
			b.WriteByte(0)
		}
		b.WriteString(p)
	}
	return b.String()
}

// parseCapturedTraceID and parseCapturedSpanID validate a hex id recovered from the
// archive's own traceparent header (see turn.Turn.TraceID / SpanID). FromHex already
// rejects the wrong length, non-hex characters and the all-zero id, which is exactly
// the validation an externally-sourced id needs before it is trusted.
func parseCapturedTraceID(s string) (trace.TraceID, bool) {
	id, err := trace.TraceIDFromHex(s)
	return id, err == nil
}

func parseCapturedSpanID(s string) (trace.SpanID, bool) {
	id, err := trace.SpanIDFromHex(s)
	return id, err == nil
}

// --- deterministic IDGenerator ---
//
// The stable SDK gives no public way to dictate a span's own id: TracerProvider
// calls IDGenerator.NewIDs (root spans) or IDGenerator.NewSpanID (child spans)
// internally, with no hook for the caller of tracer.Start to override the result.
// The documented extension point IS the IDGenerator interface itself, so this smuggles
// the id this package already decided on through the context, and a custom generator
// reads it back out. startRoot and startChild below are the ONLY two places that call
// tracer.Start in this package, and both always pin an id first - so the panics here
// are an invariant check on THIS package's own code, not a reachable runtime failure
// driven by turn data.

type pinnedIDs struct {
	traceID trace.TraceID
	spanID  trace.SpanID
}

type pinnedIDKey struct{}

func withPinnedIDs(ctx context.Context, tid trace.TraceID, sid trace.SpanID) context.Context {
	return context.WithValue(ctx, pinnedIDKey{}, pinnedIDs{traceID: tid, spanID: sid})
}

// pinnedIDGenerator hands the SDK exactly the ids startRoot/startChild already
// pinned into the context, rather than generating anything itself.
type pinnedIDGenerator struct{}

var _ sdktrace.IDGenerator = pinnedIDGenerator{}

// NewIDs is called for a ROOT span - one started with no valid parent SpanContext in
// ctx. This package only ever creates one kind of root span (the turn span), and
// startRoot always pins both ids before calling tracer.Start.
func (pinnedIDGenerator) NewIDs(ctx context.Context) (trace.TraceID, trace.SpanID) {
	p, ok := ctx.Value(pinnedIDKey{}).(pinnedIDs)
	if !ok || !p.traceID.IsValid() || !p.spanID.IsValid() {
		panic("otlptrace: a root span was started without pinned ids - " +
			"every span in this package must go through startRoot/startChild")
	}
	return p.traceID, p.spanID
}

// NewSpanID is called for a CHILD span - one started with a valid parent
// SpanContext already in ctx, from which the SDK takes the trace id itself. Only the
// new span's own id needs to be supplied here.
func (pinnedIDGenerator) NewSpanID(ctx context.Context, _ trace.TraceID) trace.SpanID {
	p, ok := ctx.Value(pinnedIDKey{}).(pinnedIDs)
	if !ok || !p.spanID.IsValid() {
		panic("otlptrace: a child span was started without a pinned span id - " +
			"every span in this package must go through startRoot/startChild")
	}
	return p.spanID
}
