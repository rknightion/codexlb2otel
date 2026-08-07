package otlpmetric

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/rknightion/codexlb2otel/internal/archive"
	"github.com/rknightion/codexlb2otel/internal/attr"
	"github.com/rknightion/codexlb2otel/internal/config"
	"github.com/rknightion/codexlb2otel/internal/fixture"
	"github.com/rknightion/codexlb2otel/internal/frame"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

// newTestSink builds a Sink around a ManualReader, so a test can assert exactly what
// was recorded instead of standing up an HTTP server to catch what a real exporter
// would have sent. See newSink's doc comment for why this split exists.
func newTestSink(t *testing.T) (*Sink, *sdkmetric.ManualReader, *attr.Guard) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	guard := attr.NewGuard()
	s, err := newSink(reader, config.Service{Name: "codexlb2otel-test"}, guard)
	if err != nil {
		t.Fatalf("newSink: %v", err)
	}
	return s, reader, guard
}

func collect(t *testing.T, reader *sdkmetric.ManualReader) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	return rm
}

func findMetric(rm metricdata.ResourceMetrics, name string) (metricdata.Metrics, bool) {
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				return m, true
			}
		}
	}
	return metricdata.Metrics{}, false
}

// attrString reads one attribute off a data point's Set, matching the two concrete
// data-point shapes this package produces (Sum's DataPoint and Histogram's
// HistogramDataPoint both expose Attributes the same way, but are different generic
// instantiations so there is no common interface to read it through).
func attrString(t *testing.T, set attribute.Set, key string) (string, bool) {
	t.Helper()
	v, ok := set.Value(attribute.Key(key))
	if !ok {
		return "", false
	}
	return v.AsString(), true
}

func baseTurn(id string) *turn.Turn {
	return &turn.Turn{
		RequestID: id,
		Model:     "gpt-5.6-terra",
		Status:    "completed",
		AccountID: "acct-1",
		Family:    "websocket",
	}
}

// TestPrewarmAndCompactionExcludedFromTurns is the acceptance criterion from issue #7
// stated as a test: prewarm and compaction do real engine work but are not turns a
// user asked for, and counting them inflates turn rates and understates cost per
// turn.
func TestPrewarmAndCompactionExcludedFromTurns(t *testing.T) {
	s, reader, _ := newTestSink(t)
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	turns := []*turn.Turn{
		func() *turn.Turn { tt := baseTurn("1"); tt.RequestKind = "turn"; return tt }(),
		func() *turn.Turn { tt := baseTurn("2"); tt.RequestKind = "turn"; return tt }(),
		func() *turn.Turn { tt := baseTurn("3"); tt.RequestKind = "prewarm"; return tt }(),
		func() *turn.Turn { tt := baseTurn("4"); tt.RequestKind = "compaction"; return tt }(),
	}
	if err := s.Emit(context.Background(), turns); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	rm := collect(t, reader)

	responses, ok := findMetric(rm, attr.MetricResponses)
	if !ok {
		t.Fatalf("%s not recorded", attr.MetricResponses)
	}
	if got := sumInt64(t, responses); got != 4 {
		t.Errorf("%s: got %d responses, want 4 (all four, regardless of request_kind)", attr.MetricResponses, got)
	}

	turnsMetric, ok := findMetric(rm, attr.MetricTurns)
	if !ok {
		t.Fatalf("%s not recorded", attr.MetricTurns)
	}
	if got := sumInt64(t, turnsMetric); got != 2 {
		t.Errorf("%s: got %d, want 2 - prewarm and compaction must be excluded", attr.MetricTurns, got)
	}
	sum := turnsMetric.Data.(metricdata.Sum[int64])
	for _, dp := range sum.DataPoints {
		if kind, _ := attrString(t, dp.Attributes, attr.RequestKind); kind != "turn" {
			t.Errorf("%s: data point carries request_kind=%q, want only \"turn\"", attr.MetricTurns, kind)
		}
	}
}

