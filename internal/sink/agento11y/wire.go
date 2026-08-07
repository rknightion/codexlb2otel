// Package agento11y pushes reduced turns to Grafana's agent-observability (sigil)
// ExportGenerations endpoint - a wire contract distinct from OTLP, additive to the
// existing otlptrace sink rather than a replacement for it. See config.AgentO11y's
// doc comment for why sigil needs its own emitter at all.
//
// The wire types below are hand-written against
// /Users/<user>/repos/sigil-sdk/proto/agento11y/v1/generation_ingest.proto rather than
// generated from it or built via the protobuf-go library: this service has no other
// protobuf dependency anywhere in its production path (internal/sink/otlptrace talks
// OTLP, which is itself protojson/protobuf under the otel SDK, but nothing here hand-
// assembles a proto message), and pulling in protobuf-go and its code generator for
// one sink would be a heavier dependency than the four encoding rules below are worth
// reimplementing. github.com/grafana/agento11y/go is in go.mod for exactly one
// purpose: generation_test.go decodes this package's JSON output through the real
// protojson unmarshaller, so "the shape is right" is proven against the actual wire
// contract rather than asserted from reading the .proto by eye.
//
// protojson.MarshalOptions{UseProtoNames: true} - which is what the SDK and sigil's
// own server use - has four behaviours a hand-rolled struct gets wrong by default,
// each reproduced here on purpose:
//
//  1. int64 fields marshal as JSON STRINGS, not numbers (TokenUsage's six fields).
//  2. Enums marshal as their full symbolic name (GENERATION_MODE_STREAM), not a
//     number and not a bare suffix.
//  3. google.protobuf.Timestamp marshals as an RFC3339 string.
//  4. A zero-valued field is omitted entirely rather than sent as its zero.
package agento11y

// wireExportGenerationsRequest is the ExportGenerations request body.
type wireExportGenerationsRequest struct {
	Generations []wireGeneration `json:"generations"`
}

// wireExportGenerationsResponse is the ExportGenerations response body. Every
// generation gets its own accepted/error verdict - see push.go's doc comment on why
// this, not the HTTP status alone, is what push() trusts.
type wireExportGenerationsResponse struct {
	Results []wireExportGenerationResult `json:"results"`
}

type wireExportGenerationResult struct {
	GenerationID string `json:"generation_id"`
	Accepted     bool   `json:"accepted"`
	Error        string `json:"error"`
}

// Generation mode values - proto's GenerationMode enum, full symbolic name per trap
// #2 above. UNSPECIFIED is never emitted: mode is always derived, never absent.
const (
	modeSync   = "GENERATION_MODE_SYNC"
	modeStream = "GENERATION_MODE_STREAM"
)

// Message role values - proto's MessageRole enum. There is no DEVELOPER or SYSTEM
// value in sigil's vocabulary (only UNSPECIFIED/USER/ASSISTANT/TOOL) - see
// generation.go's mapRole for what a Turn.Prompt with role "developer" becomes.
const (
	roleUser      = "MESSAGE_ROLE_USER"
	roleAssistant = "MESSAGE_ROLE_ASSISTANT"
	roleTool      = "MESSAGE_ROLE_TOOL"
)

// tokenInputSemanticsInclusive is proto's TOKEN_INPUT_SEMANTICS_INCLUSIVE: input_tokens
// covers every input token type, with the cache buckets as SUBSETS of it rather than
// additions. That is exactly the shape the OpenAI Responses API usage block already
// has - InputTokensDetails.CachedTokens/CacheWriteTokens are a breakdown of
// InputTokens, not extra tokens on top (see reducer.go where t.InputTokens = u.InputTokens
// and t.CachedTokens = u.InputTokensDetails.CachedTokens are both read from the same
// usage block) - so this is a statement of fact about the source data, not a guess.
const tokenInputSemanticsInclusive = "TOKEN_INPUT_SEMANTICS_INCLUSIVE"

type wireModelRef struct {
	Provider string `json:"provider,omitempty"`
	Name     string `json:"name,omitempty"`
}

type wireMessage struct {
	Role  string     `json:"role,omitempty"`
	Parts []wirePart `json:"parts,omitempty"`
}

