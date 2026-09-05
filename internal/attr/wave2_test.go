package attr

import (
	"github.com/rknightion/codexlb2otel/internal/turn"
	"testing"
)

func TestWave2DiagnosticsNeverBecomeDimensions(t *testing.T) {
	tn := &turn.Turn{TurnTrigger: "goal", SafetyReasons: []string{"probes"}, ReasoningCtx: "all_turns", ParallelTools: true, UpstreamStatusCode: 503, UpstreamErrorCode: "overloaded", UpstreamTransport: "http"}
	g := NewGuard()
	for _, key := range []string{TurnTrigger, SafetyBufferingReasons, ReasoningContext, ParallelToolCalls, "codexlb.upstream.status_code", "codexlb.upstream.error_code", "codexlb.upstream.transport", "codexlb.tool.origin_match"} {
		if ValidateLabels([]string{key}) == nil {
			t.Errorf("label allowed: %s", key)
		}
		for _, kv := range g.MetricAttrs(tn) {
			if kv.Key == key {
				t.Errorf("metric dimension: %s", key)
			}
		}
	}
	for _, key := range []string{TurnTrigger, SafetyBufferingReasons, ReasoningContext, ParallelToolCalls, "codexlb.upstream.status_code", "codexlb.upstream.error_code", "codexlb.upstream.transport"} {
		found := false
		for _, kv := range g.SpanAttrs(tn) {
			if kv.Key == key {
				found = true
			}
		}
		if !found {
			t.Errorf("missing span diagnostic: %s", key)
		}
	}
}
