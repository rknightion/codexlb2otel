package otlptrace

import (
	"context"
	"testing"
	"time"

	"github.com/rknightion/codexlb2otel/internal/attr"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

// TestWave4IdentityFieldsReachSpanAttributes proves the trace sink consumes the
// shared SpanAttrs contract rather than dropping the new per-response identities.
// It also checks the same Turn against MetricAttrs: these values must stay out of
// metric dimensions, even though they belong on trace spans.
func TestWave4IdentityFieldsReachSpanAttributes(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	turnWithIdentity := &turn.Turn{
		RequestID:             "request-wave4",
		ResponseID:            "response-wave4",
		ThreadID:              "thread-wave4",
		Family:                "websocket",
		Status:                "completed",
		Model:                 "gpt-5.6-sol",
		FirstTS:               now,
		LastTS:                now.Add(time.Second),
		ServerCreatedAt:       now,
		ServerCompletedAt:     now.Add(time.Second),
		ConversationID:        "conversation-wave4",
		ProxySessionID:        "proxy-session-wave4",
		Workspaces:            `{"/workspace/project":["repo"]}`,
		TurnState:             "encrypted-turn-state-wave4",
		ModelsETag:            `W/"0123456789abcdef0123456789abcdef"`,
		ContextWindowID:       "context-window-wave4",
		PassthroughTurnID:     "turn-passthrough-wave4",
		PassthroughCreateTime: 123.5,
		SafetyID:              "safety-wave4",
	}

	want := map[string]string{
		attr.Workspaces:            turnWithIdentity.Workspaces,
		attr.TurnState:             turnWithIdentity.TurnState,
		attr.ConversationID:        turnWithIdentity.ConversationID,
		attr.ProxySessionID:        turnWithIdentity.ProxySessionID,
		attr.ModelsETag:            turnWithIdentity.ModelsETag,
		attr.ContextWindowID:       turnWithIdentity.ContextWindowID,
		attr.PassthroughTurnID:     turnWithIdentity.PassthroughTurnID,
		attr.PassthroughCreateTime: "123.5",
		attr.SafetyID:              turnWithIdentity.SafetyID,
	}

	metricAttrs := attr.NewGuard().MetricAttrs(turnWithIdentity)
	for _, kv := range metricAttrs {
		if _, identity := want[kv.Key]; identity {
			t.Errorf("identity field %s became a metric attribute with value %q", kv.Key, kv.Value)
		}
	}

	s, exp := newTestSink(t)
	if err := s.Emit(context.Background(), []*turn.Turn{turnWithIdentity}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	var turnSpanFound bool
	for _, span := range exp.GetSpans() {
		if span.Name != "turn" {
			continue
		}
		turnSpanFound = true
		got := make(map[string]string, len(span.Attributes))
		for _, attribute := range span.Attributes {
			got[string(attribute.Key)] = attribute.Value.AsString()
		}
		for key, value := range want {
			if got[key] != value {
				t.Errorf("turn span attribute[%s] = %q, want %q", key, got[key], value)
			}
		}
	}
	if !turnSpanFound {
		t.Fatal("trace sink emitted no turn span")
	}
}
