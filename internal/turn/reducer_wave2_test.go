package turn

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"testing"
	"time"

	"github.com/rknightion/codexlb2otel/internal/frame"
)

func TestReducerWave2_FunctionArguments(t *testing.T) {
	t.Run("ordinary function call", func(t *testing.T) {
		got := reduceWave2(t, New(), "thread-ordinary", `{"type":"function_call","id":"item-call","call_id":"call-ordinary","name":"lookup","status":"completed","arguments":"{\"count\":2,\"label\":\"plain\"}"}`)
		if len(got.ToolCalls) != 1 {
			t.Fatalf("tool calls = %d, want 1", len(got.ToolCalls))
		}
		call := got.ToolCalls[0]
		if call.InputChars != len(`{"count":2,"label":"plain"}`) || call.InputOmitted != 0 || call.InputTruncated {
			t.Fatalf("argument metadata = %+v", call)
		}
		if !json.Valid([]byte(call.Input)) {
			t.Fatalf("input is not valid JSON: %q", call.Input)
		}
		if call.Ordinal == 0 || call.ItemID != "item-call" || call.CapturedAt.IsZero() || call.Provenance != "live" {
			t.Fatalf("content metadata = %+v", call)
		}
	})

	t.Run("mixed and nested encrypted values", func(t *testing.T) {
		args := fmt.Sprintf(`{"plain":"ok","encrypted_token":"opaque","nested":{"message":%q,"encrypted_blob":"opaque-too"}}`, fernetTokenWave2())
		got := reduceWave2(t, New(), "thread-encrypted", functionEvent("item-encrypted", "call-encrypted", "lookup", args))
		call := got.ToolCalls[0]
		if call.InputOmitted != 3 || !json.Valid([]byte(call.Input)) {
			t.Fatalf("redaction metadata/input = %+v", call)
		}
		var value map[string]any
		if err := json.Unmarshal([]byte(call.Input), &value); err != nil {
			t.Fatal(err)
		}
		if value["encrypted_token"] != "[omitted: encrypted]" || value["nested"].(map[string]any)["encrypted_blob"] != "[omitted: encrypted]" || value["nested"].(map[string]any)["message"] != "[omitted: encrypted]" {
			t.Fatalf("encrypted values were not replaced: %s", call.Input)
		}
	})

	t.Run("collaboration call retains spawn fields", func(t *testing.T) {
		args := fmt.Sprintf(`{"task_name":"child","model":"model-x","reasoning_effort":"high","message":%q,"nested":{"encrypted":"opaque-too"}}`, fernetTokenWave2())
		got := reduceWave2(t, New(), "thread-collaboration", functionEvent("item-spawn", "call-spawn", "spawn_agent", args))
		call := got.ToolCalls[0]
		if call.TaskName != "child" || call.SubModel != "model-x" || call.SubEffort != "high" || call.InputOmitted != 2 {
			t.Fatalf("spawn/capture fields = %+v", call)
		}
	})

	t.Run("nested arrays redact opaque scalar values", func(t *testing.T) {
		args := fmt.Sprintf(`{"nested":[{"message":%q},[%q]],"encrypted_values":["opaque"]}`, fernetTokenWave2(), fernetTokenWave2())
		got := reduceWave2(t, New(), "thread-array", functionEvent("item-array", "call-array", "lookup", args))
		call := got.ToolCalls[0]
		if call.InputOmitted != 3 || !json.Valid([]byte(call.Input)) {
			t.Fatalf("array redaction metadata/input = %+v", call)
		}
		var value map[string]any
		if err := json.Unmarshal([]byte(call.Input), &value); err != nil {
			t.Fatal(err)
		}
		nested := value["nested"].([]any)
		if nested[0].(map[string]any)["message"] != "[omitted: encrypted]" || nested[1].([]any)[0] != "[omitted: encrypted]" || value["encrypted_values"].([]any)[0] != "[omitted: encrypted]" {
			t.Fatalf("opaque array values were not replaced: %s", call.Input)
		}
	})

	t.Run("custom tool remains unchanged", func(t *testing.T) {
		input := "custom input that exceeds the configured capture limit"
		got := reduceWave2(t, NewWithOptions(Options{MaxToolOutputChars: 8}), "thread-custom", fmt.Sprintf(`{"type":"custom_tool_call","id":"item-custom","call_id":"call-custom","name":"custom","status":"completed","input":%q}`, input))
		call := got.ToolCalls[0]
		if call.Input != input || call.InputTruncated || call.InputOmitted != 0 {
			t.Fatalf("custom call changed: %+v", call)
		}
	})

	t.Run("function input respects existing tool content bound", func(t *testing.T) {
		args := `{"field":"this input is deliberately longer than the bound"}`
		got := reduceWave2(t, NewWithOptions(Options{MaxToolOutputChars: 24}), "thread-bound", functionEvent("item-bound", "call-bound", "lookup", args))
		call := got.ToolCalls[0]
		if !call.InputTruncated || len(call.Input) > 24 || !json.Valid([]byte(call.Input)) {
			t.Fatalf("bounded input = %+v", call)
		}
		if call.InputChars != len(args) {
			t.Fatalf("input chars = %d, want %d", call.InputChars, len(args))
		}
	})
}

