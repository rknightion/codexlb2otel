package otlptrace

import (
	"context"
	"testing"
	"time"

	"github.com/rknightion/codexlb2otel/internal/attr"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

// gen_ai.operation.name is the discriminator a GenAI trace consumer keys on, and
// gen_ai.provider.name is required alongside it. Neither is Turn-derived, so
// attr.Guard leaves both out of SpanAttrs - which meant no span carried either and
// every span was invisible to anything reading the convention.
//
// EXACTLY ONE span per response may claim to be the inference. A consumer that turns
// each qualifying span into a record counts the turn once per claim, so putting these
// on the root as well would double-count every turn.
func TestSemconv_OnlyTheResponseSpanClaimsToBeTheInference(t *testing.T) {
	s, exp := newTestSink(t)

	if err := s.Emit(context.Background(), []*turn.Turn{{
		RequestID: "ws_1", ResponseID: "resp_1", ThreadID: "019fdb99-a66a", TurnID: "t1",
		Model: "gpt-5.6-sol", Status: "completed", Family: "websocket", RequestKind: "turn",
		AccountID: "acct-1", FirstTS: time.Now().Add(-time.Minute), LastTS: time.Now(),
		ServerCreatedAt: time.Now().Add(-time.Minute), ServerCompletedAt: time.Now(),
		ToolCalls: []turn.ToolCall{{Kind: "custom", Name: "exec", CallID: "c1"}},
	}}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	var claimants []string
	for _, sp := range exp.GetSpans() {
		var op, provider string
		for _, a := range sp.Attributes {
			switch string(a.Key) {
			case attr.GenAIOperation:
				op = a.Value.AsString()
			case attr.GenAIProvider:
				provider = a.Value.AsString()
			}
		}
		if op == "" && provider == "" {
			continue
		}
		if op == "" || provider == "" {
			t.Errorf("span %q carries one of operation/provider but not both (%q/%q); "+
				"the convention requires both on an inference span", sp.Name, op, provider)
		}
		claimants = append(claimants, sp.Name)
	}

	if len(claimants) != 1 {
		t.Fatalf("%d spans claim to be the inference (%v); want exactly the response span, "+
			"or a consumer counts this turn once per claimant", len(claimants), claimants)
	}
	if claimants[0] != "chat gpt-5.6-sol" {
		t.Errorf("the inference span is %q, want the chat span", claimants[0])
	}
}
