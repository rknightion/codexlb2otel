// Package otlptrace turns reduced Turns into an OTLP trace, exported to Tempo.
//
// This is the one sink in the fan-out that cannot simply forward what the reducer
// already computed: Loki wants labelled log lines and OTLP metrics want counters,
// both of which a Turn maps onto directly, but a TRACE is a shape the archive does
// not literally contain. This package builds one, deterministically, from ids the
// archive already carries - see spans.go and ids.go for how and why.
package otlptrace

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/rknightion/codexlb2otel/internal/attr"
	"github.com/rknightion/codexlb2otel/internal/config"
	"github.com/rknightion/codexlb2otel/internal/sink"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

// Sink exports Turns as OTLP trace spans.
type Sink struct {
	tp     *sdktrace.TracerProvider
	tracer trace.Tracer
	guard  *attr.Guard

	mu       sync.Mutex
	pending  int
	rejected map[string]int64
}

var _ sink.Sink = (*Sink)(nil)
var _ sink.Reporter = (*Sink)(nil)

// New builds a Sink that exports over real OTLP HTTP to cfg.Endpoint, authenticating
// with cfg.InstanceID as the basic-auth username and cfg.Token (resolved) as the
// password - the same scheme otlpmetric uses, and the same reason: there is no
// dedicated "basic auth" option on the OTLP HTTP exporter, so the header is built by
// hand. cfg.Traces.SampleRatio sets the head-sampling ratio; cfg.Traces.Enabled is
// NOT checked here - the caller (the wiring pass) only calls New once it already
// knows traces are enabled, matching how otlpmetric.New and loki.New are called.
//
// guard is the attribute cap guard SHARED across every emitter in the process (Loki,
// otlpmetric, this sink) - see internal/attr's Guard doc comment for why it must be
// one instance and not one per sink. Callers construct it once and pass it to all
// three.
//
// New does not block on connectivity: otlptracehttp.New does not itself perform a
// network round trip, so a bad endpoint or bad credentials surface at the first
// Flush, not here.
func New(ctx context.Context, cfg config.OTLP, svc config.Service, guard *attr.Guard) (*Sink, error) {
	if guard == nil {
		return nil, fmt.Errorf("otlptrace: guard must not be nil")
	}
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("otlptrace: otlp.endpoint is empty")
	}
	if cfg.Traces.SampleRatio < 0 || cfg.Traces.SampleRatio > 1 {
		return nil, fmt.Errorf("otlptrace: otlp.traces.sample_ratio must be in [0,1], got %v", cfg.Traces.SampleRatio)
	}
	token, err := cfg.Token.Resolve()
	if err != nil {
		return nil, fmt.Errorf("otlptrace: otlp.token: %w", err)
	}

	endpointURL, err := tracesURL(cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("otlptrace: %w", err)
	}

	exp, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(endpointURL),
		otlptracehttp.WithHeaders(map[string]string{
			"Authorization": "Basic " + basicAuth(cfg.InstanceID, token),
		}),
		otlptracehttp.WithCompression(otlptracehttp.GzipCompression),
		otlptracehttp.WithTimeout(cfg.Timeout),
	)
	if err != nil {
		return nil, fmt.Errorf("otlptrace: creating exporter: %w", err)
	}

	res := sdkresource.NewSchemaless(resourceAttrs(svc)...)

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithIDGenerator(pinnedIDGenerator{}),
		sdktrace.WithSampler(sdktrace.TraceIDRatioBased(cfg.Traces.SampleRatio)),
		sdktrace.WithResource(res),
	)

	return &Sink{
		tp:       tp,
		tracer:   tp.Tracer("github.com/rknightion/codexlb2otel/internal/sink/otlptrace"),
		guard:    guard,
		rejected: map[string]int64{},
	}, nil
}

// tracesURL appends the OTLP traces path to a configured base endpoint.
// otlptracehttp.WithEndpointURL takes the FULL URL and does NOT append "/v1/traces"
// itself (confirmed against the pinned v1.45.0 source) - unlike WithEndpoint, which
// takes host:port only and appends the default path automatically. cfg.Endpoint is
// the gateway base ("…/otlp"), matching otlpmetric's own tracesURL-equivalent logic
// for "/v1/metrics", so this is the one place that turns it into what the exporter
// actually needs.
func tracesURL(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parsing endpoint %q: %w", base, err)
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/v1/traces"
	return u.String(), nil
}

