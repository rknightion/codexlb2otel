package agento11y

import (
	"strconv"
	"strings"
	"time"

	"github.com/rknightion/codexlb2otel/internal/attr"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

// buildGeneration turns one reduced Turn into the Generation this sink will push -
// the one-response-per-Generation mapping the issue's own research already verified,
// not reshaped OTLP spans (issue #19's comment: sigil's product surface is populated
// only by ExportGenerations or its own tolerant OTLP-span decoder; the existing
// otlptrace sink to Tempo is untouched by this package).
//
// Fields the proto defines but this function never sets, and why:
//
//   - agent_name / agent_version: no agent-name concept exists anywhere in the
//     capture - codex-lb has no notion of "which agent", only which account and
//     client binary served a request. Permanent gap, not an oversight.
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
// tools, temperature and top_p (issue #23) ARE now set, closing the three gaps this
// comment used to list here - see toolsOf/wireGeneration's own doc comment for
// tools/temperature/top_p respectively. Tools is empty on most turns because
// Turn.Tools only carries a catalogue on the response where its hash first changes
// (turn.go's own comment on Tools/ToolsHash) - that is correct dedup behaviour, not a
// gap to work around, so this function never tries to backfill a catalogue for a
// response that did not carry one.
//
// stop_reason is deliberately left unset, and this is the same call made for
// gen_ai.response.finish_reasons on issue #18 - the two must agree or the codebase
// holds two contradictory answers to one question.
//
// It is tempting to pass Turn.Status through verbatim, since stop_reason is free text
// rather than an enum. But status is a DIFFERENT AXIS, not a coarser version of the
// same one: it answers "did this pipeline observe the response finish", where
// stop_reason answers "why did the model stop generating". Measured across the full
// corpus, response.status takes exactly one value ever - "completed" - while
// finish_reason and stop_reason appear nowhere in the capture at all, and three of our
// four status values (incomplete, transport, error) are manufactured by this service
// rather than reported by the server. Writing "transport" into stop_reason would tell
// sigil's UI the model stopped for a reason the model had no part in.
//
// Nothing is lost: codexlb.status is already carried in tags, under a name that says
// what it actually means.
//
// effective_version is deliberately left unset. The proto requires it to match
// ^sha256:[0-9a-f]{64}$ or ingest REJECTS the generation outright; Turn.InstructionsHash
// is sha256(instructions)[:8] hex-encoded - 16 hex characters, not 64 (see
// turn.shortHash) - so it does not qualify, and prefixing "sha256:" onto a truncated
// hash would produce a value that matches the regex but is not actually a sha256 sum
// of anything, which is worse than omitting it. The proto's own fallback - "the
// backend falls back to a server-computed sha256 of (system_prompt + sorted tools)
// when omitted" - is what this emitter relies on instead.
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
	g := wireGeneration{
		ID:             generationID(t),
		ConversationID: t.ThreadID,
		OperationName:  attr.GenAIOperationValue, // "chat" - same value the existing GenAI metrics/spans use
		Mode:           mode(t),
		TraceID:        t.TraceID,
		SpanID:         t.SpanID,
		Model:          &wireModelRef{Provider: attr.GenAIProviderValue, Name: t.Model},
		ResponseID:     t.ResponseID,
		ResponseModel:  responseModel(t),
		StartedAt:      rfc3339(startedAt(t)),
		CompletedAt:    rfc3339(completedAt(t)),
		CallError:      callError(t),
	}

	if tags := tagsOf(guard, t); len(tags) > 0 {
		g.Tags = tags
	}
	if usage := usageOf(t); usage != nil {
		g.Usage = usage
	}
	g.Input, g.SystemPrompt = inputMessages(t)
	g.Output = outputMessages(t)
	g.Tools = toolsOf(t.Tools)
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

// toolsOf maps Turn.Tools (populated only on the response where a catalogue's hash
// first changes - see turn.go's comment on Tools/ToolsHash) onto proto's
// ToolDefinition. Returns nil for an empty/nil input rather than an empty non-nil
// slice, matching every other "absent means nothing to report" field in this file
// (usageOf, tagsOf) and letting Go's own json encoding of a nil slice with omitempty
// drop the field entirely, as wireGeneration's own doc comment requires.
func toolsOf(defs []turn.ToolDef) []wireToolDefinition {
	if len(defs) == 0 {
		return nil
	}
	out := make([]wireToolDefinition, 0, len(defs))
	for _, d := range defs {
		out = append(out, wireToolDefinition{Name: d.Name, Description: d.Description, Type: d.Kind})
	}
	return out
}

// generationID is what a repeated push (retry, or a checkpoint replay) must agree on,
// so sigil's own dedup - if it has any - has something stable to key on. ResponseID
// (resp_*) is the natural choice: it is the id of the exact model response this
// Generation represents, one-to-one with the Turn. It is empty on responses that never
// completed (a bare transport or error record), so RequestID - always populated - is
// the fallback.
func generationID(t *turn.Turn) string {
	if t.ResponseID != "" {
		return t.ResponseID
	}
	return t.RequestID
}

// mode is STREAM when this response delivered any text deltas over the websocket,
// SYNC otherwise. Always one or the other - never GENERATION_MODE_UNSPECIFIED.
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
func responseModel(t *turn.Turn) string {
	if t.SafetyRetryModel != "" {
		return t.SafetyRetryModel
	}
	return t.Model
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
	return ts.UTC().Format(time.RFC3339)
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
		InputTokens:           strconv.Itoa(t.InputTokens),
		OutputTokens:          strconv.Itoa(t.OutputTokens),
		TotalTokens:           strconv.Itoa(t.TotalTokens),
		CacheReadInputTokens:  strconv.Itoa(t.CachedTokens),
		CacheWriteInputTokens: strconv.Itoa(t.CacheWriteTokens),
		ReasoningTokens:       strconv.Itoa(t.ReasoningTokens),
		InputSemantics:        tokenInputSemanticsInclusive,
	}
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
		out = append(out, wireMessage{Role: mapRole(p.Role), Parts: parts})
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
