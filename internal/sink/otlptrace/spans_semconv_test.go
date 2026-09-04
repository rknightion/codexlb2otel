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
		case attr.GenAIOperationGenerateText, attr.GenAIOperationStreamText:
			chatClaimants = append(chatClaimants, sp.Name)
			if provider == "" {
				t.Errorf("inference span %q has no gen_ai.provider.name; the convention requires "+
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
		t.Fatalf("%d spans claim to be the inference (%v); want exactly the response span, "+
			"or a consumer counts this turn once per claimant", len(chatClaimants), chatClaimants)
	}
	if chatClaimants[0] != "generateText gpt-5.6-sol" {
		t.Errorf("the inference span is %q, want the response span", chatClaimants[0])
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

func TestTurnSpanCarriesRequestedTierAndOmitsAbsentNormalTier(t *testing.T) {
	tests := []struct {
		name string
		tier string
		want bool
	}{
		{name: "priority", tier: "priority", want: true},
		{name: "normal", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, exp := newTestSink(t)
			now := time.Now()
			if err := s.Emit(context.Background(), []*turn.Turn{{
				RequestID: "ws_" + tc.name, ResponseID: "resp_" + tc.name,
				ThreadID: "thread_" + tc.name, TurnID: "turn_" + tc.name,
				Model: "gpt-5.6-sol", Status: "completed", Family: "websocket",
				RequestKind: "turn", ServiceTierRequested: tc.tier,
				FirstTS: now.Add(-time.Second), LastTS: now,
			}}); err != nil {
				t.Fatal(err)
			}
			if err := s.Flush(context.Background()); err != nil {
				t.Fatal(err)
			}

			for _, sp := range exp.GetSpans() {
				if sp.Name != "turn" {
					continue
				}
				var got string
				for _, a := range sp.Attributes {
					if string(a.Key) == attr.ServiceTierRequested {
						got = a.Value.AsString()
					}
				}
				if tc.want && got != tc.tier {
					t.Errorf("turn span requested tier = %q, want %q", got, tc.tier)
				}
				if !tc.want && got != "" {
					t.Errorf("normal turn span requested tier = %q, want absent", got)
				}
				return
			}
			t.Fatal("turn span not emitted")
		})
	}
}

func TestPostgresEnrichmentIsTypedAndResponseScoped(t *testing.T) {
	s, exp := newTestSink(t)
	now := time.Now()
	cost := 1.25
	tn := &turn.Turn{
		RequestID: "ws_1", ResponseID: "resp_1", ThreadID: "thread-1", TurnID: "turn-1",
		Model: "gpt-5.6-sol", Status: "completed", Family: "websocket", RequestKind: "turn",
		FirstTS: now.Add(-time.Second), LastTS: now, ServerCreatedAt: now.Add(-time.Second),
		ServerCompletedAt: now, CostUSD: &cost, APIKeyID: "key-1", APIKeyName: "primary",
		ProxyStatus: "success", ProxyResponseCreatedMS: 1250, ProxyFirstUpstreamEventMS: 2500,
	}
	if err := s.Emit(context.Background(), []*turn.Turn{tn}); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}

	found := map[string]bool{}
	for _, sp := range exp.GetSpans() {
		for _, a := range sp.Attributes {
			switch string(a.Key) {
			case attr.CostUSD:
				found[attr.CostUSD] = true
				if sp.Name != "generateText gpt-5.6-sol" || a.Value.AsFloat64() != cost {
					t.Errorf("span %q cost = %v, want typed response-only value %v", sp.Name, a.Value, cost)
				}
			case attr.ProxyTimeToResponseCreated:
				found[attr.ProxyTimeToResponseCreated] = true
				if sp.Name != "generateText gpt-5.6-sol" || a.Value.AsFloat64() != 1.25 {
					t.Errorf("span %q response-created time = %v, want typed response-only 1.25", sp.Name, a.Value)
				}
			case attr.ProxyTimeToFirstUpstreamEvent:
				found[attr.ProxyTimeToFirstUpstreamEvent] = true
				if sp.Name != "generateText gpt-5.6-sol" || a.Value.AsFloat64() != 2.5 {
					t.Errorf("span %q first-upstream time = %v, want typed response-only 2.5", sp.Name, a.Value)
				}
			case attr.APIKeyID, attr.APIKeyName, attr.ProxyStatus:
				found[string(a.Key)] = true
				if sp.Name != "generateText gpt-5.6-sol" {
					t.Errorf("span %q carries response-only enrichment %s", sp.Name, a.Key)
				}
			}
		}
	}
	for _, key := range []string{
		attr.CostUSD, attr.ProxyTimeToResponseCreated, attr.ProxyTimeToFirstUpstreamEvent,
		attr.APIKeyID, attr.APIKeyName, attr.ProxyStatus,
	} {
		if !found[key] {
			t.Errorf("response span did not carry %s", key)
		}
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

// TestAgentO11yContract_OnTheResponseSpanOnly pins issue #32's span side.
//
// Every attribute below is one Grafana agent observability reads and this service did
// not emit, and each failed SILENTLY - an empty search result, a conversation with no
// trace attached, an "anonymous" row - with nothing in any log to say why. There is no
// other guard on them: they are not registry fields (the two agento11y.* ones are
// stamped directly on the response span) or their value is derived (the agent name),
// so a refactor could drop any of them and every other test would still pass.
//
// The scoping half matters as much as the presence half. agento11y.sdk.name marks a
// span as agent-observability's own, and agento11y.generation.id claims to BE a
// particular generation - putting either on the turn root or a phase child would make
// several spans claim one generation, which is the same double-count the inference
// claim above is scoped to avoid.
func TestAgentO11yContract_OnTheResponseSpanOnly(t *testing.T) {
	s, exp := newTestSink(t)

	now := time.Now()
	if err := s.Emit(context.Background(), []*turn.Turn{{
		RequestID: "ws_1", ResponseID: "resp_1", ThreadID: "019fdb99-a66a", TurnID: "t1",
		Model: "gpt-5.6-sol", Status: "completed", Family: "websocket", RequestKind: "turn",
		Originator: "codex-tui", InstructionsHash: "3dcc72f5c56809d0",
		AccountID: "acct-1", FirstTS: now.Add(-time.Minute), LastTS: now,
		ServerCreatedAt: now.Add(-time.Minute), ServerCompletedAt: now,
		CriticalPath: turn.CriticalPath{EngineWallMs: 10, Coverage: "complete"},
		ToolCalls:    []turn.ToolCall{{Kind: "custom", Name: "exec", CallID: "c1"}},
	}}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	const respName = "generateText gpt-5.6-sol"

	// Present on the response span, with the exact values the far end matches on.
	want := map[string]string{
		attr.AgentO11ySDKName:      attr.AgentO11ySDKNameValue,
		attr.AgentO11yGenerationID: "resp_1", // MUST equal the agento11y sink's Generation.id
		attr.GenAIAgentName:        "codex",
		attr.GenAIAgentVersion:     "3dcc72f5c56809d0",
		attr.GenAIOperation:        attr.GenAIOperationGenerateText,
	}
	var found bool
	for _, sp := range exp.GetSpans() {
		if sp.Name != respName {
			continue
		}
		found = true
		got := map[string]string{}
		for _, a := range sp.Attributes {
			got[string(a.Key)] = a.Value.AsString()
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("response span %s = %q, want %q", k, got[k], v)
			}
		}
	}
	if !found {
		t.Fatalf("no span named %q was exported", respName)
	}

	// Scoped to that span alone.
	for _, sp := range exp.GetSpans() {
		if sp.Name == respName {
			continue
		}
		for _, a := range sp.Attributes {
			switch string(a.Key) {
			case attr.AgentO11ySDKName, attr.AgentO11yGenerationID:
				t.Errorf("span %q carries %s; it belongs to the response span alone, or "+
					"several spans claim one generation", sp.Name, a.Key)
			}
		}
	}
}

// TestOperationName_FollowsTheStreamSignal covers the other half of the operation
// value: a response that delivered text deltas is streamText, and it must agree with
// the mode the agento11y sink derives from the same signal, because sigil's ingest
// defaults one from the other.
func TestOperationName_FollowsTheStreamSignal(t *testing.T) {
	s, exp := newTestSink(t)

	now := time.Now()
	if err := s.Emit(context.Background(), []*turn.Turn{{
		RequestID: "ws_2", ResponseID: "resp_2", ThreadID: "019fdb99-b77b", TurnID: "t2",
		Model: "gpt-5.6-sol", Status: "completed", Family: "websocket", RequestKind: "turn",
		TextDeltaCount: 4,
		FirstTS:        now.Add(-time.Minute), LastTS: now,
		ServerCreatedAt: now.Add(-time.Minute), ServerCompletedAt: now,
	}}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	var names []string
	for _, sp := range exp.GetSpans() {
		names = append(names, sp.Name)
		if sp.Name != "streamText gpt-5.6-sol" {
			continue
		}
		for _, a := range sp.Attributes {
			if string(a.Key) == attr.GenAIOperation && a.Value.AsString() != attr.GenAIOperationStreamText {
				t.Errorf("span name says streamText but gen_ai.operation.name is %q",
					a.Value.AsString())
			}
		}
		return
	}
	t.Fatalf("no streamText span among %v; a response with text deltas must not be generateText", names)
}
