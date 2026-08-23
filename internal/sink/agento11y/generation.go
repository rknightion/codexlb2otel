package agento11y

import (
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/rknightion/codexlb2otel/internal/attr"
	"github.com/rknightion/codexlb2otel/internal/correlation"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

// buildGeneration turns one reduced Turn into the Generation this sink will push -
// the one-response-per-Generation mapping the issue's own research already verified,
// not reshaped OTLP spans (issue #19's comment: sigil's product surface is populated
// only by ExportGenerations or its own tolerant OTLP-span decoder; the existing
// otlptrace sink to Tempo is untouched by this package).
//
// agent_name follows the dedicated Codex coding-agent plugin: ordinary turns are
// "codex" and only source-proven child threads are "codex/subagent". The client
// entrypoint remains in codexlb.originator rather than fragmenting one agent into
// several catalog entries. agent_version remains the instructions fingerprint; the
// full effective_version combines it with the repeated tool-catalogue fingerprint.
//
// Fields the proto defines but this function never sets, and why:
//
//   - max_tokens / tool_choice / thinking_enabled: request parameters that never
//     reach the reducer at all; Turn has no field for any of them.
//   - metadata (a free-form Struct) and raw_artifacts: nothing in the brief asked
//     for these, and assembling either would mean inventing an attribute set outside
//     attr.Guard - exactly what tags below exists to avoid.
//   - per-tool-call timing: every ToolCall spans the whole response in the capture
//     (an acknowledged approximation elsewhere in this repo), and there is nowhere
//     on Part or ToolCall to attach a start/end anyway, so nothing here implies a
//     timing this sink cannot back up.
//
// tools combines the richer, deduplicated request catalogue with names actually
// called by this response. That matches the Codex plugin's useful fallback when a
// full catalogue body is unavailable, without pretending to reconstruct schemas.
//
// stop_reason is "completed" only when the source reports successful completion and
// no call error exists, matching the Codex plugin's completed-turn outcome. The
// pipeline statuses incomplete, transport, and error are never passed through as
// fabricated model stop reasons; they remain in codexlb.status and call_error.
//
// parent_generation_ids is deliberately left unset. Turn.ParentTurnID lives in the
// SERVER's turn-id space (turn_*, from response.create's client_metadata); this
// emitter's own Generation.id lives in the response-id space (resp_*, see
// generationID). There is no local join between the two - it would require caching
// every turn_id -> resp_id mapping this process has ever seen, keyed across threads,
// which is state this hand-written per-batch emitter does not keep. Emitting
// ParentTurnID's raw value as a parent_generation_ids entry would produce an id that
// resolves to nothing in sigil's id space, which is worse than the field being absent.
func buildGeneration(t *turn.Turn, guard *attr.Guard) wireGeneration {
	modelName := strings.TrimSpace(t.Model)
	provider := attr.GenAIProviderValue
	if modelName == "" {
		modelName = "unknown"
		provider = "codex"
	}
	g := wireGeneration{
		ID:             attr.GenerationID(t),
		ConversationID: t.ThreadID,
		// generateText | streamText - the same value the metrics and the response span
		// now carry, from the same helper. Sigil substitutes a default only when this
		// field is EMPTY (its generation service's normalizeGeneration), so a value it
		// does not recognise is stored verbatim and classified as unknown; sending
		// "chat" here was not a harmless approximation.
		OperationName:    attr.OperationName(t),
		Mode:             mode(t),
		TraceID:          correlation.TraceID(t).String(),
		SpanID:           correlation.ResponseSpanID(t).String(),
		Model:            &wireModelRef{Provider: provider, Name: modelName},
		ResponseID:       t.ResponseID,
		ResponseModel:    responseModel(t, modelName),
		AgentName:        attr.AgentName(t),
		AgentVersion:     t.InstructionsHash,
		EffectiveVersion: effectiveVersion(t),
		StartedAt:        rfc3339(startedAt(t)),
		CompletedAt:      rfc3339(completedAt(t)),
		CallError:        callError(t),
	}
	if t.Status == "completed" && g.CallError == "" {
		g.StopReason = "completed"
	}

	if tags := tagsOf(guard, t); len(tags) > 0 {
		g.Tags = tags
	}
	if usage := usageOf(t); usage != nil {
		g.Usage = usage
	}
	g.Input, g.SystemPrompt = inputMessages(t)
	g.Output = outputMessages(t)
	g.Tools = toolsOf(t.Tools, t.ToolCalls)
	// != 0, not > 0: temperature 0 (fully greedy decoding) is a legitimate request
	// setting, unlike every ms-duration field elsewhere in this codebase where 0 only
	// ever means "not populated". See wireGeneration's own doc comment for the
	// zero-vs-absent ambiguity this still cannot fully resolve (inherited from Turn's
	// plain float64 fields, not introduced here).
	if t.Temperature != 0 {
		v := t.Temperature
		g.Temperature = &v
	}
	if t.TopP != 0 {
		v := t.TopP
		g.TopP = &v
	}

	return g
}

// toolsOf maps the occasional full request catalogue and fills its gaps with the
// names actually called in this response, which is the evidence available to the
// Codex hook plugin too. Catalogue definitions win on duplicate names. Returns nil
// only when neither source reports a tool.
func toolsOf(defs []turn.ToolDef, calls []turn.ToolCall) []wireToolDefinition {
	if len(defs) == 0 && len(calls) == 0 {
		return nil
	}
	out := make([]wireToolDefinition, 0, len(defs)+len(calls))
	seen := make(map[string]struct{}, len(defs)+len(calls))
	for _, d := range defs {
		name := strings.TrimSpace(d.Name)
		if name == "" {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, wireToolDefinition{Name: name, Description: d.Description, Type: d.Kind})
	}
	for _, c := range calls {
		name := strings.TrimSpace(c.Name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		toolType := strings.TrimSpace(c.Kind)
		if toolType == "" {
			toolType = "function"
		}
		seen[name] = struct{}{}
		out = append(out, wireToolDefinition{Name: name, Type: toolType})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// mode is STREAM when this response delivered any text deltas over the websocket,
// SYNC otherwise. Always one or the other - never GENERATION_MODE_UNSPECIFIED.
//
// The same test attr.OperationName applies to pick streamText over generateText, and
// that is not a coincidence to be tidied away: sigil's own ingest derives the default
// operation name FROM the mode with exactly this mapping, so the two disagreeing would
// make a generation self-contradictory on arrival.
func mode(t *turn.Turn) string {
	if t.TextDeltaCount > 0 {
		return modeStream
	}
	return modeSync
}

// responseModel mirrors attr's own GenAIResponseModel field.Of closure exactly
// (internal/attr/attr.go's registry): equal to the requested model except when
// safety buffering re-ran the response through a different one, which is the one
// case worth being able to see. Duplicated rather than imported because that logic
// lives inside an unexported closure literal in attr's registry, not as a callable
// function - reusing it as-is would mean exporting it from a package this lane does
// not own.
func responseModel(t *turn.Turn, fallback string) string {
	if t.SafetyRetryModel != "" {
		return t.SafetyRetryModel
	}
	return fallback
}

// effectiveVersion joins the archive's repeated instruction and tool-catalogue
// fingerprints into one full SHA-256 value accepted by Agent Observability. The
// bodies are intentionally sparse; their fingerprints let those occasional bodies
// accumulate under one catalog version rather than create empty phantom versions.
func effectiveVersion(t *turn.Turn) string {
	seed := fmt.Sprintf("codex\ninstructions=%s\ntools=%s", t.InstructionsHash, t.ToolsHash)
	sum := sha256.Sum256([]byte(seed))
	return fmt.Sprintf("sha256:%x", sum)
}

// startedAt/completedAt prefer the server's own response bounds over the capture
// pipeline's frame-arrival timestamps: a Generation's started_at/completed_at should
// describe the model call, not when this service happened to observe its frames.
// ServerCreatedAt/ServerCompletedAt are exactly that ("Server-side response bounds,
// independent of when the frames were captured" - turn.go's own doc comment) but are
// not populated on every record (e.g. a bare transport-close record never got a
// server-created event), so FirstTS/LastTS - always populated - are the fallback.
func startedAt(t *turn.Turn) time.Time {
	if !t.ServerCreatedAt.IsZero() {
		return t.ServerCreatedAt
	}
	return t.FirstTS
}

func completedAt(t *turn.Turn) time.Time {
	if !t.ServerCompletedAt.IsZero() {
		return t.ServerCompletedAt
	}
	return t.LastTS
}

func rfc3339(ts time.Time) string {
	if ts.IsZero() {
		return ""
	}
	return ts.UTC().Format(time.RFC3339Nano)
}

// callError folds Turn's three error fields into proto's single call_error string.
// ErrorMessage already embeds ids and free text (internal/attr marks it Identity,
// never a metric attribute), so it is fine as the tail of a human-readable line here.
func callError(t *turn.Turn) string {
	if t.ErrorType == "" && t.ErrorCode == "" && t.ErrorMessage == "" {
		return ""
	}
	var b strings.Builder
	if t.ErrorType != "" {
		b.WriteString(t.ErrorType)
	}
	if t.ErrorCode != "" {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString("(" + t.ErrorCode + ")")
	}
	if t.ErrorMessage != "" {
		if b.Len() > 0 {
			b.WriteString(": ")
		}
		b.WriteString(t.ErrorMessage)
	}
	return b.String()
}

// usageOf carries all six token counts, matching TOKEN_INPUT_SEMANTICS_INCLUSIVE per
// tokenInputSemanticsInclusive's doc comment. Returns nil - so Usage is omitted
// entirely - when every counter is zero, matching trap #4 (a real proto zero-usage
// message would itself be entirely absent, not a struct of "0" strings).
func usageOf(t *turn.Turn) *wireTokenUsage {
	if t.InputTokens == 0 && t.OutputTokens == 0 && t.TotalTokens == 0 &&
		t.CachedTokens == 0 && t.CacheWriteTokens == 0 && t.ReasoningTokens == 0 {
		return nil
	}
	return &wireTokenUsage{
		InputTokens:           positiveItoa(t.InputTokens),
		OutputTokens:          positiveItoa(t.OutputTokens),
		TotalTokens:           positiveItoa(t.TotalTokens),
		CacheReadInputTokens:  positiveItoa(t.CachedTokens),
		CacheWriteInputTokens: positiveItoa(t.CacheWriteTokens),
		ReasoningTokens:       positiveItoa(t.ReasoningTokens),
		InputSemantics:        tokenInputSemanticsInclusive,
	}
}

func positiveItoa(n int) string {
	if n <= 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// tagsOf sources tags from attr.Guard.SpanAttrs - the same bounded+identity attribute
// set the otlptrace sink puts on a span, built through the one shared cardinality
// guard every sink shares - rather than assembling a bespoke set here. Keys keep
// their dotted OTel form (codexlb.family, gen_ai.conversation.id): attr.LokiKey's
// underscore rewrite is a Loki label-grammar workaround with nothing to do with this
// wire format, and applying it here would be copying a Loki-specific fix into a
// protocol that never had the problem it fixes.
func tagsOf(guard *attr.Guard, t *turn.Turn) map[string]string {
	kvs := guard.SpanAttrs(t)
	if len(kvs) == 0 {
		return nil
	}
	out := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		out[kv.Key] = kv.Value
	}
	return out
}

// mapRole translates a Turn.Prompt's role into proto's MessageRole. Sigil's vocabulary
// is UNSPECIFIED/USER/ASSISTANT/TOOL only - there is no DEVELOPER or SYSTEM value -
// so a "developer" prompt (the harness's own instructions-adjacent messages, distinct
// from the system prompt itself, which is routed to system_prompt before this is ever
// called - see inputMessages) maps to "" (MESSAGE_ROLE_UNSPECIFIED, omitted on the
// wire) rather than being mislabeled as MESSAGE_ROLE_USER. An omitted role is honest
// about not knowing; a wrong one would misattribute who said it.
func mapRole(role string) string {
	switch role {
	case "user":
		return roleUser
	case "assistant":
		return roleAssistant
	default:
		return ""
	}
}

// inputMessages builds the input array from Turn.Prompts and Turn.ToolOutputs, and
// separately returns the system prompt text pulled out of Prompts along the way.
//
// Ordering: Prompts in capture order, followed by ToolOutputs in capture order.
// Turn carries no relative-ordering signal between the two (no per-item timestamp),
// so this is the simplest defensible order rather than a claim about true
// conversation interleaving - the same limitation the Loki sink has, just less
// visible there because each becomes its own timestamped line instead of sharing one
// array.
func inputMessages(t *turn.Turn) ([]wireMessage, string) {
	var systemPrompt string
	out := make([]wireMessage, 0, len(t.Prompts)+len(t.ToolOutputs))

	for _, p := range t.Prompts {
		if p.Role == "instructions" {
			// First one wins. Instructions are re-sent on every response.create and
			// captured only once per hash (turn.go's own comment on Turn.Prompts /
			// InstructionsHash), so a Turn should never carry more than one of these,
			// but silently overwriting on the rare chance of a second would be worse
			// than deterministically keeping the first.
			if systemPrompt == "" {
				systemPrompt = p.Text
			}
			continue
		}
		role := mapRole(p.Role)
		if role == "" {
			continue
		}
		parts := make([]wirePart, 0, 1+p.Images)
		if p.Text != "" {
			parts = append(parts, wirePart{Text: p.Text})
		}
		// Images counts image parts on this message but carries none of the payload
		// (up to 784 KB base64 data URIs - see turn.go's Prompt.Images doc comment).
		// url stays empty for the same reason: the only url these ever had WAS the
		// payload, inline. ImageMIME is whatever the data URI declared for itself, and
		// is empty rather than guessed when it declared nothing.
		for i := 0; i < p.Images; i++ {
			parts = append(parts, wirePart{Media: &wireMedia{Kind: "image", MimeType: p.ImageMIME}})
		}
		if len(parts) == 0 {
			continue
		}
		out = append(out, wireMessage{Role: role, Parts: parts})
	}

	for _, to := range t.ToolOutputs {
		if to.Text == "" && to.CallID == "" {
			continue
		}
		out = append(out, wireMessage{
			Role: roleTool,
			Parts: []wirePart{{ToolResult: &wireToolResult{
				ToolCallID: to.CallID,
				Content:    to.Text,
			}}},
		})
	}

	if len(out) == 0 {
		out = nil
	}
	return out, systemPrompt
}

// outputMessages builds the output array from Turn.Messages (assistant text) followed
// by Turn.ToolCalls (function calls), both role ASSISTANT, in capture order - the
// same ordering caveat as inputMessages applies: Turn has no signal for how text and
// tool calls interleaved within one response.
func outputMessages(t *turn.Turn) []wireMessage {
	out := make([]wireMessage, 0, len(t.Messages)+len(t.ToolCalls))

	for _, m := range t.Messages {
		if m.Text == "" {
			continue
		}
		out = append(out, wireMessage{Role: roleAssistant, Parts: []wirePart{{Text: m.Text}}})
	}

	for _, tc := range t.ToolCalls {
		out = append(out, wireMessage{Role: roleAssistant, Parts: []wirePart{{ToolCall: &wireToolCall{
			ID:        tc.CallID,
			Name:      tc.Name,
			InputJSON: []byte(tc.Input),
		}}}})
	}

	if len(out) == 0 {
		return nil
	}
	return out
}
