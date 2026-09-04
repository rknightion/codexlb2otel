// Package otlpmetric turns reduced Turns into OTLP metrics and pushes them to
// Grafana Cloud (Mimir) over HTTP.
//
// Temporality is CUMULATIVE, not delta, and that is not a knob this package exposes.
// Mimir's delta OTLP ingestion is experimental and not enableable on managed Grafana
// Cloud (see issue #7's ground truth). The reducer already hands us per-response
// DELTAS - safe-to-sum increments - and those are the natural argument to an OTel
// Counter.Add(): the SDK accumulates them into a cumulative series itself, and a
// process restart starts that accumulation over with a fresh start time, which OTLP
// consumers read as a correct counter reset rather than a spike or a gap. That is
// also why this sink carries no checkpoint of its own - see Sink.Flush.
package otlpmetric

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"

	"github.com/rknightion/codexlb2otel/internal/attr"
	"github.com/rknightion/codexlb2otel/internal/config"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

// meterScope names this package's instrumentation scope, distinct from the service's
// own resource attributes so a Grafana query can tell "which library produced this
// series" apart from "which deployment did".
const meterScope = "github.com/rknightion/codexlb2otel/internal/sink/otlpmetric"

// costResponseLimit bounds the in-process replay guard. A completed response is
// normally emitted once, but a successful export followed by a checkpoint-save
// failure replays the same archive bytes on the next poll. Keeping recent response
// IDs prevents that retry from adding a DB-derived response cost twice without
// putting a response ID on the metric or growing state without bound.
const costResponseLimit = 65536

// Sink exports reduced Turns as OTLP metrics.
//
// Safe for concurrent use: every instrument method on the OTel API is required to be
// (see the metric package's own doc comments), and Sink adds no state of its own on
// top of them - there is nothing here to lock.
type Sink struct {
	guard *attr.Guard
	mp    *sdkmetric.MeterProvider
	inst  instruments

	// selfObsRegistered guards RegisterSelfObs (selfobs.go) against being called
	// twice, which would register a second callback and double-report every
	// self-observability instrument.
	selfObsRegistered bool
	selfInst          selfInstruments

	costMu        sync.Mutex
	costResponses map[string]struct{}
	costOrder     []string
}

// New builds a Sink that exports over real OTLP HTTP to cfg.Endpoint, authenticating
// with cfg.InstanceID as the basic-auth username and cfg.Token (resolved) as the
// password - the same scheme Grafana Cloud uses for Loki, but there is no dedicated
// "basic auth" option on the OTLP HTTP exporter, so the header is built by hand.
//
// guard is the attribute cap guard SHARED across every emitter in the process (Loki,
// this sink, otlptrace) - see internal/attr's Guard doc comment for why it must be
// one instance and not one per sink. Callers construct it once and pass it to all
// three.
func New(ctx context.Context, cfg config.OTLP, svc config.Service, guard *attr.Guard) (*Sink, error) {
	if guard == nil {
		return nil, fmt.Errorf("otlpmetric: guard must not be nil")
	}
	token, err := cfg.Token.Resolve()
	if err != nil {
		return nil, fmt.Errorf("otlpmetric: otlp.token: %w", err)
	}
	auth := base64.StdEncoding.EncodeToString([]byte(cfg.InstanceID + ":" + token))

	exp, err := otlpmetrichttp.New(ctx,
		// WithEndpointURL takes the path as-is and does NOT append /v1/metrics - see
		// its doc comment. cfg.Endpoint is the gateway base ("…/otlp"); the metrics
		// path has to be appended explicitly or every export 404s.
		otlpmetrichttp.WithEndpointURL(strings.TrimSuffix(cfg.Endpoint, "/")+"/v1/metrics"),
		otlpmetrichttp.WithHeaders(map[string]string{"Authorization": "Basic " + auth}),
		otlpmetrichttp.WithCompression(otlpmetrichttp.GzipCompression),
		otlpmetrichttp.WithTimeout(cfg.Timeout),
		// Cumulative is the exporter's own default (DefaultTemporalitySelector), but
		// it is set explicitly here rather than relied on: this is the one property
		// issue #7 calls "settled, do not re-litigate", and a future SDK bump
		// silently changing its default must not silently change codex-lb2otel's
		// behaviour with it.
		otlpmetrichttp.WithTemporalitySelector(sdkmetric.DefaultTemporalitySelector),
	)
	if err != nil {
		return nil, fmt.Errorf("otlpmetric: building exporter: %w", err)
	}

	reader := sdkmetric.NewPeriodicReader(exp, sdkmetric.WithInterval(cfg.Metrics.Interval))
	return newSink(reader, svc, guard)
}

