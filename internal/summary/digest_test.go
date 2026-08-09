package summary

import (
	"strings"
	"testing"
	"time"

	"github.com/rknightion/codexlb2otel/internal/turn"
)

func at(min int) time.Time {
	return time.Date(2026, 8, 8, 9, min, 0, 0, time.UTC)
}

func TestBuild_KeepsWhatSaysWorkHappened(t *testing.T) {
	turns := []*turn.Turn{{
		FirstTS: at(0),
		Status:  "completed",
		Prompts: []turn.Prompt{{Role: "user", Text: "add adaptive retention to the live view"}},
		ToolCalls: []turn.ToolCall{
			{Name: "shell", Input: `{"command":["go","test","./internal/live/"]}`},
			{Name: "spawn_agent", TaskName: "reviewer", Input: `{"task":"review it"}`},
		},
		ToolOutputs:   []turn.ToolOutput{{Text: "ok  internal/live  0.4s"}},
		Messages:      []turn.Message{{Text: "Retention is now adaptive."}},
		AgentMessages: []turn.AgentMessage{{Author: "/root", Recipient: "/root/reviewer", Text: "check live.go"}},
	}}

	got := Build(turns, Options{})
	if got.Passes() != 1 {
		t.Fatalf("Passes() = %d, want 1", got.Passes())
	}
	body := got.Chunks[0]

	for _, want := range []string{
		"add adaptive retention to the live view", // the ask
		`"command":["go","test"`,                  // the tool call arguments
		"spawns reviewer",                         // the spawn, by task name
		"ok  internal/live  0.4s",                 // the result
		"Retention is now adaptive.",              // the agent's own account
		"/root -> /root/reviewer",                 // the inter-agent topology
	} {
		if !strings.Contains(body, want) {
			t.Errorf("digest is missing %q:\n%s", want, body)
		}
	}
}

// Telemetry belongs to Grafana. Sending it costs budget that should be spent on content,
// and invites the model to write the report card the metrics pipeline already produces
// more accurately.
func TestBuild_SendsNoTelemetry(t *testing.T) {
	turns := []*turn.Turn{{
		FirstTS:              at(0),
		Model:                "gpt-5.3-codex",
		Effort:               "high",
		InputTokens:          123456,
		OutputTokens:         7890,
		ReasoningTokens:      4242,
		TTFTMs:               987.5,
		ServiceTier:          "priority",
		InstructionsHash:     "abc123",
		InstructionsChars:    67000,
		Tools:                []turn.ToolDef{{Name: "shell", Description: "run a command"}},
		RateLimitUsedPercent: 88.5,
		Messages:             []turn.Message{{Text: "done"}},
	}}

	body := Build(turns, Options{}).Chunks[0]
	for _, never := range []string{"123456", "7890", "4242", "987.5", "priority", "abc123", "67000", "88.5", "run a command"} {
		if strings.Contains(body, never) {
			t.Errorf("digest leaked telemetry %q:\n%s", never, body)
		}
	}
	if !strings.Contains(body, "done") {
		t.Error("digest dropped the assistant message")
	}
}

// response.create re-sends the entire conversation every turn, so replaying assistant-role
// prompts would repeat the whole transcript once per remaining turn - the digest would
// grow quadratically in the length of the session.
func TestBuild_DropsReplayedAssistantPrompts(t *testing.T) {
	turns := []*turn.Turn{{
		FirstTS: at(0),
		Prompts: []turn.Prompt{
			{Role: "user", Text: "KEEP-ME"},
			{Role: "assistant", Text: "DROP-ME"},
		},
	}}
	body := Build(turns, Options{}).Chunks[0]
	if !strings.Contains(body, "KEEP-ME") {
		t.Error("dropped the user prompt")
	}
	if strings.Contains(body, "DROP-ME") {
		t.Error("kept a replayed assistant prompt")
	}
}

func TestBuild_ErrorsSurvive(t *testing.T) {
	turns := []*turn.Turn{{
		FirstTS:      at(0),
		Status:       turn.StatusError,
		ErrorType:    "rate_limit",
		ErrorCode:    "429",
		ErrorMessage: "slow down",
	}}
	body := Build(turns, Options{}).Chunks[0]
	for _, want := range []string{"ERROR", "rate_limit", "429", "slow down"} {
		if !strings.Contains(body, want) {
			t.Errorf("error digest is missing %q:\n%s", want, body)
		}
	}
}

