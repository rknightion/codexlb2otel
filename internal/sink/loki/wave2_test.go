package loki

import (
	"bytes"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/rknightion/codexlb2otel/internal/attr"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

func TestBuildLines_ContentCaptureMetadataAndOrdinalOrdering(t *testing.T) {
	ts := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	tn := &turn.Turn{
		FirstTS: ts, LastTS: ts, ServerCompletedAt: ts,
		Prompts:       []turn.Prompt{{Ordinal: 4, ItemID: "prompt-4", CapturedAt: ts.Add(time.Second), Provenance: "live", Role: "user", Text: "alpha"}},
		Messages:      []turn.Message{{Ordinal: 5, ItemID: "message-5", CapturedAt: ts.Add(2 * time.Second), Provenance: "live", Text: "beta"}},
		ToolCalls:     []turn.ToolCall{{Ordinal: 1, ItemID: "call-1", CapturedAt: ts.Add(3 * time.Second), Provenance: "replayed", Kind: "function", Name: "tool", CallID: "call-1", Input: `{"secret":"[omitted: encrypted]"}`, InputChars: 31, InputOmitted: 1, InputTruncated: true}},
		ToolOutputs:   []turn.ToolOutput{{Ordinal: 2, ItemID: "result-2", CapturedAt: ts.Add(4 * time.Second), Provenance: "live", CallID: "call-1", Text: "gamma", OriginResponseID: "response-0", OriginTurnID: "turn-0", OriginToolName: "tool", OriginMatch: "exact"}},
		AgentMessages: []turn.AgentMessage{{Ordinal: 3, ItemID: "agent-3", CapturedAt: ts.Add(5 * time.Second), Provenance: "live", Text: "delta"}},
	}
	enabled := map[string]bool{
		attr.RecordPrompt: true, attr.RecordMessage: true, attr.RecordToolCall: true,
		attr.RecordToolOutput: true, attr.RecordAgentMessage: true,
	}
	lines := buildLines(tn, attr.NewGuard(), "svc", attr.DefaultLabels, 192<<10, enabled, newFakeRejecter())
	if len(lines) != 5 {
		t.Fatalf("lines = %d, want 5", len(lines))
	}
	for i, line := range lines {
		wantOrdinal := i + 1
		metadata := kvMap(line.metadata)
		if got := metadata["codexlb_content_ordinal"]; got != strconv.Itoa(wantOrdinal) {
			t.Errorf("line %d ordinal metadata = %q, want %d", i, got, wantOrdinal)
		}
		for _, key := range []string{"codexlb_content_item_id", "codexlb_content_captured_at", "codexlb_content_provenance"} {
			if metadata[key] == "" {
				t.Errorf("line %d missing %s structured metadata: %+v", i, key, metadata)
			}
		}
	}

	var call turn.ToolCall
	if err := json.Unmarshal(lines[0].body, &call); err != nil {
		t.Fatal(err)
	}
	if call.Input != `{"secret":"[omitted: encrypted]"}` || call.InputChars != 31 || call.InputOmitted != 1 || !call.InputTruncated {
		t.Errorf("tool call = %+v, want preserved input fields", call)
	}
	var result turn.ToolOutput
	if err := json.Unmarshal(lines[1].body, &result); err != nil {
		t.Fatal(err)
	}
	if result.OriginResponseID != "response-0" || result.OriginTurnID != "turn-0" || result.OriginToolName != "tool" || result.OriginMatch != "exact" {
		t.Errorf("tool result origin = %+v, want exact origin metadata", result)
	}
}

func TestToolCallLineTruncatesAsValidArgumentsJSON(t *testing.T) {
	input := `{"key":"` + string(bytes.Repeat([]byte("x"), 4096)) + `"}`
	body, ok := toolCallLine(turn.ToolCall{Kind: "function", Name: "tool", Input: input, InputChars: len(input)}, 512)
	if !ok {
		t.Fatal("tool call line did not fit after input replacement")
	}
	var got turn.ToolCall
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if !got.InputTruncated || got.InputChars != len(input) {
		t.Errorf("truncation fields = %+v, want InputTruncated with original chars", got)
	}
	if !json.Valid([]byte(got.Input)) {
		t.Errorf("replacement input = %q, want valid JSON", got.Input)
	}
}