func TestReducerWave2_ReusedCallIDRemainsAmbiguous(t *testing.T) {
	r := New()
	base := time.Date(2026, 9, 5, 14, 0, 0, 0, time.UTC)
	for _, response := range []struct{ request, response string }{{"req-origin-a", "response-a"}, {"req-origin-b", "response-b"}} {
		addWave2Event(t, r, response.request, base, `{"type":"response.create","client_metadata":{"thread_id":"thread-ambiguous"}}`)
		addWave2Event(t, r, response.request, base.Add(time.Millisecond), fmt.Sprintf(`{"type":"response.created","response":{"id":%q}}`, response.response))
		addWave2Event(t, r, response.request, base.Add(2*time.Millisecond), `{"type":"response.output_item.done","item":`+functionEvent("item-"+response.response, "shared-call", "lookup", `{"key":"value"}`)+`}`)
		addWave2Event(t, r, response.request, base.Add(3*time.Millisecond), `{"type":"response.completed","response":{"status":"completed"}}`)
	}
	addWave2Event(t, r, "req-result", base.Add(4*time.Millisecond), `{"type":"response.create","client_metadata":{"thread_id":"thread-ambiguous"},"input":[{"type":"function_call_output","call_id":"shared-call","output":"result"}]}`)
	done := addWave2Event(t, r, "req-result", base.Add(5*time.Millisecond), `{"type":"response.completed","response":{"status":"completed"}}`)
	if done == nil || len(done.ToolOutputs) != 1 || done.ToolOutputs[0].OriginMatch != "ambiguous" {
		t.Fatalf("reused call result = %+v", done)
	}
}

func TestReducerWave3ToolCallOccurrencesLinkExactOutput(t *testing.T) {
	r := New()
	base := time.Date(2026, 9, 5, 14, 0, 0, 0, time.UTC)
	addWave2Event(t, r, "req-origin", base, `{"type":"response.create","client_metadata":{"thread_id":"thread-occurrence"}}`)
	addWave2Event(t, r, "req-origin", base.Add(time.Millisecond), `{"type":"response.created","response":{"id":"response-origin"}}`)
	origin := addWave2Event(t, r, "req-origin", base.Add(2*time.Millisecond), `{"type":"response.output_item.done","item":`+functionEvent("item-origin", "call-reused", "lookup", `{}`)+`}`)
	if origin != nil {
		t.Fatal("output item unexpectedly completed the origin response")
	}
	origin = addWave2Event(t, r, "req-origin", base.Add(3*time.Millisecond), `{"type":"response.completed","response":{"status":"completed"}}`)
	if origin == nil || origin.ToolCalls[0].CallOccurrence != 1 {
		t.Fatalf("origin occurrence = %+v, want 1", origin)
	}

	addWave2Event(t, r, "req-result", base.Add(4*time.Millisecond), `{"type":"response.create","client_metadata":{"thread_id":"thread-occurrence"},"input":[{"type":"function_call_output","call_id":"call-reused","output":"result"}]}`)
	result := addWave2Event(t, r, "req-result", base.Add(5*time.Millisecond), `{"type":"response.completed","response":{"status":"completed"}}`)
	if result == nil || len(result.ToolOutputs) != 1 || result.ToolOutputs[0].OriginMatch != "exact" || result.ToolOutputs[0].OriginCallOccurrence != 1 {
		t.Fatalf("result origin = %+v, want exact occurrence 1", result)
	}

	second := reduceWave2(t, r, "thread-occurrence", functionEvent("item-second", "call-reused", "lookup", `{}`))
	if second.ToolCalls[0].CallOccurrence != 2 {
		t.Fatalf("reused call occurrence = %d, want 2", second.ToolCalls[0].CallOccurrence)
	}
}

