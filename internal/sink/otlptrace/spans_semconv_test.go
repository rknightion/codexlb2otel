package otlptrace

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/rknightion/codexlb2otel/internal/attr"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

// gen_ai.operation.name is the discriminator a GenAI trace consumer keys on, and
// gen_ai.provider.name is required alongside it on an INFERENCE span specifically.
// Neither is Turn-derived, so attr.Guard leaves both out of SpanAttrs - which meant no
// span carried either and every span was invisible to anything reading the
// convention.
//
// EXACTLY ONE span per response may claim to be the "chat" inference. A consumer that
// turns each qualifying span into a record counts the turn once per claim, so putting
// these on the root as well would double-count every turn.
//
// issue #18 added gen_ai.operation.name to tool_call spans too (execute_tool /
// invoke_agent) - a DIFFERENT operation value on a DIFFERENT span, describing a
// different event (one tool call), not a second claim on the same inference. The
// spec's own execute_tool.internal/invoke_agent.internal attribute groups do not
// reference gen_ai.provider.name at all (checked against spans.yaml, 2026-08-07), so
// this test's old blanket "op and provider always travel together" assumption no
// longer holds across the whole tree - it holds for the chat span specifically, which
// is what the two checks below now test separately.
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

	var chatClaimants, toolClaimants []string
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
		switch op {
		case "":
			if provider != "" {
				t.Errorf("span %q carries gen_ai.provider.name with no gen_ai.operation.name", sp.Name)
			}
		case attr.GenAIOperationValue:
			chatClaimants = append(chatClaimants, sp.Name)
			if provider == "" {
				t.Errorf("chat span %q has no gen_ai.provider.name; the convention requires "+
					"both together on an inference span", sp.Name)
			}
		case attr.GenAIOperationExecuteTool, attr.GenAIOperationInvokeAgent:
			toolClaimants = append(toolClaimants, sp.Name)
			if provider != "" {
				t.Errorf("tool span %q carries gen_ai.provider.name = %q; the spec's "+
					"execute_tool/invoke_agent attribute groups do not reference it", sp.Name, provider)
			}
		default:
			t.Errorf("span %q carries an unexpected gen_ai.operation.name %q", sp.Name, op)
		}
	}

	if len(chatClaimants) != 1 {
		t.Fatalf("%d spans claim to be the chat inference (%v); want exactly the response span, "+
			"or a consumer counts this turn once per claimant", len(chatClaimants), chatClaimants)
	}
	if chatClaimants[0] != "chat gpt-5.6-sol" {
		t.Errorf("the inference span is %q, want the chat span", chatClaimants[0])
	}
	if len(toolClaimants) != 1 {
		t.Fatalf("%d spans claim execute_tool/invoke_agent (%v); want exactly one, for the "+
			"single tool call this turn made", len(toolClaimants), toolClaimants)
	}
	if want := "execute_tool exec"; toolClaimants[0] != want {
		t.Errorf("the tool-call span is %q, want %q (a plain exec call, not a spawn_agent)",
			toolClaimants[0], want)
	}
}

// TestBoundArguments_CapsTheTailWithoutSplittingARune covers issue #22 item 5. The
// corpus says ToolCall.Input is median 318 bytes but reaches 33.4 KB, and unlike
// ToolOutput nothing upstream bounds it.
//
// The rune case is the one worth pinning: a span attribute is a UTF-8 string, and an
// exporter handed an invalid one may drop the attribute entirely - trading a large
// value for no value, which is strictly worse than the problem being fixed. Cutting at
// a fixed byte offset splits a multi-byte rune whenever one straddles it.
func TestBoundArguments_CapsTheTailWithoutSplittingARune(t *testing.T) {
	t.Run("a normal tool call is untouched", func(t *testing.T) {
		in := `{"cmd":"ls -la"}`
		if got := boundArguments(in); got != in {
			t.Errorf("boundArguments rewrote a %d-byte input: %q", len(in), got)
		}
	})

	t.Run("exactly at the limit is untouched", func(t *testing.T) {
		in := strings.Repeat("a", maxToolCallArgumentBytes)
		if got := boundArguments(in); got != in {
			t.Errorf("boundArguments truncated an input of exactly the limit (%d bytes)", len(in))
		}
	})

	t.Run("the corpus maximum is cut and says how much was lost", func(t *testing.T) {
		const orig = 33411 // the largest ToolCall.Input measured across the corpus
		got := boundArguments(strings.Repeat("a", orig))
		if len(got) >= orig {
			t.Errorf("boundArguments returned %d bytes for a %d-byte input", len(got), orig)
		}
		if !strings.Contains(got, "33411") {
			t.Errorf("truncation marker does not carry the original length; a reader "+
				"cannot tell how much was lost: %q", got[len(got)-80:])
		}
	})

	t.Run("a multi-byte rune straddling the cut is not split", func(t *testing.T) {
		// "€" is 3 bytes, so padding to one byte short of the limit puts the cut
		// squarely inside it.
		in := strings.Repeat("a", maxToolCallArgumentBytes-1) + "€" + strings.Repeat("b", 100)
		got := boundArguments(in)
		if !utf8.ValidString(got) {
			t.Fatal("boundArguments produced an invalid UTF-8 string; an exporter may " +
				"drop the attribute entirely rather than send a large one")
		}
	})
}