// TestRateLimitGaugesAlwaysCarryAccountID is the acceptance criterion that rate-limit
// gauges are per-account and meaningless otherwise: a turn with no account_id must
// never produce a rate-limit data point at all, and every data point that IS produced
// must carry a non-empty account_id.
func TestRateLimitGaugesAlwaysCarryAccountID(t *testing.T) {
	s, reader, _ := newTestSink(t)
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	withAccount := baseTurn("1")
	withAccount.RateLimitWindowMin = 300
	withAccount.RateLimitUsedPercent = 42

	noAccount := baseTurn("2")
	noAccount.AccountID = ""
	noAccount.RateLimitWindowMin = 300
	noAccount.RateLimitUsedPercent = 99

	if err := s.Emit(context.Background(), []*turn.Turn{withAccount, noAccount}); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	rm := collect(t, reader)
	m, ok := findMetric(rm, attr.MetricRateLimitUsed)
	if !ok {
		t.Fatalf("%s not recorded", attr.MetricRateLimitUsed)
	}
	gauge := m.Data.(metricdata.Gauge[float64])
	if len(gauge.DataPoints) != 1 {
		t.Fatalf("%s: got %d data points, want exactly 1 - the account-less turn must not "+
			"produce one", attr.MetricRateLimitUsed, len(gauge.DataPoints))
	}
	for _, dp := range gauge.DataPoints {
		acct, ok := attrString(t, dp.Attributes, attr.AccountID)
		if !ok || acct == "" {
			t.Errorf("%s: data point has no account_id attribute", attr.MetricRateLimitUsed)
		}
		if dp.Value != 42 {
			t.Errorf("%s: got %v, want 42", attr.MetricRateLimitUsed, dp.Value)
		}
	}
}

