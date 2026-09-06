package otlpmetric

import (
	"context"
	"slices"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/rknightion/codexlb2otel/internal/attr"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

func TestProxyWaitMetricsUseFrozenAttributeSets(t *testing.T) {
	s, reader, _ := newTestSink(t)
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	queue := 0
	gate := 125
	tt := baseTurn("proxy-waits")
	tt.ResponseID = "resp-proxy-waits"
	tt.RequestKind = requestKindTurn
	tt.ConnectionKind = "normal"
	tt.ThreadSource = "subagent"
	tt.ProxyQueueWaitMS = &queue
	tt.ProxyResponseCreateGateWaitMS = &gate
	// ProxyBridgeQueueWaitMS is nil deliberately: coverage must include it, but
	// the histogram must omit it because no measurement was present.

	if err := s.Emit(context.Background(), []*turn.Turn{tt}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	rm := collect(t, reader)

	wait, ok := findMetric(rm, attr.MetricProxyWait)
	if !ok {
		t.Fatalf("%s not recorded", attr.MetricProxyWait)
	}
	if wait.Unit != "s" {
		t.Errorf("%s unit = %q, want s", attr.MetricProxyWait, wait.Unit)
	}
	hist, ok := wait.Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("%s: not a float64 histogram (got %T)", attr.MetricProxyWait, wait.Data)
	}
	if len(hist.DataPoints) != 2 {
		t.Fatalf("%s: got %d data points, want queue and response_create_gate only", attr.MetricProxyWait, len(hist.DataPoints))
	}
	wantBounds := []float64{0.001, 0.002, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5}
	wantWaitAttrs := []string{attr.Family, attr.ConnectionKind, attr.GenAIRequestModel, attr.RequestKind, attr.ThreadSource, attr.ProxyWaitKind}
	slices.Sort(wantWaitAttrs)
	seenKinds := map[string]float64{}
	for _, dp := range hist.DataPoints {
		gotKeys := keysOf(dp.Attributes)
		if !slices.Equal(gotKeys, wantWaitAttrs) {
			t.Errorf("%s attribute keys = %v, want %v", attr.MetricProxyWait, gotKeys, wantWaitAttrs)
		}
		if len(dp.Bounds) != len(wantBounds) || !slices.Equal(dp.Bounds, wantBounds) {
			t.Errorf("%s bounds = %v, want %v", attr.MetricProxyWait, dp.Bounds, wantBounds)
		}
		kind, ok := attrString(t, dp.Attributes, attr.ProxyWaitKind)
		if !ok {
			t.Fatalf("%s data point has no wait kind", attr.MetricProxyWait)
		}
		assertAttr(t, attr.MetricProxyWait, dp.Attributes, attr.Family, tt.Family)
		assertAttr(t, attr.MetricProxyWait, dp.Attributes, attr.ConnectionKind, tt.ConnectionKind)
		assertAttr(t, attr.MetricProxyWait, dp.Attributes, attr.GenAIRequestModel, tt.Model)
		assertAttr(t, attr.MetricProxyWait, dp.Attributes, attr.RequestKind, tt.RequestKind)
		assertAttr(t, attr.MetricProxyWait, dp.Attributes, attr.ThreadSource, tt.ThreadSource)
		seenKinds[kind] = dp.Sum
		if dp.Count != 1 {
			t.Errorf("%s %s count = %d, want 1", attr.MetricProxyWait, kind, dp.Count)
		}
	}
	if got, ok := seenKinds["queue"]; !ok || got != 0 {
		t.Errorf("%s queue sum = %v (present zero must be recorded)", attr.MetricProxyWait, got)
	}
	if got, ok := seenKinds["response_create_gate"]; !ok || got != 0.125 {
		t.Errorf("%s response_create_gate sum = %v, want 0.125", attr.MetricProxyWait, got)
	}
	if _, ok := seenKinds["bridge_queue"]; ok {
		t.Errorf("%s contains absent bridge_queue observation", attr.MetricProxyWait)
	}

	coverage, ok := findMetric(rm, attr.MetricProxyWaitCoverage)
	if !ok {
		t.Fatalf("%s not recorded", attr.MetricProxyWaitCoverage)
	}
	if coverage.Unit != "{observation}" {
		t.Errorf("%s unit = %q, want {observation}", attr.MetricProxyWaitCoverage, coverage.Unit)
	}
	sum, ok := coverage.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("%s: not an int64 sum (got %T)", attr.MetricProxyWaitCoverage, coverage.Data)
	}
	if len(sum.DataPoints) != 3 {
		t.Fatalf("%s: got %d data points, want one per wait kind", attr.MetricProxyWaitCoverage, len(sum.DataPoints))
	}
	wantCoverageAttrs := []string{attr.Family, attr.ConnectionKind, attr.ProxyWaitKind, attr.SelfObsResult}
	slices.Sort(wantCoverageAttrs)
	wantResults := map[string]string{
		"queue":                "present",
		"response_create_gate": "present",
		"bridge_queue":         "absent",
	}
	for _, dp := range sum.DataPoints {
		gotKeys := keysOf(dp.Attributes)
		if !slices.Equal(gotKeys, wantCoverageAttrs) {
			t.Errorf("%s attribute keys = %v, want %v", attr.MetricProxyWaitCoverage, gotKeys, wantCoverageAttrs)
		}
		kind, kindOK := attrString(t, dp.Attributes, attr.ProxyWaitKind)
		result, resultOK := attrString(t, dp.Attributes, attr.SelfObsResult)
		if !kindOK || !resultOK {
			t.Fatalf("%s point missing kind or result: kind=%q (%v), result=%q (%v)", attr.MetricProxyWaitCoverage, kind, kindOK, result, resultOK)
		}
		assertAttr(t, attr.MetricProxyWaitCoverage, dp.Attributes, attr.Family, tt.Family)
		assertAttr(t, attr.MetricProxyWaitCoverage, dp.Attributes, attr.ConnectionKind, tt.ConnectionKind)
		if got := wantResults[kind]; got != result {
			t.Errorf("%s %s result = %q, want %q", attr.MetricProxyWaitCoverage, kind, result, got)
		}
		if dp.Value != 1 {
			t.Errorf("%s %s value = %d, want 1", attr.MetricProxyWaitCoverage, kind, dp.Value)
		}
	}
}

