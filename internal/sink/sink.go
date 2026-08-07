// Package sink defines what an emitter is, so the tail loop does not know or care
// whether a turn is going to Loki, to Mimir or to Tempo.
//
// The interface is deliberately small and its error contract is the load-bearing part:
// an Emit that returns an error means the batch was NOT durably accepted, and the
// caller must not advance the checkpoint past it. That single rule is what stops a
// restart from silently skipping the turns that were in flight when a push failed.
package sink

import (
	"context"
	"errors"
	"fmt"

	"github.com/rknightion/codexlb2otel/internal/turn"
)

// Sink consumes reduced turns.
//
// Implementations must be safe for concurrent use: the tail loop may emit while a
// periodic flush is running.
type Sink interface {
	// Name identifies the sink in logs and in self-observability metrics.
	Name() string

	// Emit hands over a batch. Returning an error means the caller must not treat these
	// turns as delivered.
	//
	// A sink MAY buffer instead of pushing, but the caller cannot then treat nil as
	// delivery: tail.Watcher persists its checkpoint after every successful poll, so a
	// buffered batch would be checkpointed and then genuinely lost when the decoupled
	// flush failed, with nothing left to hold the checkpoint back. cmd/codexlb2otel
	// therefore pairs every Emit with an immediate Flush, and that pairing - not this
	// interface - is what makes nil mean pushed. A sink's batch settings consequently
	// bound the size of one push rather than how often pushes happen.
	Emit(ctx context.Context, turns []*turn.Turn) error

	// Flush blocks until everything previously handed to Emit has been delivered, and
	// reports the first failure. Called before the checkpoint is persisted.
	Flush(ctx context.Context) error

	// Close releases resources. Callers Flush first; Close does not flush, because a
	// Close that silently flushed would hide a failed flush behind a successful exit.
	Close(ctx context.Context) error
}

// Multi fans one batch out to several sinks.
//
// Every sink sees every batch even if an earlier one fails: sinks are independent
// destinations, and stopping at the first error would make Loki's availability decide
// whether metrics get emitted. The errors are joined so the caller still sees that
// something failed and holds the checkpoint.
type Multi []Sink

var _ Sink = Multi(nil)

// Name reports the composite.
func (m Multi) Name() string { return "multi" }

// Emit delivers to every sink, collecting failures rather than stopping at the first.
func (m Multi) Emit(ctx context.Context, turns []*turn.Turn) error {
	return m.each(func(s Sink) error { return s.Emit(ctx, turns) })
}

// Flush flushes every sink.
func (m Multi) Flush(ctx context.Context) error {
	return m.each(func(s Sink) error { return s.Flush(ctx) })
}

// Close closes every sink.
func (m Multi) Close(ctx context.Context) error {
	return m.each(func(s Sink) error { return s.Close(ctx) })
}

func (m Multi) each(fn func(Sink) error) error {
	var errs []error
	for _, s := range m {
		if err := fn(s); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.Name(), err))
		}
	}
	return errors.Join(errs...)
}

// Discard is a sink that accepts everything and keeps nothing. For a dry run, and for
// tests that need a sink but are not testing one.
type Discard struct{}

var _ Sink = Discard{}

// Name reports the sink name.
func (Discard) Name() string { return "discard" }

// Emit accepts and drops the batch.
func (Discard) Emit(context.Context, []*turn.Turn) error { return nil }

// Flush is a no-op.
func (Discard) Flush(context.Context) error { return nil }

// Close is a no-op.
func (Discard) Close(context.Context) error { return nil }

// Rejection is one delivery a sink refused, counted BY REASON.
//
// The reason matters more than the count. Loki discards an oversized line entirely
// rather than truncating it, and a rejection folded into one "errors" total is how
// "we have been dropping 5% of lines for a month" happens - the total looks small and
// nobody can tell which 5%.
type Rejection struct {
	Reason string
	Count  int64
}

// Reasons a sink reports. The first four are Loki's own validation errors, spelled as
// Loki spells them so a metric value can be matched against its documentation.
const (
	ReasonLineTooLong = "line_too_long"
	ReasonRateLimited = "rate_limited"
	ReasonOutOfOrder  = "out_of_order"
	ReasonStreamLimit = "stream_limit"
	ReasonBadRequest  = "bad_request"
	// ReasonTooOld is a line older than the tenant's maximum sample age. It is
	// detected locally and never sent, because Loki does not report it: an over-age
	// push returns 204 and the line is then queryable nowhere. See loki.dropTooOld.
	ReasonTooOld       = "too_old"
	ReasonUnauthorized = "unauthorized"
	ReasonServerError  = "server_error"
	ReasonTransport    = "transport"
)

// Reporter is implemented by sinks that can describe their own health. Optional: the
// service type-asserts for it rather than requiring it of every sink.
type Reporter interface {
	// Rejections reports deliveries refused, by reason, since process start.
	Rejections() []Rejection
	// Pending reports how much is buffered and not yet delivered.
	Pending() int
}