// TestExtraRateLimitsFanOutCarryAccountID covers the per-model gauge specifically:
// ExtraRateLimits is a map, so the fan-out is a different code path than the primary
// window gauge tested above.
func TestExtraRateLimitsFanOutCarryAccountID(t *testing.T) {
	s, reader, _ := newTestSink(t)
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	tt := baseTurn("1")
	tt.ExtraRateLimits = map[string]float64{"GPT-5.3-Codex-Spark": 17.5}
	noAcct := baseTurn("2")
	noAcct.AccountID = ""
	noAcct.ExtraRateLimits = map[string]float64{"GPT-5.3-Codex-Spark": 88}

	if err := s.Emit(context.Background(), []*turn.Turn{tt, noAcct}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	rm := collect(t, reader)
	m, ok := findMetric(rm, attr.MetricRateLimitPerModel)
	if !ok {
		t.Fatalf("%s not recorded", attr.MetricRateLimitPerModel)
	}
	gauge := m.Data.(metricdata.Gauge[float64])
	if len(gauge.DataPoints) != 1 {
		t.Fatalf("%s: got %d data points, want 1", attr.MetricRateLimitPerModel, len(gauge.DataPoints))
	}
	dp := gauge.DataPoints[0]
	if acct, ok := attrString(t, dp.Attributes, attr.AccountID); !ok || acct == "" {
		t.Errorf("%s: data point has no account_id", attr.MetricRateLimitPerModel)
	}
	if model, _ := attrString(t, dp.Attributes, attr.GenAIRequestModel); model != "GPT-5.3-Codex-Spark" {
		t.Errorf("%s: gen_ai.request.model = %q, want the map key", attr.MetricRateLimitPerModel, model)
	}
	// Exactly one gen_ai.request.model value on the data point - Only() dropped the
	// turn's own Model before With() added the map key back, so there is no
	// leftover duplicate from the turn's t.Model ("gpt-5.6-terra").
	n := 0
	for _, kv := range dp.Attributes.ToSlice() {
		if string(kv.Key) == attr.GenAIRequestModel {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%s: gen_ai.request.model appears %d times on one data point, want 1", attr.MetricRateLimitPerModel, n)
	}
}

// TestMillisecondsConvertedToSeconds is the ms->s trap from issue #7: Turn and
// CriticalPath carry milliseconds throughout, but every histogram here is documented
// and named in seconds.
func TestMillisecondsConvertedToSeconds(t *testing.T) {
	s, reader, _ := newTestSink(t)
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	tt := baseTurn("1")
	tt.TTFTMs = 2500 // 2.5s
	tt.CriticalPath.Coverage = "complete"
	tt.CriticalPath.EngineWallMs = 4000 // 4s

	if err := s.Emit(context.Background(), []*turn.Turn{tt}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	rm := collect(t, reader)

	ttft, ok := findMetric(rm, attr.MetricTTFT)
	if !ok {
		t.Fatalf("%s not recorded", attr.MetricTTFT)
	}
	if got := histogramSum(t, ttft); got != 2.5 {
		t.Errorf("%s: got %v seconds, want 2.5 (from 2500ms)", attr.MetricTTFT, got)
	}

	wall, ok := findMetric(rm, attr.MetricEngineWall)
	if !ok {
		t.Fatalf("%s not recorded", attr.MetricEngineWall)
	}
	if got := histogramSum(t, wall); got != 4 {
		t.Errorf("%s: got %v seconds, want 4 (from 4000ms)", attr.MetricEngineWall, got)
	}
}

// TestTokenTypesExcludeTotal pins the deliberate deviation from issue #7's candidate
// list documented in record.go: "total" has no TokenType constant in names.go, so it
// is never emitted as a gen_ai.token.type value - only the five the contract defines.
func TestTokenTypesExcludeTotal(t *testing.T) {
	s, reader, _ := newTestSink(t)
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	tt := baseTurn("1")
	tt.InputTokens, tt.OutputTokens, tt.TotalTokens = 10, 20, 30
	if err := s.Emit(context.Background(), []*turn.Turn{tt}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	rm := collect(t, reader)
	m, ok := findMetric(rm, attr.MetricTokens)
	if !ok {
		t.Fatalf("%s not recorded", attr.MetricTokens)
	}
	for _, dp := range m.Data.(metricdata.Sum[int64]).DataPoints {
		if kind, _ := attrString(t, dp.Attributes, attr.GenAITokenType); kind == "total" {
			t.Errorf("%s: emitted gen_ai.token.type=total, which is not on the contract "+
				"and would double-count against input+output", attr.MetricTokens)
		}
	}
}

// TestTokenUsageHistogramParallelsCounter is issue #18's acceptance criterion "a
// parallel gen_ai.client.token.usage histogram alongside the existing counter" -
// checked as an actual equality against MetricTokens' own sum, not merely "some data
// arrived", so a future change that lets the two drift apart (one attrs.Only slice
// edited and not the other) fails here.
func TestTokenUsageHistogramParallelsCounter(t *testing.T) {
	s, reader, _ := newTestSink(t)
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	tt := baseTurn("1")
	tt.InputTokens, tt.OutputTokens, tt.ReasoningTokens = 10, 20, 5
	if err := s.Emit(context.Background(), []*turn.Turn{tt}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	rm := collect(t, reader)

	counter, ok := findMetric(rm, attr.MetricTokens)
	if !ok {
		t.Fatalf("%s not recorded", attr.MetricTokens)
	}
	histogram, ok := findMetric(rm, attr.MetricTokenUsage)
	if !ok {
		t.Fatalf("%s not recorded", attr.MetricTokenUsage)
	}
	if cSum, hSum := sumInt64(t, counter), histogramSumInt64(t, histogram); cSum != hSum {
		t.Errorf("%s sum = %d, %s sum = %d - the parallel histogram has drifted from "+
			"the counter it is supposed to mirror", attr.MetricTokens, cSum, attr.MetricTokenUsage, hSum)
	}
}

// TestToolCallsPerOperationCountsZero pins the deliberate difference from
// recordToolCalls (which skips a response with no tool calls entirely): this
// histogram measures the shape of tool use across every response, so a response with
// zero tool calls must still contribute a recorded zero rather than being silently
// excluded from the distribution.
//
// Both turns here share the same attribute set (model, account, request kind), so the
// SDK aggregates them into ONE data point keyed by that attribute set - Count is how
// many Record calls landed in it, Sum is their total, and that is what distinguishes
// "recorded a zero" from "never called" (Count would be 1, not 2, if the zero-tool-call
// response had been skipped the way recordToolCalls skips it).
func TestToolCallsPerOperationCountsZero(t *testing.T) {
	s, reader, _ := newTestSink(t)
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	turns := []*turn.Turn{
		baseTurn("1"), // no tool calls
		func() *turn.Turn {
			tt := baseTurn("2")
			tt.ToolCalls = []turn.ToolCall{{Name: "exec"}, {Name: "wait"}}
			return tt
		}(),
	}
	if err := s.Emit(context.Background(), turns); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	rm := collect(t, reader)

	m, ok := findMetric(rm, attr.MetricToolCallsPerOperation)
	if !ok {
		t.Fatalf("%s not recorded", attr.MetricToolCallsPerOperation)
	}
	h, ok := m.Data.(metricdata.Histogram[int64])
	if !ok {
		t.Fatalf("%s: not an int64 histogram (got %T)", attr.MetricToolCallsPerOperation, m.Data)
	}
	if len(h.DataPoints) != 1 {
		t.Fatalf("%s: got %d data points, want 1 (both turns share one attribute set)",
			attr.MetricToolCallsPerOperation, len(h.DataPoints))
	}
	if got := h.DataPoints[0].Count; got != 2 {
		t.Errorf("%s: Count = %d, want 2 (one Record call per response, including the zero)",
			attr.MetricToolCallsPerOperation, got)
	}
	if got := histogramSumInt64(t, m); got != 2 {
		t.Errorf("%s sum = %d, want 2 (0 + 2 tool calls)", attr.MetricToolCallsPerOperation, got)
	}
}

// TestEveryEmittedAttributeKeyIsOnContract asserts the acceptance criterion "attributes
// come from the attribute-contract package only" directly against what actually left
// the sink, across every instrument at once. The two exceptions - GenAITokenType and
// ToolName - are names.go constants used exactly as attr.With's own doc comment
// describes ("a token type on a token counter, a tool name on a tool counter"); they
// are not in attr's Field registry because they are per-item, not per-Turn, but they
// are still frozen names this package did not invent.
func TestEveryEmittedAttributeKeyIsOnContract(t *testing.T) {
	s, reader, _ := newTestSink(t)
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	tt := baseTurn("1")
	tt.RequestKind = "turn"
	tt.Effort = "high"
	tt.PlanType = "pro"
	tt.ServiceTier = "default"
	tt.ThreadSource = "user"
	tt.SubagentKind = "lab"
	tt.Originator = "codex-tui"
	tt.ErrorType, tt.ErrorCode, tt.Status = "server_error", "server_is_overloaded", "error"
	tt.CriticalPath.Coverage = "complete"
	tt.CriticalPath.EngineWallMs = 100
	tt.CriticalPath.EngineCalls = 2
	tt.BaselineReset = true
	tt.CloseCode = intPtr(1000)
	tt.SafetyBuffering = true
	tt.SafetyRetryModel = "gpt-5.6-luna"
	tt.InputTokens, tt.OutputTokens, tt.ReasoningTokens, tt.CachedTokens, tt.CacheWriteTokens = 1, 2, 3, 4, 5
	tt.WebSearchRequests, tt.ImageGenTokens = 1, 1
	tt.ToolCalls = []turn.ToolCall{{Name: "exec"}}
	tt.RateLimitWindowMin, tt.RateLimitUsedPercent = 300, 10
	tt.RateLimit2WindowMin, tt.RateLimit2UsedPercent = 60, 5
	tt.ExtraRateLimits = map[string]float64{"gpt-5.3-codex-spark": 3}
	tt.HasCredits = true
	tt.CreditsBalance = "$12.50"

	if err := s.Emit(context.Background(), []*turn.Turn{tt}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	rm := collect(t, reader)

	allowed := map[string]bool{attr.GenAITokenType: true, attr.ToolName: true}
	for _, f := range attr.Fields() {
		allowed[f.Key] = true
	}
	allowed[attr.GenAIProvider] = true
	allowed[attr.GenAIOperation] = true

	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			for _, key := range dataPointKeys(t, m) {
				if !allowed[key] {
					t.Errorf("metric %s emitted attribute key %q, which is not on the "+
						"attribute contract (internal/attr)", m.Name, key)
				}
			}
		}
	}
}

// TestCorpusSeriesCountStaysSane feeds real reduced turns through the sink and checks
// the total data-point count is proportionate to the number of turns, not exploding -
// a cheap guard against a combinatorial blow-up across this sink's several capped
// dimensions (gen_ai.tool.name, gen_ai.request.model, gen_ai.token.type, ...), each
// individually bounded by the shared Guard but not jointly bounded by any single cap.
func TestCorpusSeriesCountStaysSane(t *testing.T) {
	files := fixture.Any(t, 2)
	r := turn.New()
	var turns []*turn.Turn
	for _, p := range files {
		turns = append(turns, reduceFixture(t, r, p)...)
	}
	turns = append(turns, r.Flush()...)
	if len(turns) == 0 {
		t.Fatal("no turns reduced from the corpus sample")
	}

	s, reader, guard := newTestSink(t)
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	if err := s.Emit(context.Background(), turns); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	rm := collect(t, reader)

	total := 0
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			total += dataPointCount(t, m)
		}
	}
	t.Logf("%d turns -> %d total data points across %d instruments", len(turns), total, countInstruments(rm))
	if total == 0 {
		t.Fatal("no data points recorded from a non-empty corpus sample")
	}
	// Generous bound: even with every counter, histogram and gauge firing on every
	// turn plus a handful of tool calls each, this should stay a small multiple of
	// the turn count, not blow up into per-response cardinality.
	if max := len(turns) * 50; total > max {
		t.Errorf("%d data points from %d turns looks like a cardinality explosion (want <= %d)",
			total, len(turns), max)
	}

	if rejected := guard.Rejected(); len(rejected) > 0 {
		t.Logf("guard rejected values (expected occasionally on a real corpus): %v", rejected)
	}
}

func intPtr(v int) *int { return &v }

func sumInt64(t *testing.T, m metricdata.Metrics) int64 {
	t.Helper()
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("%s: not an int64 sum (got %T)", m.Name, m.Data)
	}
	var total int64
	for _, dp := range sum.DataPoints {
		total += dp.Value
	}
	return total
}

func histogramSum(t *testing.T, m metricdata.Metrics) float64 {
	t.Helper()
	h, ok := m.Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("%s: not a float64 histogram (got %T)", m.Name, m.Data)
	}
	var total float64
	for _, dp := range h.DataPoints {
		total += dp.Sum
	}
	return total
}

