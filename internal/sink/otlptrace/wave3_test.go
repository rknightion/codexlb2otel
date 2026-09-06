package otlptrace

import (
	"context"
	"testing"
	"time"

	"github.com/rknightion/codexlb2otel/internal/turn"
)

func TestToolCallSpanIDs_DistinguishReusedCallIDs(t *testing.T) {
	s, exp := newTestSink(t)
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	thread := "thread-reused"
	callID := "call-shared"
	tn := &turn.Turn{
		RequestID: "request-reused", ResponseID: "response-reused", ThreadID: thread,
		Model: "model", Status: "completed", FirstTS: now, LastTS: now.Add(time.Second),
		ServerCreatedAt: now, ServerCompletedAt: now.Add(time.Second),
		ToolCalls: []turn.ToolCall{
			{CallID: callID, Name: "first", Kind: "function", CallOccurrence: 1},
			{CallID: callID, Name: "second", Kind: "function", CallOccurrence: 2},
		},
	}
	if err := s.Emit(context.Background(), []*turn.Turn{tn}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	got := make(map[string]string)
	for _, span := range toolCallSpans(exp.GetSpans()) {
		got[toolNameOf(span)] = span.SpanContext.SpanID().String()
	}
	if got["first"] != toolCallSpanID(thread, callID, 1).String() {
		t.Errorf("first tool-call span id = %q, want occurrence 1 id %q", got["first"], toolCallSpanID(thread, callID, 1))
	}
	if got["second"] != toolCallSpanID(thread, callID, 2).String() {
		t.Errorf("second tool-call span id = %q, want occurrence 2 id %q", got["second"], toolCallSpanID(thread, callID, 2))
	}
	if got["first"] == got["second"] {
		t.Fatalf("reused call ID produced one span ID: %q", got["first"])
	}
}

func TestToolResultLink_UsesMatchedCallOccurrence(t *testing.T) {
	s, exp := newTestSink(t)
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	thread := "thread-result"
	callID := "call-reused"
	origin := &turn.Turn{
		RequestID: "request-origin", ResponseID: "response-origin", ThreadID: thread,
		Model: "model", Status: "completed", FirstTS: now, LastTS: now.Add(time.Second),
		ServerCreatedAt: now, ServerCompletedAt: now.Add(time.Second),
		ToolCalls: []turn.ToolCall{{CallID: callID, Name: "tool", Kind: "function", CallOccurrence: 2}},
	}
	receiving := &turn.Turn{
		RequestID: "request-result", ResponseID: "response-result", ThreadID: thread,
		Model: "model", Status: "completed", FirstTS: now.Add(2 * time.Second), LastTS: now.Add(3 * time.Second),
		ServerCreatedAt: now.Add(2 * time.Second), ServerCompletedAt: now.Add(3 * time.Second),
		ToolOutputs: []turn.ToolOutput{{
			CallID: callID, Text: "result", OriginResponseID: origin.ResponseID,
			OriginTurnID: "turn-origin", OriginToolName: "tool", OriginMatch: "exact",
			OriginCallOccurrence: 2,
		}},
	}
	if err := s.Emit(context.Background(), []*turn.Turn{origin, receiving}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	var resultSpanName string
	var resultLinks []struct {
		spanID  string
		traceID string
	}
	for _, span := range exp.GetSpans() {
		if span.Name != "tool_result" {
			continue
		}
		resultSpanName = span.Name
		for _, link := range span.Links {
			resultLinks = append(resultLinks, struct {
				spanID  string
				traceID string
			}{link.SpanContext.SpanID().String(), link.SpanContext.TraceID().String()})
		}
	}
	if resultSpanName == "" {
		t.Fatal("exact cross-response tool result span was not emitted")
	}
	wantID := toolCallSpanID(thread, callID, 2).String()
	wantTraceID := traceID(receiving).String()
	var matched bool
	for _, link := range resultLinks {
		if link.spanID == wantID && link.traceID == wantTraceID {
			matched = true
		}
		if link.spanID == toolCallSpanID(thread, callID, 1).String() {
			t.Errorf("exact result linked to occurrence 1, want occurrence 2: %+v", link)
		}
	}
	if !matched {
		t.Fatalf("exact result links = %+v, want origin span %s in trace %s", resultLinks, wantID, wantTraceID)
	}
}

func TestToolResultLink_NonExactMatchHasNoOriginLink(t *testing.T) {
	for _, match := range []string{"ambiguous", "none"} {
		t.Run(match, func(t *testing.T) {
			s, exp := newTestSink(t)
			now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
			thread := "thread-nonexact-" + match
			callID := "call-shared"
			receiving := &turn.Turn{
				RequestID: "request-" + match, ResponseID: "response-" + match, ThreadID: thread,
				Model: "model", Status: "completed", FirstTS: now, LastTS: now.Add(time.Second),
				ServerCreatedAt: now, ServerCompletedAt: now.Add(time.Second),
				ToolCalls: []turn.ToolCall{{CallID: "other-call", Name: "other", Kind: "function", CallOccurrence: 1}},
				ToolOutputs: []turn.ToolOutput{{
					CallID: callID, Text: "result", OriginResponseID: "response-origin",
					OriginToolName: "tool", OriginMatch: match, OriginCallOccurrence: 2,
				}},
			}
			if err := s.Emit(context.Background(), []*turn.Turn{receiving}); err != nil {
				t.Fatalf("emit: %v", err)
			}
			if err := s.Flush(context.Background()); err != nil {
				t.Fatalf("flush: %v", err)
			}

			for _, span := range exp.GetSpans() {
				for _, link := range span.Links {
					if link.SpanContext.SpanID() == toolCallSpanID(thread, callID, 1) ||
						link.SpanContext.SpanID() == toolCallSpanID(thread, callID, 2) {
						t.Errorf("%s result carried an origin link: %+v", match, link)
					}
				}
			}
		})
	}
}

func TestToolCallSpanID_UniqueCaseKeepsLegacyValue(t *testing.T) {
	const want = "ecfd3b8786c25b11"
	for _, occurrence := range []int{0, 1} {
		if got := toolCallSpanID("thread-1", "call-1", occurrence).String(); got != want {
			t.Errorf("occurrence %d tool-call span id = %s, want pinned legacy id %s", occurrence, got, want)
		}
	}
}
