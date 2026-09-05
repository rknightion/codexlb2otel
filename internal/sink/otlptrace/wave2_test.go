package otlptrace

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/rknightion/codexlb2otel/internal/attr"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

func TestToolCallIDAndCrossResponseResultLink(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	tn := &turn.Turn{
		RequestID: "request-1", ResponseID: "response-1", ThreadID: "thread-1", Model: "model", Status: "completed",
		FirstTS: now, LastTS: now.Add(time.Second), ServerCreatedAt: now, ServerCompletedAt: now.Add(time.Second),
		ToolCalls: []turn.ToolCall{{Ordinal: 1, ItemID: "item-call", CapturedAt: now, Provenance: "live", CallID: "call-1", Name: "tool", Kind: "function", Input: `{"key":"[omitted: encrypted]"}`, InputChars: 28, InputOmitted: 1}},
	}
	result := &turn.Turn{
		RequestID: "request-2", ResponseID: "response-2", ThreadID: "thread-1", Model: "model", Status: "completed",
		FirstTS: now.Add(2 * time.Second), LastTS: now.Add(3 * time.Second), ServerCreatedAt: now.Add(2 * time.Second), ServerCompletedAt: now.Add(3 * time.Second),
		ToolOutputs: []turn.ToolOutput{{Ordinal: 2, ItemID: "item-result", CapturedAt: now.Add(2 * time.Second), Provenance: "replayed", CallID: "call-1", Text: "result", OriginResponseID: "response-1", OriginTurnID: "turn-1", OriginToolName: "tool", OriginMatch: "exact"}},
	}
	s, exp := newTestSink(t)
	if err := s.Emit(context.Background(), []*turn.Turn{tn, result}); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}

	wantID := hashSpanID("thread-1", "call-1")
	var callSpan, resultSpan *tracetest.SpanStub
	for _, span := range exp.GetSpans() {
		switch span.Name {
		case "execute_tool tool":
			callSpan = &span
		case "tool_result":
			resultSpan = &span
		}
	}
	if callSpan == nil || callSpan.SpanContext.SpanID() != wantID {
		t.Fatalf("tool call span id = %v, want %v", callSpan, wantID)
	}
	if resultSpan == nil {
		t.Fatal("missing cross-response tool_result span")
	}
	if got := resultSpan.Parent.SpanID(); got != responseSpanID(result) {
		t.Errorf("tool result parent = %s, want receiving response %s", got, responseSpanID(result))
	}
	var linked bool
	for _, link := range resultSpan.Links {
		if link.SpanContext.SpanID() == wantID && link.SpanContext.TraceID() == traceID(result) {
			linked = true
		}
	}
	if !linked {
		t.Errorf("tool result links = %+v, want origin tool-call link", resultSpan.Links)
	}
	attrs := map[string]string{}
	for _, a := range resultSpan.Attributes {
		attrs[string(a.Key)] = a.Value.AsString()
	}
	if attrs[attr.ToolOriginResponseID] != "response-1" || attrs[attr.ToolOriginMatch] != "exact" {
		t.Errorf("result attrs = %+v, want origin response and exact match", attrs)
	}
	if attrs[attr.ContentOrdinal] != "2" || attrs[attr.ContentItemID] != "item-result" || attrs[attr.ContentProvenance] != "replayed" {
		t.Errorf("result content attrs = %+v, want capture metadata", attrs)
	}
}

func TestSameResponseOutputStaysOnToolCall(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	tn := &turn.Turn{RequestID: "request", ResponseID: "response", ThreadID: "thread", Model: "model", Status: "completed", FirstTS: now, LastTS: now, ServerCreatedAt: now, ServerCompletedAt: now,
		ToolCalls: []turn.ToolCall{{CallID: "call", Name: "tool", Kind: "function"}}, ToolOutputs: []turn.ToolOutput{{CallID: "call", Text: "result", OriginResponseID: "response", OriginMatch: "exact"}}}
	s, exp := newTestSink(t)
	if err := s.Emit(context.Background(), []*turn.Turn{tn}); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, span := range exp.GetSpans() {
		if span.Name == "tool_result" {
			t.Fatal("same-response output emitted a separate tool_result span")
		}
	}
}
