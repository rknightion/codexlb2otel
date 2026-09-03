package otlpmetric

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/rknightion/codexlb2otel/internal/attr"
	"github.com/rknightion/codexlb2otel/internal/enrich"
	"github.com/rknightion/codexlb2otel/internal/selfobs"
	"github.com/rknightion/codexlb2otel/internal/sink"
)

// TestRegisterSelfObs_IngestLagReflectsSnapshotNotCollectionTime pins the metric
// wiring (issue #8) against a Snapshot whose Watermark is deliberately stale, exactly
// the same shape internal/tail's own
// TestWatcher_IngestLagReflectsArchiveTimeNotWallClock proves one layer down: this
// test's fake source never touches a real archive, so the ONLY way the exported
// gauge can come out near 6h is if this package's callback actually performs
// Now.Sub(Watermark) - not a coincidence of processing being fast, which would
// happen to read near-zero and hide a bug where the wrong value was wired to the
// gauge.
func TestRegisterSelfObs_IngestLagReflectsSnapshotNotCollectionTime(t *testing.T) {
	s, reader, _ := newTestSink(t)
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	now := time.Now().UTC()
	stale := now.Add(-6 * time.Hour)
	source := func() selfobs.Snapshot {
		return selfobs.Snapshot{Now: now, Watermark: stale, HasWatermark: true}
	}
	if err := s.RegisterSelfObs(source); err != nil {
		t.Fatalf("RegisterSelfObs: %v", err)
	}

	rm := collect(t, reader)
	m, ok := findMetric(rm, attr.MetricSelfIngestLag)
	if !ok {
		t.Fatalf("%s not recorded", attr.MetricSelfIngestLag)
	}
	gauge, ok := m.Data.(metricdata.Gauge[float64])
	if !ok {
		t.Fatalf("%s: not a float64 gauge (got %T)", attr.MetricSelfIngestLag, m.Data)
	}
	if len(gauge.DataPoints) != 1 {
		t.Fatalf("%s: got %d data points, want 1", attr.MetricSelfIngestLag, len(gauge.DataPoints))
	}
	got := gauge.DataPoints[0].Value
	want := 6 * 3600.0
	if got < want-1 || got > want+1 {
		t.Errorf("%s = %v, want ~%v (Now - Watermark, not collection time)", attr.MetricSelfIngestLag, got, want)
	}
}

// TestRegisterSelfObs_NoWatermarkOmitsIngestLag is the other half of
// selfobs.Snapshot.IngestLagSeconds' contract: before the watcher has ever seen a
// record, HasWatermark is false and the gauge must not report a fabricated reading
// against the Go zero time (which would look like a decades-long outage).
func TestRegisterSelfObs_NoWatermarkOmitsIngestLag(t *testing.T) {
	s, reader, _ := newTestSink(t)
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	if err := s.RegisterSelfObs(func() selfobs.Snapshot { return selfobs.Snapshot{} }); err != nil {
		t.Fatalf("RegisterSelfObs: %v", err)
	}

	rm := collect(t, reader)
	m, ok := findMetric(rm, attr.MetricSelfIngestLag)
	if ok {
		gauge := m.Data.(metricdata.Gauge[float64])
		if len(gauge.DataPoints) != 0 {
			t.Errorf("%s: got %d data points with no watermark, want 0", attr.MetricSelfIngestLag, len(gauge.DataPoints))
		}
	}
}

// TestRegisterSelfObs_PartialMemberAndDecodeErrorsAreSeparateInstruments is issue
// #8's acceptance criterion carried through to the metric layer: the two must be
// distinguishable, which requires they never land on the same instrument.
func TestRegisterSelfObs_PartialMemberAndDecodeErrorsAreSeparateInstruments(t *testing.T) {
	s, reader, _ := newTestSink(t)
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	source := func() selfobs.Snapshot {
		return selfobs.Snapshot{PartialMemberReads: 41, DecodeErrors: 3}
	}
	if err := s.RegisterSelfObs(source); err != nil {
		t.Fatalf("RegisterSelfObs: %v", err)
	}

	rm := collect(t, reader)
	partial, ok := findMetric(rm, attr.MetricSelfPartialMemberReads)
	if !ok {
		t.Fatalf("%s not recorded", attr.MetricSelfPartialMemberReads)
	}
	decodeErr, ok := findMetric(rm, attr.MetricSelfDecodeErrors)
	if !ok {
		t.Fatalf("%s not recorded", attr.MetricSelfDecodeErrors)
	}
	if got := sumInt64(t, partial); got != 41 {
		t.Errorf("%s = %d, want 41", attr.MetricSelfPartialMemberReads, got)
	}
	if got := sumInt64(t, decodeErr); got != 3 {
		t.Errorf("%s = %d, want 3", attr.MetricSelfDecodeErrors, got)
	}
}

