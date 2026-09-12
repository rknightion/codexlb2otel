package otlpmetric

import (
	"context"
	"slices"
	"testing"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/rknightion/codexlb2otel/internal/accountpoll"
	"github.com/rknightion/codexlb2otel/internal/attr"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

func TestWave4TurnMetricsEmitFrozenAttributeSets(t *testing.T) {
	s, reader, _ := newTestSink(t)
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	used, reset := 37.5, 42.0
	tt := baseTurn("wave4-turn-metrics")
	tt.ConnectionKind = "normal"
	tt.PlanType = "prolite"
	tt.ErrorType = "usage_limit_reached"
	tt.RateLimitWindowMin = 300
	tt.RateLimitReached = true
	tt.ExtraRateLimitReached = map[string]bool{"gpt-reserve": false}
	tt.QuotaFailureActiveLimit = "premium"
	tt.QuotaFailureLimits = []turn.QuotaFailureLimit{{
		Family: "codex",
		Windows: []turn.QuotaFailureWindow{{
			Window: "primary", UsedPercent: &used, ResetAfterSeconds: &reset,
		}},
	}}
	tt.RoutingHintAgreement = "disagree"
	tt.ContentItemKinds = map[string]int{"unknown": 2}
	tt.CompactionTrigger = "manual"
	tt.CompactionReason = "user_requested"
	tt.CompactionImplementation = "responses_compaction_v2"
	tt.CompactionPhase = "standalone_turn"
	tt.CompactionStrategy = "memento"
	tt.ServiceTierOutcome = "downgraded"
	tt.StickyKind = "prompt_cache"
	tt.StickyKeySource = "thread_header"
	tt.ProxyLatencyMS = 2500
	tt.ProxyFirstTokenMS = 125

	if err := s.Emit(context.Background(), []*turn.Turn{tt}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	rm := collect(t, reader)

	want := map[string][]string{
		attr.MetricRateLimitReached:      {attr.AccountID},
		attr.MetricRateLimitModelReached: {attr.AccountID, attr.GenAIRequestModel},
		attr.MetricQuotaFailures:         {attr.AccountID, attr.PlanType, attr.QuotaActiveLimit, attr.ErrorType},
		attr.MetricQuotaFailureUsed:      {attr.AccountID, attr.QuotaLimitFamily, attr.QuotaWindow},
		attr.MetricQuotaFailureReset:     {attr.AccountID, attr.QuotaLimitFamily, attr.QuotaWindow},
		attr.MetricRoutingHintAgreement:  {attr.RoutingHintAgreement, attr.Family, attr.ConnectionKind},
		attr.MetricContentItemKinds:      {attr.ContentItemKind, attr.Family},
		attr.MetricCompactions:           {attr.CompactionTrigger, attr.CompactionReason, attr.CompactionStrategy, attr.CompactionPhase},
		attr.MetricServiceTierOutcome:    {attr.ServiceTierOutcome, attr.GenAIRequestModel, attr.Family},
		attr.MetricStickyRouting:         {attr.StickyKind, attr.StickyKeySource, attr.Family},
		attr.MetricProxyLatency:          {attr.Family, attr.ConnectionKind, attr.GenAIRequestModel},
		attr.MetricProxyFirstToken:       {attr.Family, attr.ConnectionKind, attr.GenAIRequestModel},
	}
	for name, keys := range want {
		m, ok := findMetric(rm, name)
		if !ok {
			t.Fatalf("%s was not emitted", name)
		}
		sets := attrSets(t, m)
		if len(sets) != 1 {
			t.Fatalf("%s emitted %d data points, want 1", name, len(sets))
		}
		got := keysOf(sets[0])
		slices.Sort(keys)
		if !slices.Equal(got, keys) {
			t.Errorf("%s attribute keys = %v, want exactly %v", name, got, keys)
		}
	}

	quotaFailures, _ := findMetric(rm, attr.MetricQuotaFailures)
	if got := sumInt64(t, quotaFailures); got != 1 {
		t.Errorf("%s sum = %d, want 1", attr.MetricQuotaFailures, got)
	}
	contentKinds, _ := findMetric(rm, attr.MetricContentItemKinds)
	if got := sumInt64(t, contentKinds); got != 2 {
		t.Errorf("%s sum = %d, want 2", attr.MetricContentItemKinds, got)
	}
	latency, _ := findMetric(rm, attr.MetricProxyLatency)
	if got := histogramSum(t, latency); got != 2.5 {
		t.Errorf("%s sum = %v, want 2.5 seconds", attr.MetricProxyLatency, got)
	}
	firstToken, _ := findMetric(rm, attr.MetricProxyFirstToken)
	if got := histogramSum(t, firstToken); got != 0.125 {
		t.Errorf("%s sum = %v, want 0.125 seconds", attr.MetricProxyFirstToken, got)
	}
	assertWave4Gauge(t, rm, attr.MetricRateLimitReached, 1)
	assertWave4Gauge(t, rm, attr.MetricRateLimitModelReached, 0)
	assertWave4Gauge(t, rm, attr.MetricQuotaFailureUsed, used)
	assertWave4Gauge(t, rm, attr.MetricQuotaFailureReset, reset)
}

func TestWave4AccountMetricsReadSnapshotWithFrozenAttributeSets(t *testing.T) {
	s, reader, _ := newTestSink(t)
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	used, reset, modelUsed, credits := 12.5, 90.0, 37.5, 4.25
	snapshot := accountpoll.Snapshot{Accounts: []accountpoll.Account{{
		AccountID: "11111111-1111-1111-1111-111111111111",
		Email:     "account@example.invalid",
		Status:    "active", PlanType: "prolite", RoutingPolicy: "burn_first",
		Quotas: []accountpoll.Quota{{
			Window: "primary", UsedPercent: &used, ResetAfterSeconds: &reset, WindowMinutes: 300,
		}},
		ModelQuotas: []accountpoll.ModelQuota{{
			QuotaKey: "gpt_reserve", LimitName: "gpt-reserve", Window: "primary", UsedPercent: &modelUsed, WindowMinutes: 300,
		}},
		CreditsBalance: &credits,
		APIKeys:        []accountpoll.APIKeyEligibility{{Name: "key-a", Eligible: false}},
	}}}
	if err := s.registerAccountSnapshot(func() accountpoll.Snapshot { return snapshot }); err != nil {
		t.Fatalf("registerAccountSnapshot: %v", err)
	}

	rm := collect(t, reader)
	want := map[string][]string{
		attr.MetricAccountQuotaUsed:      {attr.AccountID, attr.QuotaWindow, attr.PlanType, attr.RateLimitWindowMinutes},
		attr.MetricAccountQuotaReset:     {attr.AccountID, attr.QuotaWindow, attr.RateLimitWindowMinutes},
		attr.MetricAccountModelQuotaUsed: {attr.AccountID, attr.QuotaKey, attr.QuotaWindow, attr.RateLimitWindowMinutes},
		attr.MetricAccountCreditsBalance: {attr.AccountID},
		attr.MetricAccountInfo:           {attr.AccountID, attr.AccountEmail, attr.AccountStatus, attr.PlanType, attr.AccountRoutingPolicy},
		attr.MetricAccountAPIKeyEligible: {attr.AccountID, attr.APIKeyName},
	}
	for name, keys := range want {
		m, ok := findMetric(rm, name)
		if !ok {
			t.Fatalf("%s was not emitted", name)
		}
		sets := attrSets(t, m)
		if len(sets) != 1 {
			t.Fatalf("%s emitted %d data points, want 1", name, len(sets))
		}
		got := keysOf(sets[0])
		slices.Sort(keys)
		if !slices.Equal(got, keys) {
			t.Errorf("%s attribute keys = %v, want exactly %v", name, got, keys)
		}
	}
	assertWave4Gauge(t, rm, attr.MetricAccountQuotaUsed, used)
	assertWave4Gauge(t, rm, attr.MetricAccountQuotaReset, reset)
	assertWave4Gauge(t, rm, attr.MetricAccountModelQuotaUsed, modelUsed)
	assertWave4Gauge(t, rm, attr.MetricAccountCreditsBalance, credits)
	assertWave4Gauge(t, rm, attr.MetricAccountInfo, 1)
	assertWave4Gauge(t, rm, attr.MetricAccountAPIKeyEligible, 0)
}

func assertWave4Gauge(t *testing.T, rm metricdata.ResourceMetrics, name string, want float64) {
	t.Helper()
	m, ok := findMetric(rm, name)
	if !ok {
		t.Fatalf("%s was not emitted", name)
	}
	gauge, ok := m.Data.(metricdata.Gauge[float64])
	if !ok || len(gauge.DataPoints) != 1 {
		t.Fatalf("%s has data %T with %d points, want one float64 gauge point", name, m.Data, len(gauge.DataPoints))
	}
	if got := gauge.DataPoints[0].Value; got != want {
		t.Errorf("%s value = %v, want %v", name, got, want)
	}
}
