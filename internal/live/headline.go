package live

import (
	"strings"
	"unicode"

	"github.com/rknightion/codexlb2otel/internal/turn"
)

// headlineChars bounds a derived one-liner. Long enough to carry the gist of a request,
// short enough that a row stays a row.
const headlineChars = 120

// headlineOf derives "what was this asked to do" from one turn, or "" when the turn says
// nothing new about it. Callers keep the last non-empty answer.
//
// A user prompt beats a developer one because the developer role carries system
// scaffolding, which is identical across every thread and so distinguishes nothing.
//
// A spawn_agent call's TaskName is deliberately NOT used here, and that is a correction:
// it reads like the perfect label for a subagent, but the call appears on the turn of the
// thread doing the SPAWNING, never on the child's own turns. Preferring it made a parent
// thread announce itself as "spawn /root/explore_reducer" while hiding the human request
// that had actually driven it - caught by loading the page rather than by any assertion.
// The spawn is an ACTIVITY of the parent, which is where activityOf reports it.
func headlineOf(t *turn.Turn, content bool) string {
	if !content {
		// Nothing left that is safe to say. The structural fields a content-free view can
		// show (model, subagent kind) are already their own columns, and duplicating one
		// into the headline would only make every row look identical.
		return ""
	}

	best := ""
	for _, p := range t.Prompts {
		switch p.Role {
		case "user":
			// Newest user prompt wins outright; keep scanning in case there are several.
			if s := firstLine(p.Text); s != "" {
				best = s
			}
		case "developer":
			if best == "" {
				if s := firstLine(p.Text); s != "" {
					best = s
				}
			}
		}
	}
	return best
}

// activityOf derives "what is it doing" from a completed turn. Unlike headlineOf this is
// meant to change every turn.
func activityOf(t *turn.Turn, content bool) string {
	switch t.Status {
	case turn.StatusError:
		// The error TYPE and CODE are enum-like; the message embeds ids and free text, so
		// it is content even though it reads like metadata.
		return strings.TrimSpace("error " + t.ErrorType + " " + t.ErrorCode)
	case turn.StatusIncomplete:
		return "incomplete"
	case turn.StatusTransport:
		return "transport " + t.TransportEvent
	}

	if n := len(t.ToolCalls); n > 0 {
		return describeTool(t.ToolCalls[n-1], content)
	}
	if content {
		if n := len(t.Messages); n > 0 {
			if s := firstLine(t.Messages[n-1].Text); s != "" {
				return s
			}
		}
	}
	return "responded"
}

// activityOfInFlight is the same idea for a response that has not finished, where the
// interesting states are the ones a completed turn can never be in.
func activityOfInFlight(f turn.InFlight, content bool) string {
	if f.SpawnedTask != "" {
		return "spawning " + f.SpawnedTask
	}
	if f.LastToolCall != "" {
		return describeTool(turn.ToolCall{Name: f.LastToolCall, Input: f.LastToolInput}, content)
	}
	if content && f.LastMessage != "" {
		if s := firstLine(f.LastMessage); s != "" {
			return s
		}
	}
	if f.TextDeltas > 0 {
		return "writing"
	}
	// No text, no tool, no message: the model is reasoning. This is the common case for
	// a long-running response and the reason the in-flight view exists at all - none of
	// it is visible from completed turns.
	return "thinking"
}

func describeTool(tc turn.ToolCall, content bool) string {
	name := tc.Name
	if name == "" {
		name = "tool"
	}
	if !content || tc.Input == "" {
		return name
	}
	arg := firstLine(tc.Input)
	if arg == "" {
		return name
	}
	return name + " " + arg
}

// firstLine collapses text to a single trimmed line of at most headlineChars runes.
//
// Rune-counted, not byte-counted: a byte cut lands mid-sequence on any non-ASCII prompt
// and produces invalid UTF-8, which json.Marshal silently rewrites to U+FFFD - so the
// bug would surface as mojibake in the browser rather than as an error anywhere near
// here.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Fold every run of whitespace, newlines included, into single spaces. A prompt is
	// usually many lines and taking only the first would often take a bare "Task:".
	var b strings.Builder
	space := false
	n := 0
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			if n++; n > headlineChars {
				break
			}
			b.WriteRune(' ')
		}
		space = false
		if n++; n > headlineChars {
			break
		}
		b.WriteRune(r)
	}
	out := b.String()
	if n > headlineChars {
		out += "…"
	}
	return out
}