// TestRegisterSelfObs_OpenResponsesAndReducerStateSize covers the leak-visibility
// acceptance criterion directly: a growing open-response map and a growing reducer
// state must both be readable off their own gauges.
func TestRegisterSelfObs_OpenResponsesAndReducerStateSize(t *testing.T) {
	s, reader, _ := newTestSink(t)
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	source := func() selfobs.Snapshot {
		return selfobs.Snapshot{OpenResponses: 17, ReducerSeriesCount: 5, ReducerThreadCount: 9}
	}
	if err := s.RegisterSelfObs(source); err != nil {
		t.Fatalf("RegisterSelfObs: %v", err)
	}

	rm := collect(t, reader)
	for name, want := range map[string]int64{
		attr.MetricSelfOpenResponses:  17,
		attr.MetricSelfReducerSeries:  5,
		attr.MetricSelfReducerThreads: 9,
	} {
		m, ok := findMetric(rm, name)
		if !ok {
			t.Fatalf("%s not recorded", name)
		}
		gauge, ok := m.Data.(metricdata.Gauge[int64])
		if !ok {
			t.Fatalf("%s: not an int64 gauge (got %T)", name, m.Data)
		}
		if len(gauge.DataPoints) != 1 || gauge.DataPoints[0].Value != want {
			t.Errorf("%s: got %+v, want a single point = %d", name, gauge.DataPoints, want)
		}
	}
}

// TestRegisterSelfObs_SinkRejectionsCarrySinkAndReason proves the fan-out over
// Snapshot.Sinks: issue #8's instruction to reuse sink.Reporter rather than add a
// parallel mechanism, checked as an actual attribute readback rather than merely
// "some data arrived".
func TestRegisterSelfObs_SinkRejectionsCarrySinkAndReason(t *testing.T) {
	s, reader, _ := newTestSink(t)
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	source := func() selfobs.Snapshot {
		return selfobs.Snapshot{Sinks: []selfobs.SinkHealth{
			{
				Name:       "loki",
				Pending:    4,
				Rejections: []sink.Rejection{{Reason: sink.ReasonLineTooLong, Count: 12}},
			},
		}}
	}
	if err := s.RegisterSelfObs(source); err != nil {
		t.Fatalf("RegisterSelfObs: %v", err)
	}

	rm := collect(t, reader)

	rej, ok := findMetric(rm, attr.MetricSelfSinkRejections)
	if !ok {
		t.Fatalf("%s not recorded", attr.MetricSelfSinkRejections)
	}
	sum := rej.Data.(metricdata.Sum[int64])
	if len(sum.DataPoints) != 1 {
		t.Fatalf("%s: got %d data points, want 1", attr.MetricSelfSinkRejections, len(sum.DataPoints))
	}
	dp := sum.DataPoints[0]
	if dp.Value != 12 {
		t.Errorf("%s value = %d, want 12", attr.MetricSelfSinkRejections, dp.Value)
	}
	if v, ok := dp.Attributes.Value(attribute.Key("codexlb.selfobs.sink")); !ok || v.AsString() != "loki" {
		t.Errorf("%s: sink attribute = %v (ok=%v), want %q", attr.MetricSelfSinkRejections, v, ok, "loki")
	}
	if v, ok := dp.Attributes.Value(attribute.Key("codexlb.selfobs.reason")); !ok || v.AsString() != sink.ReasonLineTooLong {
		t.Errorf("%s: reason attribute = %v (ok=%v), want %q", attr.MetricSelfSinkRejections, v, ok, sink.ReasonLineTooLong)
	}

	pending, ok := findMetric(rm, attr.MetricSelfSinkPending)
	if !ok {
		t.Fatalf("%s not recorded", attr.MetricSelfSinkPending)
	}
	pg := pending.Data.(metricdata.Gauge[int64])
	if len(pg.DataPoints) != 1 || pg.DataPoints[0].Value != 4 {
		t.Errorf("%s: got %+v, want a single point = 4", attr.MetricSelfSinkPending, pg.DataPoints)
	}
}

// TestRegisterSelfObs_CalledTwiceErrors guards against silently double-registering
// the callback, which would double-report every self-observability value.
func TestRegisterSelfObs_CalledTwiceErrors(t *testing.T) {
	s, _, _ := newTestSink(t)
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	source := func() selfobs.Snapshot { return selfobs.Snapshot{} }
	if err := s.RegisterSelfObs(source); err != nil {
		t.Fatalf("first RegisterSelfObs: %v", err)
	}
	if err := s.RegisterSelfObs(source); err == nil {
		t.Error("second RegisterSelfObs succeeded, want an error")
	}
}