func histogramSumInt64(t *testing.T, m metricdata.Metrics) int64 {
	t.Helper()
	h, ok := m.Data.(metricdata.Histogram[int64])
	if !ok {
		t.Fatalf("%s: not an int64 histogram (got %T)", m.Name, m.Data)
	}
	var total int64
	for _, dp := range h.DataPoints {
		total += dp.Sum
	}
	return total
}

func dataPointKeys(t *testing.T, m metricdata.Metrics) []string {
	t.Helper()
	var keys []string
	collectSet := func(set attribute.Set) {
		for _, kv := range set.ToSlice() {
			keys = append(keys, string(kv.Key))
		}
	}
	switch d := m.Data.(type) {
	case metricdata.Sum[int64]:
		for _, dp := range d.DataPoints {
			collectSet(dp.Attributes)
		}
	case metricdata.Sum[float64]:
		for _, dp := range d.DataPoints {
			collectSet(dp.Attributes)
		}
	case metricdata.Histogram[float64]:
		for _, dp := range d.DataPoints {
			collectSet(dp.Attributes)
		}
	case metricdata.Histogram[int64]:
		// tokenUsage and toolCallsPerOperation (issue #18) are Int64Histogram, unlike
		// every histogram this sink built before them - all seconds-unit durations,
		// hence float64. Missing this case is not a compile error, only a t.Fatalf the
		// first time either instrument actually emits a data point, which is exactly
		// the kind of silent gap this package's own newInstruments doc comment warns
		// about for instrument construction; the same trap applies to reading them back.
		for _, dp := range d.DataPoints {
			collectSet(dp.Attributes)
		}
	case metricdata.Gauge[float64]:
		for _, dp := range d.DataPoints {
			collectSet(dp.Attributes)
		}
	case metricdata.Gauge[int64]:
		for _, dp := range d.DataPoints {
			collectSet(dp.Attributes)
		}
	default:
		t.Fatalf("%s: unhandled aggregation type %T", m.Name, m.Data)
	}
	return keys
}