// A command's verdict is usually its last line, so head-only truncation reliably cuts
// exactly the part worth keeping.
func TestHeadTail_KeepsBothEnds(t *testing.T) {
	s := "VERDICT-HEAD" + strings.Repeat("x", 5000) + "VERDICT-TAIL"
	got := headTail(s, 100)

	if !strings.HasPrefix(got, "VERDICT-HEAD") {
		t.Errorf("lost the head: %q", got[:min(40, len(got))])
	}
	if !strings.HasSuffix(got, "VERDICT-TAIL") {
		t.Errorf("lost the tail: %q", got[max(0, len(got)-40):])
	}
	if !strings.Contains(got, "elided") {
		t.Error("truncation was not marked, so a model cannot tell it is reading a fragment")
	}
	if len(got) >= len(s) {
		t.Errorf("did not truncate: %d >= %d", len(got), len(s))
	}
}

func TestHeadTail_ShortInputIsUntouched(t *testing.T) {
	if got := headTail("small", 100); got != "small" {
		t.Errorf("headTail(short) = %q, want it unchanged", got)
	}
}

func TestClip_MarksWhatItRemoved(t *testing.T) {
	got := clip(strings.Repeat("y", 500), 100)
	if !strings.HasPrefix(got, strings.Repeat("y", 100)) {
		t.Error("clip did not keep the head")
	}
	if !strings.Contains(got, "400 chars elided") {
		t.Errorf("clip did not say how much it removed: %q", got)
	}
}

func TestBuild_ToolOutputIsTighterThanToolInput(t *testing.T) {
	big := strings.Repeat("z", 50_000)
	turns := []*turn.Turn{{
		FirstTS:     at(0),
		ToolCalls:   []turn.ToolCall{{Name: "apply_patch", Input: big}},
		ToolOutputs: []turn.ToolOutput{{Text: big}},
	}}
	body := Build(turns, Options{MaxCharsPerToolInput: 20_000, MaxCharsPerToolOutput: 2_000}).Chunks[0]

	// The arguments say what changed; the output is console noise. That asymmetry is the
	// whole point of the digest, so it is pinned rather than left to a comment.
	if n := strings.Count(body, "z"); n < 20_000 || n > 24_000 {
		t.Errorf("kept %d argument+output chars, want roughly 22000 (20k input + 2k output)", n)
	}
}

// Splitting mid-turn separates a tool call from its result, so the model reports a command
// that was never answered and an answer to nothing.
func TestBuild_ChunksOnlyAtTurnBoundaries(t *testing.T) {
	var turns []*turn.Turn
	for i := range 6 {
		turns = append(turns, &turn.Turn{
			FirstTS:  at(i),
			Messages: []turn.Message{{Text: strings.Repeat("m", 1000)}},
		})
	}

	d := Build(turns, Options{MaxCharsPerSession: 2500})
	if d.Passes() < 2 {
		t.Fatalf("Passes() = %d, want the digest to have chunked", d.Passes())
	}
	for i, c := range d.Chunks {
		if n := strings.Count(c, "--- turn "); n == 0 {
			t.Errorf("chunk %d contains no whole turn header", i)
		}
		// Every turn header must be followed by that turn's body in the same chunk.
		if strings.HasSuffix(strings.TrimSpace(c), "--- turn") {
			t.Errorf("chunk %d ends on a turn header, so a turn was split", i)
		}
	}

	// Nothing may be lost across the split.
	total := 0
	for _, c := range d.Chunks {
		total += strings.Count(c, "--- turn ")
	}
	if total != len(turns) {
		t.Errorf("chunks carry %d turns, want all %d", total, len(turns))
	}
}

// A single turn larger than the budget is emitted whole rather than split, because the
// alternative is the mid-turn split the design rules out. It gets its own chunk.
func TestBuild_OversizedTurnIsNotSplit(t *testing.T) {
	turns := []*turn.Turn{
		{FirstTS: at(0), Messages: []turn.Message{{Text: strings.Repeat("a", 100)}}},
		{FirstTS: at(1), Messages: []turn.Message{{Text: strings.Repeat("b", 9000)}}},
	}
	d := Build(turns, Options{MaxCharsPerSession: 1000, MaxCharsPerMessage: 100_000})
	if d.Passes() != 2 {
		t.Fatalf("Passes() = %d, want 2", d.Passes())
	}
	if strings.Count(d.Chunks[1], "b") < 9000 {
		t.Error("the oversized turn was truncated or split rather than kept whole")
	}
}

func TestBuild_EmptyTurnsProduceNoChunks(t *testing.T) {
	d := Build([]*turn.Turn{{FirstTS: at(0)}}, Options{})
	if d.Passes() != 0 || d.Chars != 0 {
		t.Errorf("Passes()=%d Chars=%d, want an empty digest for a contentless turn", d.Passes(), d.Chars)
	}
}