func TestReducerWave2_ErrorEventsTolerateSequenceNumber(t *testing.T) {
	r := New()
	done := addWave2Event(t, r, "req-error", time.Date(2026, 9, 5, 15, 0, 0, 0, time.UTC), `{"type":"error","sequence_number":7,"error":{"type":"service_unavailable_error","code":"server_is_overloaded","message":"synthetic"}}`)
	if done == nil {
		t.Fatal("error event did not close the turn")
	}
	if done.Status != StatusError || done.ErrorType != "service_unavailable_error" || done.ErrorCode != "server_is_overloaded" || done.ErrorMessage != "synthetic" {
		t.Fatalf("error turn = %+v", done)
	}
}

func TestReducerWave2_ContentOrderingAndReplayProvenance(t *testing.T) {
	r := New()
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	create := `{"type":"response.create","instructions":"instruction text","client_metadata":{"thread_id":"thread-order"},"input":[` +
		`{"type":"message","id":"item-message-a","role":"user","content":"first input"},` +
		`{"type":"function_call_output","id":"item-output-a","call_id":"call-output-a","output":"first result"},` +
		`{"type":"message","id":"item-message-b","role":"user","content":"second input"},` +
		`{"type":"agent_message","id":"item-agent","author":"author","recipient":"recipient","content":"agent note"}]}`
	addWave2Event(t, r, "req-order", base, create)
	addWave2Event(t, r, "req-order", base.Add(250*time.Millisecond), `{"type":"response.created","response":{"id":"response-replayed"}}`)
	addWave2Event(t, r, "req-order", base.Add(500*time.Millisecond), `{"type":"response.create","input":[{"type":"function_call_output","id":"item-output-b","call_id":"call-output-b","output":"second result"}]}`)
	addWave2Event(t, r, "req-order", base.Add(time.Second), `{"type":"response.output_item.done","item":`+functionEvent("item-call", "call-live", "lookup", `{"key":"value"}`)+`}`)
	addWave2Event(t, r, "req-order", base.Add(1500*time.Millisecond), `{"type":"response.output_item.done","item":{"type":"message","id":"item-message-output","phase":"final","content":[{"type":"output_text","text":"assistant output"}]}}`)
	done := addWave2Event(t, r, "req-order", base.Add(2*time.Second), `{"type":"response.completed","response":{"status":"completed"}}`)
	if done == nil {
		t.Fatal("response did not complete")
	}

	if len(done.Prompts) != 3 || len(done.Messages) != 1 || len(done.ToolOutputs) != 2 || len(done.ToolCalls) != 1 || len(done.AgentMessages) != 1 {
		t.Fatalf("content counts prompts=%d messages=%d outputs=%d calls=%d agent_messages=%d", len(done.Prompts), len(done.Messages), len(done.ToolOutputs), len(done.ToolCalls), len(done.AgentMessages))
	}
	if done.Prompts[0].Provenance != "unknown" || done.Prompts[1].Provenance != "replayed" || done.ToolOutputs[0].Provenance != "replayed" || done.ToolCalls[0].Provenance != "live" || done.Messages[0].Provenance != "live" || done.AgentMessages[0].Provenance != "replayed" {
		t.Fatalf("unexpected provenance prompts=%+v output=%+v call=%+v message=%+v agent=%+v", done.Prompts, done.ToolOutputs[0], done.ToolCalls[0], done.Messages[0], done.AgentMessages[0])
	}
	if !done.ToolOutputs[0].CapturedAt.Equal(base) || !done.ToolOutputs[1].CapturedAt.Equal(base.Add(500*time.Millisecond)) || !done.ToolCalls[0].CapturedAt.Equal(base.Add(time.Second)) {
		t.Fatalf("capture times are not frame times: outputs=%+v call=%+v", done.ToolOutputs, done.ToolCalls[0])
	}
	assertMonotonicWave2(t, done)

	// The same response.create history arrives again on a later request. It remains
	// deduplicated, while the original input records retain replayed provenance.
	addWave2Event(t, r, "req-replay", base.Add(3*time.Second), create)
	addWave2Event(t, r, "req-replay", base.Add(3500*time.Millisecond), `{"type":"response.created","response":{"id":"response-replayed"}}`)
	addWave2Event(t, r, "req-replay", base.Add(4*time.Second), `{"type":"response.output_item.done","item":`+functionEvent("item-call-replay", "call-live", "lookup", `{"key":"value"}`)+`}`)
	replayed := addWave2Event(t, r, "req-replay", base.Add(5*time.Second), `{"type":"response.completed","response":{"status":"completed"}}`)
	if replayed == nil || len(replayed.Prompts) != 0 || len(replayed.ToolOutputs) != 0 || len(replayed.ToolCalls) != 0 || len(replayed.AgentMessages) != 0 {
		t.Fatalf("replayed history was reinserted: %+v", replayed)
	}
}

