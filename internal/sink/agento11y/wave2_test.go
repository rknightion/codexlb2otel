package agento11y

import (
	"testing"

	"github.com/rknightion/codexlb2otel/internal/turn"
)

func TestMessagesInterleaveByOrdinalAndPreserveRedactionMarker(t *testing.T) {
	input, _ := inputMessages(&turn.Turn{Prompts: []turn.Prompt{
		{Ordinal: 4, Role: "user", Text: "later prompt"},
		{Ordinal: 1, Role: "developer", Text: "unsupported role"},
	}, ToolOutputs: []turn.ToolOutput{{Ordinal: 2, CallID: "call-1", Text: "result"}}})
	if len(input) != 2 || input[0].Role != roleTool || input[1].Role != roleUser {
		t.Fatalf("input ordering = %+v, want tool result then supported user prompt", input)
	}

	output := outputMessages(&turn.Turn{Messages: []turn.Message{{Ordinal: 3, Text: "message"}}, ToolCalls: []turn.ToolCall{{Ordinal: 2, CallID: "call-1", Name: "tool", Input: `{"key":"[omitted: encrypted]"}`, InputChars: 28, InputOmitted: 1}}})
	if len(output) != 2 || output[0].Parts[0].ToolCall == nil || output[1].Parts[0].Text != "message" {
		t.Fatalf("output ordering = %+v, want call then message", output)
	}
	if got := string(output[0].Parts[0].ToolCall.InputJSON); got != `{"key":"[omitted: encrypted]"}` {
		t.Errorf("tool input = %q, want redaction marker preserved", got)
	}
}
