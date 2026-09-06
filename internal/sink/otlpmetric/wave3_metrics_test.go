package otlpmetric

import (
	"context"
	"slices"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/rknightion/codexlb2otel/internal/attr"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

func TestWave3TurnAndCostDimensionSets(t *testing.T) {
	s, reader, _ := newTestSink(t)
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	cost := 1.25
	tt := baseTurn("wave3-dimensions")
	tt.ResponseID = "response-wave3-dimensions"
	tt.RequestKind = requestKindTurn
	tt.ConnectionKind = "normal"
	tt.ClientGroup = "codex-tui"
	tt.InputTokens = 1
	tt.CostUSD = &cost
	tt.TurnStart = time.Unix(99, 0)
	tt.ServerCreatedAt = time.Unix(100, 0)
	tt.ServerCompletedAt = time.Unix(101, 0)

	if err := s.Emit(context.Background(), []*turn.Turn{tt}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	rm := collect(t, reader)

	tests := []struct {
		name string
		want []string
	}{
		{name: attr.MetricResponses, want: []string{
			attr.GenAIProvider, attr.GenAIOperation, attr.GenAIRequestModel,
			attr.GenAIResponseModel, attr.Status, attr.RequestKind, attr.Family, attr.AccountID,
			attr.ConnectionKind, attr.GenAIAgentName, attr.BaselineReset,
		}},
		{name: attr.MetricTurns, want: []string{
			attr.GenAIProvider, attr.GenAIOperation, attr.GenAIRequestModel,
			attr.GenAIResponseModel, attr.Status, attr.RequestKind, attr.Family, attr.AccountID,
			attr.ConnectionKind, attr.ClientGroup, attr.GenAIAgentName, attr.BaselineReset,
		}},
		{name: attr.MetricCostUSD, want: []string{
			attr.GenAIProvider, attr.GenAIOperation, attr.GenAIRequestModel,
			attr.GenAIResponseModel, attr.AccountID, attr.RequestKind, attr.Family,
			attr.ConnectionKind, attr.ClientGroup,
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, ok := findMetric(rm, tc.name)
			if !ok {
				t.Fatalf("metric not recorded")
			}
			sets := attrSets(t, m)
			if len(sets) != 1 {
				t.Fatalf("got %d attribute sets, want 1", len(sets))
			}
			got := keysOf(sets[0])
			want := slices.Clone(tc.want)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("attribute keys = %v, want %v", got, want)
			}
		})
	}

	responses, _ := findMetric(rm, attr.MetricResponses)
	assertWave3MetricAttributeAbsent(t, attr.MetricResponses, responses, attr.ClientGroup)
	turns, _ := findMetric(rm, attr.MetricTurns)
	assertWave3MetricAttribute(t, attr.MetricTurns, turns, attr.ClientGroup, tt.ClientGroup)
	costMetric, _ := findMetric(rm, attr.MetricCostUSD)
	assertWave3MetricAttribute(t, attr.MetricCostUSD, costMetric, attr.ClientGroup, tt.ClientGroup)
}

func TestWave3FamilyMetricsCarryConnectionKind(t *testing.T) {
	s, reader, _ := newTestSink(t)
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	queue := 1
	cost := 1.25
	tt := baseTurn("wave3-family")
	tt.ResponseID = "response-wave3-family"
	tt.RequestKind = requestKindTurn
	tt.ConnectionKind = "prewarm"
	tt.InputTokens, tt.OutputTokens = 1, 2
	tt.ImageGenTokens = 3
	tt.CostUSD = &cost
	tt.ProxyQueueWaitMS = &queue
	tt.EngineCallsDelta = 1
	tt.TurnStart = time.Unix(99, 0)
	tt.ServerCreatedAt = time.Unix(100, 0)
	tt.ServerCompletedAt = time.Unix(101, 0)
	tt.TTFTMs = 1
	tt.CriticalPath.Coverage = "complete"
	tt.CriticalPath.EngineCalls = 1
	tt.CriticalPath.EngineWallMs = 1
	tt.CriticalPath.HarnessUnblockedMs = 2
	tt.CriticalPath.PreInferenceMs = 3
	tt.CriticalPath.SamplingStreamMs = 4
	tt.CriticalPath.ClientToolPauseMs = 5
	tt.EngineServiceInferenceMsDelta = 6
	tt.EngineServiceSamplingMsDelta = 7
	tt.EngineIapiInferenceMsDelta = 8
	tt.EngineIapiSamplingMsDelta = 9
	tt.ResponsesExclEngineAndToolMsDelta = 10
	tt.ResponsesExclEngineWaitSamplingMsDelta = 11
	tt.ResponsesExclEngineWaitSamplingIapiMsDelta = 12
	tt.ResponsesAPIExclClientToolsMsDelta = 13
	tt.EngineUncachedPromptTokensDelta = 14
	tt.EngineServiceTBTMs = 15
	tt.EngineIapiTBTMs = 16
	tt.EngineServiceMinusIapiTBTMs = -1
	tt.BaselineReset = true
	tt.Status = turn.StatusTransport
	tt.CloseCode = intPtr(1000)

	if err := s.Emit(context.Background(), []*turn.Turn{tt}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	rm := collect(t, reader)

	wantFamilyMetrics := []string{
		attr.MetricResponses,
		attr.MetricTurns,
		attr.MetricTokens,
		attr.MetricTokenUsage,
		attr.MetricImageGenTokens,
		attr.MetricCostUSD,
		attr.MetricProxyWait,
		attr.MetricProxyWaitCoverage,
		attr.MetricOperationDuration,
		attr.MetricTurnDuration,
		attr.MetricTTFT,
		attr.MetricEngineWall,
		attr.MetricHarnessUnblocked,
		attr.MetricPreInference,
		attr.MetricSamplingStream,
		attr.MetricClientToolPause,
		attr.MetricEngineServiceInference,
		attr.MetricEngineServiceSampling,
		attr.MetricEngineIapiInference,
		attr.MetricEngineIapiSampling,
		attr.MetricResponsesExclEngineAndTool,
		attr.MetricResponsesExclEngineWaitSampling,
		attr.MetricResponsesExclEngineWaitSamplingIapi,
		attr.MetricResponsesAPIExclClientTools,
		attr.MetricEngineUncachedPromptTokens,
		attr.MetricEngineServiceTBT,
		attr.MetricEngineIapiTBT,
		attr.MetricEngineServiceMinusIapiTBT,
		attr.MetricTransportEvents,
		attr.MetricBaselineResets,
	}
	for _, name := range wantFamilyMetrics {
		m, ok := findMetric(rm, name)
		if !ok {
			t.Fatalf("%s not recorded; the synthetic turn should produce it", name)
		}
		for _, set := range attrSets(t, m) {
			if family, ok := attrString(t, set, attr.Family); !ok || family == "" {
				t.Fatalf("%s data point has no family", name)
			}
			assertAttr(t, name, set, attr.ConnectionKind, tt.ConnectionKind)
		}
	}
}

func TestWave3AbsentConnectionKindIsOmitted(t *testing.T) {
	s, reader, _ := newTestSink(t)
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	cost := 1.0
	queue := 0
	tt := baseTurn("wave3-absent-connection")
	tt.ResponseID = "response-wave3-absent-connection"
	tt.RequestKind = requestKindTurn
	tt.InputTokens = 1
	tt.CostUSD = &cost
	tt.ProxyQueueWaitMS = &queue
	tt.ServerCreatedAt = time.Unix(100, 0)
	tt.ServerCompletedAt = time.Unix(101, 0)

	if err := s.Emit(context.Background(), []*turn.Turn{tt}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	rm := collect(t, reader)
	for _, name := range []string{
		attr.MetricResponses,
		attr.MetricTurns,
		attr.MetricTokens,
		attr.MetricCostUSD,
		attr.MetricProxyWait,
		attr.MetricOperationDuration,
	} {
		m, ok := findMetric(rm, name)
		if !ok {
			t.Fatalf("%s not recorded", name)
		}
		assertWave3MetricAttributeAbsent(t, name, m, attr.ConnectionKind)
	}
}

func TestWave3ConnectionKindDoesNotAlterTurnRequestKindGate(t *testing.T) {
	s, reader, _ := newTestSink(t)
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	turns := []*turn.Turn{
		wave3TurnWithKinds("wave3-turn-normal", requestKindTurn, "normal"),
		wave3TurnWithKinds("wave3-turn-prewarm-connection", requestKindTurn, "prewarm"),
		wave3TurnWithKinds("wave3-prewarm-normal-connection", "prewarm", "normal"),
		wave3TurnWithKinds("wave3-prewarm-prewarm-connection", "prewarm", "prewarm"),
		wave3TurnWithKinds("wave3-compaction-prewarm-connection", "compaction", "prewarm"),
	}
	if err := s.Emit(context.Background(), turns); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	rm := collect(t, reader)

	responseMetric, ok := findMetric(rm, attr.MetricResponses)
	if !ok {
		t.Fatalf("%s not recorded", attr.MetricResponses)
	}
	if got := sumInt64(t, responseMetric); got != int64(len(turns)) {
		t.Errorf("%s sum = %d, want %d", attr.MetricResponses, got, len(turns))
	}
	turnMetric, ok := findMetric(rm, attr.MetricTurns)
	if !ok {
		t.Fatalf("%s not recorded", attr.MetricTurns)
	}
	if got := sumInt64(t, turnMetric); got != 2 {
		t.Errorf("%s sum = %d, want 2 (only RequestKind=turn counts)", attr.MetricTurns, got)
	}
	for _, set := range attrSets(t, turnMetric) {
		assertAttr(t, attr.MetricTurns, set, attr.RequestKind, requestKindTurn)
	}
}

func TestWave3ProxyWaitSyntheticPathsPreserveAbsentAndZero(t *testing.T) {
	s, reader, _ := newTestSink(t)
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	cases := []struct {
		id, family          string
		queue, gate, bridge *int
	}{
		{id: "direct-streaming", family: "http", queue: intPtr(0)},
		{id: "websocket", family: "websocket", gate: intPtr(0)},
		{id: "http-bridge", family: "http", gate: intPtr(0), bridge: intPtr(0)},
	}
	for _, tc := range cases {
		tt := baseTurn("wave3-wait-" + tc.id)
		tt.ResponseID = "response-wave3-wait-" + tc.id
		tt.RequestKind = requestKindTurn
		tt.ConnectionKind = "normal"
		tt.Family = tc.family
		tt.ProxyQueueWaitMS, tt.ProxyResponseCreateGateWaitMS, tt.ProxyBridgeQueueWaitMS = tc.queue, tc.gate, tc.bridge
		if err := s.Emit(context.Background(), []*turn.Turn{tt}); err != nil {
			t.Fatalf("Emit %s: %v", tc.id, err)
		}
	}
	rm := collect(t, reader)

	wait, ok := findMetric(rm, attr.MetricProxyWait)
	if !ok {
		t.Fatalf("%s not recorded", attr.MetricProxyWait)
	}
	hist, ok := wait.Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("%s: not a float64 histogram (got %T)", attr.MetricProxyWait, wait.Data)
	}
	type waitKey struct{ family, kind string }
	waitCounts := map[waitKey]uint64{}
	waitSums := map[waitKey]float64{}
	for _, dp := range hist.DataPoints {
		family, familyOK := attrString(t, dp.Attributes, attr.Family)
		kind, kindOK := attrString(t, dp.Attributes, attr.ProxyWaitKind)
		if !familyOK || !kindOK {
			t.Fatalf("%s data point missing family or wait kind", attr.MetricProxyWait)
		}
		assertAttr(t, attr.MetricProxyWait, dp.Attributes, attr.ConnectionKind, "normal")
		key := waitKey{family: family, kind: kind}
		waitCounts[key] += dp.Count
		waitSums[key] += dp.Sum
	}
	wantWaitCounts := map[waitKey]uint64{
		{family: "http", kind: "queue"}:                     1,
		{family: "http", kind: "response_create_gate"}:      1,
		{family: "http", kind: "bridge_queue"}:              1,
		{family: "websocket", kind: "response_create_gate"}: 1,
	}
	if !wave3MapsEqual(wantWaitCounts, waitCounts) {
		t.Errorf("%s counts = %v, want %v", attr.MetricProxyWait, waitCounts, wantWaitCounts)
	}
	for key, count := range wantWaitCounts {
		if waitSums[key] != 0 {
			t.Errorf("%s %s/%s sum = %v, want 0 for an explicit zero", attr.MetricProxyWait, key.family, key.kind, waitSums[key])
		}
		if count == 0 {
			t.Errorf("%s %s/%s has no count", attr.MetricProxyWait, key.family, key.kind)
		}
	}

	coverage, ok := findMetric(rm, attr.MetricProxyWaitCoverage)
	if !ok {
		t.Fatalf("%s not recorded", attr.MetricProxyWaitCoverage)
	}
	coverageSum, ok := coverage.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("%s: not an int64 sum (got %T)", attr.MetricProxyWaitCoverage, coverage.Data)
	}
	type coverageKey struct{ family, kind, result string }
	coverageValues := map[coverageKey]int64{}
	for _, dp := range coverageSum.DataPoints {
		family, familyOK := attrString(t, dp.Attributes, attr.Family)
		kind, kindOK := attrString(t, dp.Attributes, attr.ProxyWaitKind)
		result, resultOK := attrString(t, dp.Attributes, attr.SelfObsResult)
		if !familyOK || !kindOK || !resultOK {
			t.Fatalf("%s data point missing family, wait kind or result", attr.MetricProxyWaitCoverage)
		}
		assertAttr(t, attr.MetricProxyWaitCoverage, dp.Attributes, attr.ConnectionKind, "normal")
		coverageValues[coverageKey{family: family, kind: kind, result: result}] += dp.Value
	}
	wantCoverageValues := map[coverageKey]int64{
		{family: "http", kind: "queue", result: "present"}:                     1,
		{family: "http", kind: "queue", result: "absent"}:                      1,
		{family: "http", kind: "response_create_gate", result: "present"}:      1,
		{family: "http", kind: "response_create_gate", result: "absent"}:       1,
		{family: "http", kind: "bridge_queue", result: "absent"}:               1,
		{family: "http", kind: "bridge_queue", result: "present"}:              1,
		{family: "websocket", kind: "queue", result: "absent"}:                 1,
		{family: "websocket", kind: "response_create_gate", result: "present"}: 1,
		{family: "websocket", kind: "bridge_queue", result: "absent"}:          1,
	}
	if !wave3MapsEqual(wantCoverageValues, coverageValues) {
		t.Errorf("%s values = %v, want %v", attr.MetricProxyWaitCoverage, coverageValues, wantCoverageValues)
	}
}

func wave3TurnWithKinds(id, requestKind, connectionKind string) *turn.Turn {
	tt := baseTurn(id)
	tt.ResponseID = "response-" + id
	tt.RequestKind = requestKind
	tt.ConnectionKind = connectionKind
	tt.ClientGroup = "curl"
	return tt
}

func assertWave3MetricAttribute(t *testing.T, name string, metric metricdata.Metrics, key, want string) {
	t.Helper()
	for _, set := range attrSets(t, metric) {
		got, ok := attrString(t, set, key)
		if !ok {
			t.Errorf("%s: missing %s", name, key)
			continue
		}
		if got != want {
			t.Errorf("%s: %s = %q, want %q", name, key, got, want)
		}
	}
}

func assertWave3MetricAttributeAbsent(t *testing.T, name string, metric metricdata.Metrics, key string) {
	t.Helper()
	for _, set := range attrSets(t, metric) {
		if got, ok := attrString(t, set, key); ok {
			t.Errorf("%s: %s = %q, want absent", name, key, got)
		}
	}
}

func wave3MapsEqual[K comparable, V comparable](want, got map[K]V) bool {
	if len(want) != len(got) {
		return false
	}
	for key, value := range want {
		if got[key] != value {
			return false
		}
	}
	return true
}