func reduceWave2(t *testing.T, r *Reducer, thread, item string) *Turn {
	t.Helper()
	base := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)
	addWave2Event(t, r, "req-"+thread, base, fmt.Sprintf(`{"type":"response.create","client_metadata":{"thread_id":%q}}`, thread))
	addWave2Event(t, r, "req-"+thread, base.Add(time.Second), `{"type":"response.output_item.done","item":`+item+`}`)
	done := addWave2Event(t, r, "req-"+thread, base.Add(2*time.Second), `{"type":"response.completed","response":{"status":"completed"}}`)
	if done == nil {
		t.Fatal("response did not complete")
	}
	return done
}

func functionEvent(itemID, callID, name, arguments string) string {
	return fmt.Sprintf(`{"type":"function_call","id":%q,"call_id":%q,"name":%q,"status":"completed","arguments":%q}`, itemID, callID, name, arguments)
}

func fernetTokenWave2() string {
	return base64.RawURLEncoding.EncodeToString(append([]byte{0x80}, make([]byte, 72)...))
}

func addWave2Event(t *testing.T, r *Reducer, request string, at time.Time, event string) *Turn {
	t.Helper()
	done, err := r.Add(&frame.Record{RequestID: request, Headers: frame.Headers{"thread-id": "fallback-thread", "originator": "codex-tui"}, Timestamp: at, Payload: frame.Payload{Text: event}})
	if err != nil {
		t.Fatal(err)
	}
	return done
}

func assertMonotonicWave2(t *testing.T, turn *Turn) {
	t.Helper()
	got := map[string]int{}
	for _, item := range turn.Prompts {
		got[item.ItemID] = item.Ordinal
	}
	for _, item := range turn.ToolOutputs {
		got[item.ItemID] = item.Ordinal
	}
	for _, item := range turn.Messages {
		got[item.ItemID] = item.Ordinal
	}
	for _, item := range turn.ToolCalls {
		got[item.ItemID] = item.Ordinal
	}
	for _, item := range turn.AgentMessages {
		got[item.ItemID] = item.Ordinal
	}
	want := map[string]int{
		"":               1, // Instructions have no wire item ID.
		"item-message-a": 2, "item-output-a": 3, "item-message-b": 4,
		"item-agent": 5, "item-output-b": 6, "item-call": 7, "item-message-output": 8,
	}
	if !maps.Equal(got, want) {
		t.Fatalf("item ordinals = %#v, want %#v", got, want)
	}
}