// newSink builds a Sink around any sdkmetric.Reader. Split out from New so tests can
// hand it a ManualReader and inspect exactly what was recorded, instead of standing
// up an HTTP server to catch what a real exporter would have sent - see sink_test.go.
func newSink(reader sdkmetric.Reader, svc config.Service, guard *attr.Guard) (*Sink, error) {
	attrs := []attribute.KeyValue{attribute.String("service.name", svc.Name)}
	if svc.Environment != "" {
		attrs = append(attrs, attribute.String("deployment.environment", svc.Environment))
	}
	res := resource.NewSchemaless(attrs...)

	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader), sdkmetric.WithResource(res))
	meter := mp.Meter(meterScope)

	inst, err := newInstruments(meter, guard)
	if err != nil {
		_ = mp.Shutdown(context.Background())
		return nil, fmt.Errorf("otlpmetric: building instruments: %w", err)
	}

	return &Sink{guard: guard, mp: mp, inst: inst, costResponses: make(map[string]struct{})}, nil
}

// firstCostResponse reports whether responseID has not been charged in this
// process. An empty ID is retained as an explicitly non-deduplicable fallback for
// hand-built or malformed turns; normal reducer output always has a response ID.
func (s *Sink) firstCostResponse(responseID string) bool {
	if responseID == "" {
		return true
	}
	s.costMu.Lock()
	defer s.costMu.Unlock()
	if _, seen := s.costResponses[responseID]; seen {
		return false
	}
	s.costResponses[responseID] = struct{}{}
	s.costOrder = append(s.costOrder, responseID)
	if len(s.costOrder) > costResponseLimit {
		delete(s.costResponses, s.costOrder[0])
		s.costOrder = s.costOrder[1:]
	}
	return true
}

// Name identifies the sink in logs and self-observability.
func (s *Sink) Name() string { return "otlpmetric" }

// Emit records every turn's measurements against the SDK's in-memory aggregation.
// This never fails on its own: OTel's instrument API has no error return (recording
// cannot fail synchronously by design), so a real export failure only ever surfaces
// from Flush, once the PeriodicReader actually tries to talk to the network.
func (s *Sink) Emit(ctx context.Context, turns []*turn.Turn) error {
	for _, t := range turns {
		s.record(ctx, t)
	}
	return nil
}

// Flush forces an immediate export of everything recorded since the last one and
// reports the first failure, so the caller knows whether it is safe to advance the
// checkpoint. There is no delta buffer to drain here - see the package doc comment -
// so this only has to wait on one HTTP round trip, not walk any internal queue.
func (s *Sink) Flush(ctx context.Context) error {
	if err := s.mp.ForceFlush(ctx); err != nil {
		return fmt.Errorf("otlpmetric: flush: %w", err)
	}
	return nil
}

// Close releases the exporter's resources.
//
// It deliberately does not try to skip the SDK's own final flush-on-shutdown -
// sdkmetric.MeterProvider.Shutdown always attempts one, by upstream design, and
// that is not something this package can turn off. What it does not do is treat
// that as a substitute for calling Flush: callers still must Flush before
// persisting a checkpoint, per the Sink interface's contract, because a failure
// here is Close's problem, not theirs.
func (s *Sink) Close(ctx context.Context) error {
	if err := s.mp.Shutdown(ctx); err != nil {
		return fmt.Errorf("otlpmetric: shutdown: %w", err)
	}
	return nil
}
