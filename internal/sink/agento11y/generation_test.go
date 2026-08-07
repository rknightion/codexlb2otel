package agento11y

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	agento11yv1 "github.com/grafana/agento11y/go/proto/agento11y/v1"
	"github.com/grafana/agento11y/go/proto/agento11y/wire"

	"github.com/rknightion/codexlb2otel/internal/attr"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

// TestBuildGeneration_ConformsToProtojsonWireFormat is the important test in this
// package: it proves the hand-written JSON this sink emits is not merely "shaped
// like" sigil's wire contract but is BYTE-FOR-BYTE decodable by the real protojson
// unmarshaller the SDK and sigil's server use. Everything else in this file checks
// mapping decisions; this is the one guarding against the four encoding traps a
// hand-rolled struct gets wrong by default (see wire.go's package doc comment).
func TestBuildGeneration_ConformsToProtojsonWireFormat(t *testing.T) {
	started := time.Date(2026, 8, 7, 13, 32, 33, 0, time.UTC)
	completed := time.Date(2026, 8, 7, 13, 32, 39, 0, time.UTC)

	tr := &turn.Turn{
		RequestID:         "req-1",
		ResponseID:        "resp_x",
		ThreadID:          "thread-1",
		Model:             "gpt-5.6-sol",
		Status:            "completed",
		TraceID:           "0af7651916cd43dd8448eb211c80319c",
		SpanID:            "b7ad6b7169203331",
		TextDeltaCount:    5, // >0, so mode must come out STREAM
		ServerCreatedAt:   started,
		ServerCompletedAt: completed,
		InputTokens:       10,
		OutputTokens:      2,
		TotalTokens:       12,
		CachedTokens:      3,
		Prompts: []turn.Prompt{
			{Role: "instructions", Text: "be a helpful assistant"},
			{Role: "user", Text: "hi", Images: 1, ImageMIME: "image/png"},
		},
		Messages: []turn.Message{
			{Text: "hello there"},
		},
		ToolCalls: []turn.ToolCall{
			{Kind: "function", Name: "exec", CallID: "call_1", Input: `{"cmd":"ls"}`},
		},
		ToolOutputs: []turn.ToolOutput{
			{CallID: "call_1", Text: "file1\nfile2"},
		},
	}

	guard := attr.NewGuard()
	g := buildGeneration(tr, guard)

	data, err := json.Marshal(wireExportGenerationsRequest{Generations: []wireGeneration{g}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	encoded := string(data)

	// Trap #1: int64 fields are JSON strings, not numbers. If TotalTokens regressed to
	// a bare json.Marshal of an int, this substring check would fail while the decode
	// below might still silently succeed by coercion in some JSON libraries - checking
	// the raw bytes is what actually pins the wire shape.
	if !strings.Contains(encoded, `"total_tokens":"12"`) {
		t.Errorf("expected total_tokens as a JSON string \"12\", got: %s", encoded)
	}
	if strings.Contains(encoded, `"total_tokens":12,`) || strings.Contains(encoded, `"total_tokens":12}`) {
		t.Errorf("total_tokens leaked as a bare JSON number, not a string: %s", encoded)
	}
	// Trap #2: full symbolic enum names.
	if !strings.Contains(encoded, `"mode":"GENERATION_MODE_STREAM"`) {
		t.Errorf("expected mode as GENERATION_MODE_STREAM, got: %s", encoded)
	}

	req, err := wire.UnmarshalExportGenerationsJSON(data)
	if err != nil {
		t.Fatalf("wire.UnmarshalExportGenerationsJSON rejected this sink's own output: %v", err)
	}
	if len(req.Generations) != 1 {
		t.Fatalf("expected 1 decoded generation, got %d", len(req.Generations))
	}
	got := req.Generations[0]

	if got.GetId() != "resp_x" {
		t.Errorf("id = %q, want resp_x", got.GetId())
	}
	if got.GetConversationId() != "thread-1" {
		t.Errorf("conversation_id = %q, want thread-1", got.GetConversationId())
	}
	if got.GetOperationName() != "chat" {
		t.Errorf("operation_name = %q, want chat", got.GetOperationName())
	}
	if got.GetMode() != agento11yv1.GenerationMode_GENERATION_MODE_STREAM {
		t.Errorf("mode = %v, want GENERATION_MODE_STREAM", got.GetMode())
	}
	if got.GetTraceId() != tr.TraceID || got.GetSpanId() != tr.SpanID {
		t.Errorf("trace/span id = %q/%q, want %q/%q", got.GetTraceId(), got.GetSpanId(), tr.TraceID, tr.SpanID)
	}
	if got.GetModel().GetProvider() != "openai" || got.GetModel().GetName() != "gpt-5.6-sol" {
		t.Errorf("model = %+v, want provider=openai name=gpt-5.6-sol", got.GetModel())
	}
	if got.GetResponseModel() != "gpt-5.6-sol" {
		t.Errorf("response_model = %q, want gpt-5.6-sol", got.GetResponseModel())
	}
	if got.GetSystemPrompt() != "be a helpful assistant" {
		t.Errorf("system_prompt = %q, want the instructions text", got.GetSystemPrompt())
	}
	// stop_reason must stay EMPTY even though Turn.Status is populated. It asks why the
	// model stopped generating; status answers whether this pipeline saw the response
	// finish, which is a different question - and the same reasoning that keeps
	// gen_ai.response.finish_reasons unset on issue #18. Pinned so the two cannot drift
	// into disagreeing.
	if got.GetStopReason() != "" {
		t.Errorf("stop_reason = %q, want empty: codexlb status is a different axis from "+
			"a model finish reason, and is already carried in tags", got.GetStopReason())
	}
	if !got.GetStartedAt().AsTime().Equal(started) {
		t.Errorf("started_at = %v, want %v", got.GetStartedAt().AsTime(), started)
	}
	if !got.GetCompletedAt().AsTime().Equal(completed) {
		t.Errorf("completed_at = %v, want %v", got.GetCompletedAt().AsTime(), completed)
	}

	// effective_version and parent_generation_ids are deliberate omissions - see
	// generation.go's doc comment on buildGeneration for why. Pinned here so a future
	// change that starts emitting either does so as a conscious decision, not a
	// silent side effect of some other edit.
	if got.GetEffectiveVersion() != "" {
		t.Errorf("effective_version = %q, want empty (InstructionsHash cannot satisfy the sha256: regex)", got.GetEffectiveVersion())
	}
	if len(got.GetParentGenerationIds()) != 0 {
		t.Errorf("parent_generation_ids = %v, want empty (no id-space join available)", got.GetParentGenerationIds())
	}

	usage := got.GetUsage()
	if usage == nil {
		t.Fatal("usage is nil")
	}
	if usage.GetInputTokens() != 10 || usage.GetOutputTokens() != 2 || usage.GetTotalTokens() != 12 ||
		usage.GetCacheReadInputTokens() != 3 {
		t.Errorf("usage = %+v, want input=10 output=2 total=12 cache_read=3", usage)
	}
	if usage.GetInputSemantics() != agento11yv1.TokenInputSemantics_TOKEN_INPUT_SEMANTICS_INCLUSIVE {
		t.Errorf("input_semantics = %v, want INCLUSIVE", usage.GetInputSemantics())
	}

	// input: the "hi" prompt (role USER, a text part and an image media part), NOT the
	// instructions prompt (that became system_prompt above, not an input message), and
	// the tool output (role TOOL, a tool_result part).
	if len(got.GetInput()) != 2 {
		t.Fatalf("input = %d messages, want 2 (user prompt + tool result), got %+v", len(got.GetInput()), got.GetInput())
	}
	userMsg := got.GetInput()[0]
	if userMsg.GetRole() != agento11yv1.MessageRole_MESSAGE_ROLE_USER {
		t.Errorf("input[0].role = %v, want USER", userMsg.GetRole())
	}
	if len(userMsg.GetParts()) != 2 {
		t.Fatalf("input[0].parts = %d, want 2 (text + image media)", len(userMsg.GetParts()))
	}
	if userMsg.GetParts()[0].GetText() != "hi" {
		t.Errorf("input[0].parts[0].text = %q, want hi", userMsg.GetParts()[0].GetText())
	}
	// The media part carries the declared type but NO url: the only url these images
	// ever had was the base64 payload itself, inline, and that must never be rebuilt.
	if media := userMsg.GetParts()[1].GetMedia(); media == nil ||
		media.GetKind() != "image" || media.GetMimeType() != "image/png" || media.GetUrl() != "" {
		t.Errorf("input[0].parts[1].media = %+v, want kind=image mime=image/png and no url", media)
	}

	toolResultMsg := got.GetInput()[1]
	if toolResultMsg.GetRole() != agento11yv1.MessageRole_MESSAGE_ROLE_TOOL {
		t.Errorf("input[1].role = %v, want TOOL", toolResultMsg.GetRole())
	}
	if len(toolResultMsg.GetParts()) != 1 || toolResultMsg.GetParts()[0].GetToolResult() == nil {
		t.Fatalf("input[1].parts = %+v, want one tool_result part", toolResultMsg.GetParts())
	}
	tres := toolResultMsg.GetParts()[0].GetToolResult()
	if tres.GetToolCallId() != "call_1" || tres.GetContent() != "file1\nfile2" {
		t.Errorf("tool_result = %+v, want call_1 / \"file1\\nfile2\"", tres)
	}

	// output: the assistant text message, then the tool call.
	if len(got.GetOutput()) != 2 {
		t.Fatalf("output = %d messages, want 2 (assistant text + tool call), got %+v", len(got.GetOutput()), got.GetOutput())
	}
	if got.GetOutput()[0].GetRole() != agento11yv1.MessageRole_MESSAGE_ROLE_ASSISTANT ||
		got.GetOutput()[0].GetParts()[0].GetText() != "hello there" {
		t.Errorf("output[0] = %+v, want assistant text \"hello there\"", got.GetOutput()[0])
	}
	tc := got.GetOutput()[1].GetParts()[0].GetToolCall()
	if tc == nil || tc.GetId() != "call_1" || tc.GetName() != "exec" || string(tc.GetInputJson()) != `{"cmd":"ls"}` {
		t.Errorf("tool_call = %+v, want id=call_1 name=exec input_json={\"cmd\":\"ls\"}", tc)
	}

	// tags come from attr.Guard.SpanAttrs, the same cardinality-guarded set the
	// otlptrace sink uses - not a bespoke attribute set assembled in this package.
	tags := got.GetTags()
	if tags[attr.Status] != "completed" {
		t.Errorf("tags[%s] = %q, want completed", attr.Status, tags[attr.Status])
	}
	if tags[attr.GenAIConversationID] != "thread-1" {
		t.Errorf("tags[%s] = %q, want thread-1", attr.GenAIConversationID, tags[attr.GenAIConversationID])
	}
}

// TestMapRole_UnknownRoleOmitsRatherThanMisattributes pins the deliberate choice in
// mapRole: a role this proto has no slot for (sigil's MessageRole is only
// UNSPECIFIED/USER/ASSISTANT/TOOL) must come out unset, never coerced to USER.
func TestMapRole_UnknownRoleOmitsRatherThanMisattributes(t *testing.T) {
	if got := mapRole("developer"); got != "" {
		t.Errorf("mapRole(developer) = %q, want empty - not USER", got)
	}
}