func basicAuth(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
}

func resourceAttrs(svc config.Service) []attribute.KeyValue {
	kvs := []attribute.KeyValue{attribute.String("service.name", svc.Name)}
	if svc.Environment != "" {
		kvs = append(kvs, attribute.String("deployment.environment", svc.Environment))
	}
	return kvs
}

// Name identifies the sink in logs and self-observability.
func (s *Sink) Name() string { return "otlptrace" }

// Emit builds and starts/ends the span tree for every turn, then hands them to the
// TracerProvider's batch processor. Spans queued here are not yet durably
// delivered - see Flush, which is where that guarantee is made good, per the
// sink.Sink contract.
func (s *Sink) Emit(ctx context.Context, turns []*turn.Turn) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, t := range turns {
		s.emitTurn(ctx, t)
	}
	s.mu.Lock()
	s.pending += len(turns)
	s.mu.Unlock()
	return nil
}

// Flush blocks until every span queued by Emit has been exported, and reports the
// first failure - ForceFlush drains the batch processor's queue and performs the
// export inline rather than leaving it to the background loop, which is exactly the
// synchronous guarantee sink.Sink.Flush requires.
func (s *Sink) Flush(ctx context.Context) error {
	s.mu.Lock()
	n := s.pending
	s.mu.Unlock()

	err := s.tp.ForceFlush(ctx)

	s.mu.Lock()
	if err != nil {
		s.rejected[sink.ReasonTransport] += int64(n)
	}
	s.pending = 0
	s.mu.Unlock()

	if err != nil {
		return fmt.Errorf("otlptrace: flush: %w", err)
	}
	return nil
}

// Close shuts the TracerProvider down. Per sink.Sink, callers Flush before Close;
// TracerProvider.Shutdown also flushes internally, but by the time Close runs that
// queue is expected to already be empty, so this does not paper over a skipped
// Flush.
func (s *Sink) Close(ctx context.Context) error {
	if err := s.tp.Shutdown(ctx); err != nil {
		return fmt.Errorf("otlptrace: close: %w", err)
	}
	return nil
}

// Rejections reports export failures observed at Flush, counted against however
// many turns were pending at that Flush call. Coarser than Loki's per-line reasons
// out of necessity: the SDK's batch export reports one pass/fail per flush, not a
// reason per span, so ReasonTransport is the only reason this sink can ever report.
func (s *Sink) Rejections() []sink.Rejection {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]sink.Rejection, 0, len(s.rejected))
	for reason, count := range s.rejected {
		out = append(out, sink.Rejection{Reason: reason, Count: count})
	}
	return out
}

// Pending reports turns handed to Emit since the last Flush.
func (s *Sink) Pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pending
}

// startRoot starts a span with no OTel parent, pinning both the trace id and span
// id it must be given. See ids.go for why pinning through context, rather than a
// direct parameter, is the only extension point the stable SDK exposes.
func (s *Sink) startRoot(ctx context.Context, name string, tid trace.TraceID, sid trace.SpanID, start time.Time, attrs []attribute.KeyValue, links []trace.Link) (context.Context, trace.Span) {
	pinned := withPinnedIDs(ctx, tid, sid)
	return s.tracer.Start(pinned, name,
		trace.WithTimestamp(start),
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attrs...),
		trace.WithLinks(links...),
	)
}

// startChild starts a span as a child of parent (same trace, ParentSpanID = parent's
// span id - ordinary SDK behaviour once a valid parent SpanContext is in ctx), and
// pins the new span's own id.
func (s *Sink) startChild(ctx context.Context, parent trace.SpanContext, name string, sid trace.SpanID, start time.Time, attrs []attribute.KeyValue, kind trace.SpanKind) (context.Context, trace.Span) {
	childCtx := trace.ContextWithSpanContext(ctx, parent)
	pinned := withPinnedIDs(childCtx, trace.TraceID{}, sid) // traceID is unused on this path; see NewSpanID
	return s.tracer.Start(pinned, name,
		trace.WithTimestamp(start),
		trace.WithSpanKind(kind),
		trace.WithAttributes(attrs...),
	)
}