func TestProxyWaitMetricsShareCostReplayGuard(t *testing.T) {
	s, reader, _ := newTestSink(t)
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	queue := 250
	cost := 0.5
	tt := baseTurn("proxy-replay")
	tt.ResponseID = "resp-proxy-replay"
	tt.ProxyQueueWaitMS = &queue
	tt.CostUSD = &cost

	if err := s.Emit(context.Background(), []*turn.Turn{tt, tt}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	rm := collect(t, reader)

	wait, ok := findMetric(rm, attr.MetricProxyWait)
	if !ok {
		t.Fatalf("%s not recorded", attr.MetricProxyWait)
	}
	hist := wait.Data.(metricdata.Histogram[float64])
	if len(hist.DataPoints) != 1 || hist.DataPoints[0].Count != 1 || hist.DataPoints[0].Sum != 0.25 {
		t.Fatalf("%s = %+v, want one 0.25s observation", attr.MetricProxyWait, hist.DataPoints)
	}

	coverage, ok := findMetric(rm, attr.MetricProxyWaitCoverage)
	if !ok {
		t.Fatalf("%s not recorded", attr.MetricProxyWaitCoverage)
	}
	if got := sumInt64(t, coverage); got != 3 {
		t.Errorf("%s sum = %d, want 3 (one coverage point per kind)", attr.MetricProxyWaitCoverage, got)
	}

	costMetric, ok := findMetric(rm, attr.MetricCostUSD)
	if !ok {
		t.Fatalf("%s not recorded", attr.MetricCostUSD)
	}
	costSum, ok := costMetric.Data.(metricdata.Sum[float64])
	if !ok {
		t.Fatalf("%s: not a float64 sum (got %T)", attr.MetricCostUSD, costMetric.Data)
	}
	if len(costSum.DataPoints) != 1 || costSum.DataPoints[0].Value != cost {
		t.Errorf("%s = %+v, want one %.2f observation", attr.MetricCostUSD, costSum.DataPoints, cost)
	}
}

func TestProxyWaitReplayDoesNotClaimCostGuard(t *testing.T) {
	s, reader, _ := newTestSink(t)
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	queue := 250
	tt := baseTurn("proxy-cost-later")
	tt.ResponseID = "resp-proxy-cost-later"
	tt.ProxyQueueWaitMS = &queue

	if err := s.Emit(context.Background(), []*turn.Turn{tt}); err != nil {
		t.Fatalf("first Emit: %v", err)
	}
	cost := 0.5
	tt.CostUSD = &cost
	if err := s.Emit(context.Background(), []*turn.Turn{tt}); err != nil {
		t.Fatalf("second Emit: %v", err)
	}
	rm := collect(t, reader)

	costMetric, ok := findMetric(rm, attr.MetricCostUSD)
	if !ok {
		t.Fatalf("%s not recorded after cost became present", attr.MetricCostUSD)
	}
	costSum, ok := costMetric.Data.(metricdata.Sum[float64])
	if !ok {
		t.Fatalf("%s: not a float64 sum (got %T)", attr.MetricCostUSD, costMetric.Data)
	}
	if len(costSum.DataPoints) != 1 || costSum.DataPoints[0].Value != cost {
		t.Errorf("%s = %+v, want one %.2f observation", attr.MetricCostUSD, costSum.DataPoints, cost)
	}

	wait, ok := findMetric(rm, attr.MetricProxyWait)
	if !ok {
		t.Fatalf("%s not recorded", attr.MetricProxyWait)
	}
	hist := wait.Data.(metricdata.Histogram[float64])
	if len(hist.DataPoints) != 1 || hist.DataPoints[0].Count != 1 || hist.DataPoints[0].Sum != 0.25 {
		t.Errorf("%s = %+v, want one 0.25s observation", attr.MetricProxyWait, hist.DataPoints)
	}
}

func TestProxyWaitAttributeSetsIncludeCoverageAndPresentWaits(t *testing.T) {
	queue := 0
	tt := baseTurn("proxy-sets")
	tt.ProxyQueueWaitMS = &queue
	sets := AttributeSetsForTurn(tt, attr.NewGuard())

	var waitKinds, coverageKinds map[string]bool
	waitKinds = map[string]bool{}
	coverageKinds = map[string]bool{}
	for _, set := range sets {
		switch set.Instrument {
		case attr.MetricProxyWait:
			kind, ok := findKV(set.Attributes, attr.ProxyWaitKind)
			if !ok {
				t.Fatalf("%s attribute set has no wait kind: %+v", attr.MetricProxyWait, set.Attributes)
			}
			waitKinds[kind] = true
		case attr.MetricProxyWaitCoverage:
			kind, ok := findKV(set.Attributes, attr.ProxyWaitKind)
			if !ok {
				t.Fatalf("%s attribute set has no wait kind: %+v", attr.MetricProxyWaitCoverage, set.Attributes)
			}
			coverageKinds[kind] = true
		}
	}
	if !waitKinds["queue"] || len(waitKinds) != 1 {
		t.Errorf("proxy wait attribute sets = %v, want queue only", waitKinds)
	}
	if len(coverageKinds) != 3 || !coverageKinds["queue"] || !coverageKinds["response_create_gate"] || !coverageKinds["bridge_queue"] {
		t.Errorf("proxy wait coverage attribute sets = %v, want all three kinds", coverageKinds)
	}
}

func keysOf(set attribute.Set) []string {
	keys := make([]string, 0, set.Len())
	for _, kv := range set.ToSlice() {
		keys = append(keys, string(kv.Key))
	}
	slices.Sort(keys)
	return keys
}

func findKV(kvs []attr.KV, key string) (string, bool) {
	for _, kv := range kvs {
		if kv.Key == key {
			return kv.Value, true
		}
	}
	return "", false
}
