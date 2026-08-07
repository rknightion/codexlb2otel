package loki

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/rknightion/codexlb2otel/internal/attr"
	"github.com/rknightion/codexlb2otel/internal/sink"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

// fakeRejecter records addRejected calls without needing a full Sink.
type fakeRejecter struct {
	counts map[string]int64
}

func newFakeRejecter() *fakeRejecter { return &fakeRejecter{counts: map[string]int64{}} }

func (f *fakeRejecter) addRejected(reason string, n int64) { f.counts[reason] += n }

// TestBuildLines_MetadataSurvivesOversizedToolOutput is, per the brief, the single
// most important test in this lane: a turn whose tool output overflows the line
// budget must still emit its metadata record intact - token counts, model
// attribution, timing all present - rather than losing the whole record the way a
// bundled turn+content line would under Loki's max_line_size.
func TestBuildLines_MetadataSurvivesOversizedToolOutput(t *testing.T) {
	// Turn's bare JSON footprint (identity, timing, tokens - no content arrays) runs
	// a bit under 1KB because several zero-value time.Time fields on the frozen Turn
	// struct lack omitempty and still serialize as "0001-01-01T00:00:00Z". 2000 is
	// comfortably above that floor while remaining a 25x-smaller budget than the
	// oversized tool output below, so truncation is still unambiguously forced there.
	const budget = 2000
	now := time.Now().UTC()
	huge := strings.Repeat("x", 50_000) // far larger than the budget

	tn := &turn.Turn{
		RequestID:         "req-huge",
		ResponseID:        "resp-huge",
		ThreadID:          "thread-huge",
		Model:             "gpt-5.6-terra",
		Status:            "completed",
		Family:            "websocket",
		FirstTS:           now.Add(-3 * time.Second),
		LastTS:            now,
		ServerCompletedAt: now,
		InputTokens:       12345,
		OutputTokens:      6789,
		TotalTokens:       19134,
		ToolOutputs:       []turn.ToolOutput{{CallID: "call-1", Chars: len(huge), Truncated: false, Text: huge}},
	}

	guard := attr.NewGuard()
	rej := newFakeRejecter()
	lines := buildLines(tn, guard, "codexlb2otel", attr.DefaultLabels, budget, nil, rej)

	var turnLine, toolOutputLine *outLine
	for i := range lines {
		switch lines[i].recordType {
		case attr.RecordTurn:
			turnLine = &lines[i]
		case attr.RecordToolOutput:
			toolOutputLine = &lines[i]
		}
	}

	if turnLine == nil {
		t.Fatal("no turn-metadata line was emitted at all")
	}
	if len(turnLine.body) > budget {
		t.Fatalf("turn-metadata line is %d bytes, over the %d budget - it must never be inflated by tool output", len(turnLine.body), budget)
	}

	// The metadata line must decode back to the real numbers, proving nothing was
	// lost - not just that A line exists, but that it is the RIGHT line.
	var decoded turn.Turn
	if err := json.Unmarshal(turnLine.body, &decoded); err != nil {
		t.Fatalf("turn-metadata line is not valid JSON: %v\nbody: %s", err, turnLine.body)
	}
	if decoded.RequestID != "req-huge" || decoded.Model != "gpt-5.6-terra" {
		t.Fatalf("turn-metadata line lost identity/model: %+v", decoded)
	}
	if decoded.InputTokens != 12345 || decoded.OutputTokens != 6789 || decoded.TotalTokens != 19134 {
		t.Fatalf("turn-metadata line lost token counts: %+v", decoded)
	}
	if len(decoded.ToolOutputs) != 0 {
		t.Fatalf("turn-metadata line must not carry the content arrays, got %d tool outputs inline", len(decoded.ToolOutputs))
	}

	// The tool-output line itself must be truncated (not silently dropped, and not
	// shipped oversized), with the marker and the ORIGINAL length both present.
	if toolOutputLine == nil {
		t.Fatal("the oversized tool output must still produce a (truncated) line, not be silently dropped")
	}
	if len(toolOutputLine.body) > budget {
		t.Fatalf("tool_output line is %d bytes, exceeds the %d budget", len(toolOutputLine.body), budget)
	}
	var decodedTO turn.ToolOutput
	if err := json.Unmarshal(toolOutputLine.body, &decodedTO); err != nil {
		t.Fatalf("tool_output line is not valid JSON: %v\nbody: %s", err, toolOutputLine.body)
	}
	if decodedTO.Chars != len(huge) {
		t.Fatalf("tool_output line lost the original untruncated length: got Chars=%d, want %d", decodedTO.Chars, len(huge))
	}
	if !strings.Contains(decodedTO.Text, fmt.Sprintf("%d", len(huge))) {
		t.Fatalf("truncated text has no marker naming the original length: %q", decodedTO.Text)
	}

	if rej.counts[sink.ReasonLineTooLong] != 0 {
		t.Fatalf("nothing should be dropped here - both lines fit after truncation, got rejections: %+v", rej.counts)
	}
}