func dataPointCount(t *testing.T, m metricdata.Metrics) int {
	t.Helper()
	switch d := m.Data.(type) {
	case metricdata.Sum[int64]:
		return len(d.DataPoints)
	case metricdata.Sum[float64]:
		return len(d.DataPoints)
	case metricdata.Histogram[float64]:
		return len(d.DataPoints)
	case metricdata.Histogram[int64]:
		return len(d.DataPoints)
	case metricdata.Gauge[float64]:
		return len(d.DataPoints)
	case metricdata.Gauge[int64]:
		return len(d.DataPoints)
	default:
		t.Fatalf("%s: unhandled aggregation type %T", m.Name, m.Data)
		return 0
	}
}

func countInstruments(rm metricdata.ResourceMetrics) int {
	n := 0
	for _, sm := range rm.ScopeMetrics {
		n += len(sm.Metrics)
	}
	return n
}

// reduceFixture mirrors internal/turn/reducer_test.go's unexported feed helper -
// duplicated rather than imported because it is unexported there and this package
// must not edit internal/turn to export it.
func reduceFixture(t *testing.T, r *turn.Reducer, path string) []*turn.Turn {
	t.Helper()
	res, err := archive.DecodeMembers(fixture.Load(t, path))
	if err != nil {
		t.Fatalf("%s: %v", fixture.Name(path), err)
	}
	var out []*turn.Turn
	err = frame.Lines(res.Data, func(rec *frame.Record) error {
		done, err := r.Add(rec)
		if done != nil {
			out = append(out, done)
		}
		return err
	})
	if err != nil {
		t.Fatalf("%s: %v", fixture.Name(path), err)
	}
	return out
}