// wirePart mirrors proto's `oneof payload` by leaving every field but one at its zero
// value - protojson (and encoding/json, via omitempty) both then emit only the field
// that is actually set, which is what a real oneof looks like on the wire. Callers
// must never populate more than one.
type wirePart struct {
	Text       string          `json:"text,omitempty"`
	Thinking   string          `json:"thinking,omitempty"`
	ToolCall   *wireToolCall   `json:"tool_call,omitempty"`
	ToolResult *wireToolResult `json:"tool_result,omitempty"`
	Media      *wireMedia      `json:"media,omitempty"`
}

type wireToolCall struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	// InputJSON is proto's `bytes input_json`. A plain []byte field is exactly what
	// protojson's bytes encoding expects on the wire: encoding/json marshals it as
	// standard (padded) base64, matching protojson byte-for-byte - no manual encoding
	// call needed here, and none of the caller-provided text is treated as raw JSON to
	// splice in (which is what json.RawMessage would do, and would corrupt the
	// envelope the moment a tool's arguments were not valid JSON on their own).
	InputJSON []byte `json:"input_json,omitempty"`
}

type wireToolResult struct {
	ToolCallID string `json:"tool_call_id,omitempty"`
	Name       string `json:"name,omitempty"`
	Content    string `json:"content,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`
}

type wireMedia struct {
	Kind     string `json:"kind,omitempty"`
	URL      string `json:"url,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	Name     string `json:"name,omitempty"`
}

type wireTokenUsage struct {
	// int64-as-string per trap #1. Turn's counters are plain Go int, so these are
	// formatted, not typed as int64 - see generation.go's usageOf.
	InputTokens           string `json:"input_tokens,omitempty"`
	OutputTokens          string `json:"output_tokens,omitempty"`
	TotalTokens           string `json:"total_tokens,omitempty"`
	CacheReadInputTokens  string `json:"cache_read_input_tokens,omitempty"`
	CacheWriteInputTokens string `json:"cache_write_input_tokens,omitempty"`
	ReasoningTokens       string `json:"reasoning_tokens,omitempty"`
	InputSemantics        string `json:"input_semantics,omitempty"`
}

// wireGeneration is one Generation, protojson-UseProtoNames-shaped. Fields the proto
// defines but this emitter never populates (tools, metadata, raw_artifacts, agent_name,
// agent_version, max_tokens, temperature, top_p, tool_choice, thinking_enabled) are
// simply absent from this struct rather than present-and-always-empty: omitempty
// cannot be trusted to hide a struct or slice consistently across every Go version's
// json/v1 semantics, and an absent field is the same shape actual proto zero-values
// take on the wire per trap #4, so dropping the field entirely is both simpler and
// exactly correct. See the package doc comment on which of these are genuinely
// unavailable in a Turn versus explicitly out of scope for this lane.
type wireGeneration struct {
	ID                  string            `json:"id,omitempty"`
	ConversationID      string            `json:"conversation_id,omitempty"`
	OperationName       string            `json:"operation_name,omitempty"`
	Mode                string            `json:"mode,omitempty"`
	TraceID             string            `json:"trace_id,omitempty"`
	SpanID              string            `json:"span_id,omitempty"`
	Model               *wireModelRef     `json:"model,omitempty"`
	ResponseID          string            `json:"response_id,omitempty"`
	ResponseModel       string            `json:"response_model,omitempty"`
	SystemPrompt        string            `json:"system_prompt,omitempty"`
	Input               []wireMessage     `json:"input,omitempty"`
	Output              []wireMessage     `json:"output,omitempty"`
	Usage               *wireTokenUsage   `json:"usage,omitempty"`
	StopReason          string            `json:"stop_reason,omitempty"`
	StartedAt           string            `json:"started_at,omitempty"`
	CompletedAt         string            `json:"completed_at,omitempty"`
	Tags                map[string]string `json:"tags,omitempty"`
	CallError           string            `json:"call_error,omitempty"`
	ParentGenerationIDs []string          `json:"parent_generation_ids,omitempty"`
	EffectiveVersion    string            `json:"effective_version,omitempty"`
}