// TestBuildLines_DropsWhenNothingFitsAtAll covers the escape hatch: a budget so
// small that not even an empty text field plus the marker fits. The line must be
// dropped and counted, never shipped over budget.
func TestBuildLines_DropsWhenNothingFitsAtAll(t *testing.T) {
	const budget = 10 // smaller than the marker itself
	tn := &turn.Turn{
		RequestID: "req-tiny",
		ThreadID:  "thread-tiny",
		FirstTS:   time.Now(),
		LastTS:    time.Now(),
		Prompts:   []turn.Prompt{{Role: "user", Chars: 5000, Text: strings.Repeat("y", 5000)}},
	}
	guard := attr.NewGuard()
	rej := newFakeRejecter()
	enabled := map[string]bool{attr.RecordPrompt: true}
	lines := buildLines(tn, guard, "codexlb2otel", attr.DefaultLabels, budget, enabled, rej)

	for _, l := range lines {
		if len(l.body) > budget {
			t.Fatalf("no emitted line may exceed the budget; record_type=%s is %d bytes", l.recordType, len(l.body))
		}
	}
	if rej.counts[sink.ReasonLineTooLong] == 0 {
		t.Fatal("the prompt line could not possibly fit in 10 bytes and must be counted as dropped")
	}
}

// TestBuildLines_RespectsRecordTypesFilter confirms an empty/nil filter emits every
// type and a configured filter emits only what is listed.
func TestBuildLines_RespectsRecordTypesFilter(t *testing.T) {
	tn := sampleTurn()
	tn.Prompts = []turn.Prompt{{Role: "user", Chars: 4, Text: "hi"}}
	tn.ToolCalls = []turn.ToolCall{{Kind: "function", Name: "exec", CallID: "c1", InputChars: 3, Input: "ls"}}
	guard := attr.NewGuard()

	all := buildLines(tn, guard, "svc", attr.DefaultLabels, 192<<10, nil, newFakeRejecter())
	if len(all) < 3 { // turn + prompt + message + tool_call at least
		t.Fatalf("nil filter should emit every applicable record type, got %d lines", len(all))
	}

	only := map[string]bool{attr.RecordTurn: true}
	filtered := buildLines(tn, guard, "svc", attr.DefaultLabels, 192<<10, only, newFakeRejecter())
	if len(filtered) != 1 || filtered[0].recordType != attr.RecordTurn {
		t.Fatalf("filter to just %q produced %+v", attr.RecordTurn, filtered)
	}
}

// TestBuildLines_TimestampChoicePerRecordType pins the deliberate per-record-type
// timestamp selection documented in timestamps.go.
func TestBuildLines_TimestampChoicePerRecordType(t *testing.T) {
	first := time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC)
	last := first.Add(5 * time.Second)
	completed := first.Add(4 * time.Second)

	tn := &turn.Turn{
		RequestID:         "req-ts",
		ThreadID:          "thread-ts",
		FirstTS:           first,
		LastTS:            last,
		ServerCompletedAt: completed,
		Prompts:           []turn.Prompt{{Role: "user", Chars: 2, Text: "hi"}},
		Messages:          []turn.Message{{Phase: "final_answer", Chars: 2, Text: "hi"}},
	}
	guard := attr.NewGuard()
	lines := buildLines(tn, guard, "svc", attr.DefaultLabels, 192<<10, nil, newFakeRejecter())

	byType := map[string]time.Time{}
	for _, l := range lines {
		byType[l.recordType] = l.ts
	}
	if !byType[attr.RecordPrompt].Equal(first) {
		t.Errorf("prompt ts = %v, want FirstTS %v (input-side content)", byType[attr.RecordPrompt], first)
	}
	if !byType[attr.RecordMessage].Equal(completed) {
		t.Errorf("message ts = %v, want ServerCompletedAt %v (output-side content)", byType[attr.RecordMessage], completed)
	}
	if !byType[attr.RecordTurn].Equal(completed) {
		t.Errorf("turn ts = %v, want ServerCompletedAt %v", byType[attr.RecordTurn], completed)
	}
}

func TestSafeCut_NeverSplitsAUTF8Rune(t *testing.T) {
	s := "héllo wörld 日本語"
	for n := 0; n <= len(s)+2; n++ {
		cut := safeCut(s, n)
		if !strings.HasPrefix(s, cut) {
			t.Fatalf("safeCut(%q, %d) = %q is not a prefix of the source", s, n, cut)
		}
		if !utf8.ValidString(cut) {
			t.Fatalf("safeCut(%q, %d) = %q split a UTF-8 rune", s, n, cut)
		}
	}
}

func TestFitLine_ReturnsAsIsWhenAlreadyUnderBudget(t *testing.T) {
	body, ok, err := fitLine(1000, "short", func(text string) ([]byte, error) {
		return []byte(`{"text":"` + text + `"}`), nil
	})
	if err != nil || !ok {
		t.Fatalf("fitLine() = (%s, %v, %v), want a clean fit", body, ok, err)
	}
	if string(body) != `{"text":"short"}` {
		t.Fatalf("fitLine should not alter content that already fits: %s", body)
	}
}