func TestRegisterSelfObs_EnrichmentAndDriftSignals(t *testing.T) {
	s, reader, _ := newTestSink(t)
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	source := func() selfobs.Snapshot {
		return selfobs.Snapshot{
			EnrichCacheEntries: 37,
			HasDrift:           true,
			DriftBreaking:      2,
			DriftNew:           5,
			DriftInfo:          7,
		}
	}
	if err := s.RegisterSelfObs(source); err != nil {
		t.Fatalf("RegisterSelfObs: %v", err)
	}
	s.RecordEnrichment(context.Background(), enrich.Result{
		Outcome: enrich.OutcomeDBHit, LookupDuration: 125 * time.Millisecond,
	})
	s.RecordEnrichment(context.Background(), enrich.Result{Outcome: enrich.OutcomeCacheHit})

	rm := collect(t, reader)
	lookups, ok := findMetric(rm, attr.MetricSelfEnrichLookups)
	if !ok {
		t.Fatalf("%s not recorded", attr.MetricSelfEnrichLookups)
	}
	lookupSum := lookups.Data.(metricdata.Sum[int64])
	gotResults := map[string]int64{}
	for _, dp := range lookupSum.DataPoints {
		v, found := dp.Attributes.Value(attribute.Key(attr.SelfObsResult))
		if !found {
			t.Fatal("enrichment lookup point has no result attribute")
		}
		gotResults[v.AsString()] = dp.Value
	}
	if gotResults["db_hit"] != 1 || gotResults["cache_hit"] != 1 {
		t.Fatalf("lookup results = %v, want one db_hit and one cache_hit", gotResults)
	}

	duration, ok := findMetric(rm, attr.MetricSelfEnrichLookupDuration)
	if !ok {
		t.Fatalf("%s not recorded", attr.MetricSelfEnrichLookupDuration)
	}
	hist := duration.Data.(metricdata.Histogram[float64])
	if len(hist.DataPoints) != 1 || hist.DataPoints[0].Count != 1 || hist.DataPoints[0].Sum != 0.125 {
		t.Fatalf("lookup duration = %+v, want one 0.125s database lookup", hist.DataPoints)
	}

	cache, ok := findMetric(rm, attr.MetricSelfEnrichCacheEntries)
	if !ok {
		t.Fatalf("enrichment cache gauge missing or wrong: %+v", cache)
	}
	cacheGauge := cache.Data.(metricdata.Gauge[int64])
	if len(cacheGauge.DataPoints) != 1 || cacheGauge.DataPoints[0].Value != 37 {
		t.Fatalf("enrichment cache gauge = %+v, want one point of 37", cacheGauge.DataPoints)
	}
	driftMetric, ok := findMetric(rm, attr.MetricArchiveDriftFindings)
	if !ok {
		t.Fatalf("%s not recorded", attr.MetricArchiveDriftFindings)
	}
	driftGauge := driftMetric.Data.(metricdata.Gauge[int64])
	gotDrift := map[string]int64{}
	for _, dp := range driftGauge.DataPoints {
		v, found := dp.Attributes.Value(attribute.Key(attr.SelfObsSeverity))
		if !found {
			t.Fatal("drift point has no severity attribute")
		}
		gotDrift[v.AsString()] = dp.Value
	}
	if gotDrift["breaking"] != 2 || gotDrift["new"] != 5 || gotDrift["info"] != 7 {
		t.Fatalf("drift findings = %v, want breaking=2 new=5 info=7", gotDrift)
	}
}

// TestExistingSinkTestsUnaffectedByRegisterSelfObs guards the design decision this
// whole file rests on: self-observability instruments are built lazily, so a Sink
// that never calls RegisterSelfObs (every OTHER test in this package) reports none of
// the codexlb.selfobs.* instruments - MetricAttrsRejected is the one pre-existing
// ObservableCounter newInstruments always registers regardless (instruments.go), so
// it is expected here and excluded rather than asserting zero instruments outright.
func TestExistingSinkTestsUnaffectedByRegisterSelfObs(t *testing.T) {
	s, reader, _ := newTestSink(t)
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	rm := collect(t, reader)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == attr.MetricAttrsRejected {
				continue
			}
			t.Errorf("unexpected instrument %q reported with no RegisterSelfObs call", m.Name)
		}
	}
}
