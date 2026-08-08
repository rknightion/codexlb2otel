package live

import (
	"strings"
	"unicode"

	"github.com/rknightion/codexlb2otel/internal/turn"
)

// askChars bounds the derived request line, latestChars the agent's own narrative.
// The narrative gets far more room on purpose: measured against the corpus, assistant
// messages run 250-400 characters and are written as self-contained status reports, so
// clipping one at headline length throws away the half that says what happened.
const (
	askChars    = 110
	latestChars = 400
	toolChars   = 90
)

// scaffoldChars is the length above which a user-role message is treated as injected
// harness scaffolding rather than something a human typed.
//
// The threshold is the whole trick, and it is measured rather than guessed. In the
// 2026-08-08T16 capture the genuine human turns were 89 and 119 characters ("emergency
// pause as I need to reboot my itnernet ASAP", "you may resume. Make sure to re
// launch/unpause all child agents"). Everything else wearing the user role was machine
// -injected and an order of magnitude bigger: the workspace description at 28,582, the
// AGENTS.md block at 10,404, a resumption summary at 8,527, the compaction instruction
// at 426. There is a clean two-order-of-magnitude gap and nothing lands in it.
const scaffoldChars = 2000

// scaffoldPrefixes are openings that mark an injected message regardless of length -
// the ones short enough to slip under scaffoldChars. Compared case-insensitively
// against the trimmed start of the text.
//
// "You are performing a CONTEXT CHECKPOINT COMPACTION" is the important one: at 426
// characters it is under the size threshold, it is genuinely the NEWEST user-role
// message on any compacted thread, and it is identical on every thread that has one.
// Left in, it becomes the label on precisely the long-running threads a human most
// wants to tell apart.
var scaffoldPrefixes = []string{
	"you are performing a context checkpoint compaction",
	"you are codex",
	"another language model started",
	"# agents.md",
	"<user_instructions>",
	"<instructions>",
	"<recommended_plugins>",
	"<multi_agent_mode>",
	"## memory",
}

// TaskPath derives a thread's own collaboration task path, e.g.
// "/root/final_integration_review_v2", from the agent messages addressed TO it.
//
// This is the one field that gives a subagent a human-meaningful name, and it exists
// only by this indirection. The wire keeps two disjoint identity spaces: the transport
// layer knows uuid thread ids, and the collaboration layer knows slash-separated task
// paths. Nothing in the capture states the mapping.
//
// What DOES hold, measured across the corpus: every agent message delivered to a thread
// carries that thread's own path as `recipient`, and it is overwhelmingly consistent -
// 172 of 172 in thread 019fe1ee, naming "/root/final_integration_review_v2". The path
// hierarchy then independently reproduces the parent_thread_id hierarchy, which is what
// makes this trustworthy rather than a coincidence worth shipping.
//
// The obvious alternative - joining a spawn_agent call's task_name to the thread it
// created - DOES NOT WORK, and the reason is worth recording so it is not retried:
// subagent threads are PREWARMED. Their first frames precede the spawn call that gives
// them work (a child first seen at 16:00:00 against its supposed spawn at 16:02:40),
// and the counts do not even match (26 spawns, 22 threads). Any ordering-based join
// silently mislabels agents, which is worse than leaving them unnamed.
func TaskPath(turns []*turn.Turn) string {
	counts := map[string]int{}
	for _, t := range turns {
		for _, am := range t.AgentMessages {
			if am.Recipient == "" || am.Recipient == "/root" {
				continue
			}
			counts[am.Recipient]++
		}
	}
	best, bestN := "", 0
	for path, n := range counts {
		// Ties broken on the path itself, so the label cannot flip between polls purely
		// because Go randomises map iteration.
		if n > bestN || (n == bestN && path < best) {
			best, bestN = path, n
		}
	}
	return best
}

// TaskName is the last segment of a task path - "final_integration_review_v2" - which
// is what a row shows. The full path is kept separately for the detail view, where the
// ancestry is worth seeing.
func TaskName(path string) string {
	if path == "" {
		return ""
	}
	if i := strings.LastIndex(path, "/"); i >= 0 && i+1 < len(path) {
		return path[i+1:]
	}
	return path
}

// askOf derives what a human actually asked for, or "" when this turn carries nothing
// that qualifies. Callers keep the last non-empty answer.
//
// Only user-role messages are eligible. Developer-role ones are harness scaffolding
// without exception in the corpus - the system prompt, the memory block, the
// multi-agent-mode notice - and an earlier version of this function fell back to them
// on the reasoning that something beats nothing. It does not: it put a 17 KB "You are
// Codex, an agent based on GPT-5" on the row, identically, on every thread at once.
func askOf(t *turn.Turn, content bool) string {
	if !content {
		return ""
	}
	best := ""
	for _, p := range t.Prompts {
		if p.Role != "user" || isScaffold(p.Text) {
			continue
		}
		if s := oneLine(p.Text, askChars); s != "" {
			best = s
		}
	}
	return best
}

func isScaffold(text string) bool {
	if len(text) > scaffoldChars {
		return true
	}
	lower := strings.ToLower(strings.TrimSpace(text))
	for _, p := range scaffoldPrefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

// latestOf is the agent's own newest message - the single most informative field the
// archive carries. Present on root threads and forks alike, unlike the ask.
func latestOf(t *turn.Turn, content bool) string {
	if !content {
		return ""
	}
	for i := len(t.Messages) - 1; i >= 0; i-- {
		if s := oneLine(t.Messages[i].Text, latestChars); s != "" {
			return s
		}
	}
	return ""
}

// activityOf is the mechanical state - which tool, or which failure. Deliberately
// separate from latestOf: this changes every few seconds and belongs in a status
// column, whereas the narrative belongs in the body of the row.
func activityOf(t *turn.Turn, content bool) string {
	switch t.Status {
	case turn.StatusError:
		return strings.TrimSpace("error " + t.ErrorType + " " + t.ErrorCode)
	case turn.StatusIncomplete:
		return "incomplete"
	case turn.StatusTransport:
		return "transport " + t.TransportEvent
	}
	if n := len(t.ToolCalls); n > 0 {
		return describeTool(t.ToolCalls[n-1], content)
	}
	return "responded"
}

// activityOfInFlight is the same for a response that has not finished, where the
// interesting states are ones a completed turn can never be in.
func activityOfInFlight(f turn.InFlight, content bool) string {
	if f.SpawnedTask != "" {
		return "spawning " + f.SpawnedTask
	}
	if f.LastToolCall != "" {
		return describeTool(turn.ToolCall{Name: f.LastToolCall, Input: f.LastToolInput}, content)
	}
	if f.TextDeltas > 0 {
		return "writing"
	}
	// No text, no tool: the model is reasoning. This is the common state for a
	// long-running response and is invisible from completed turns, which is the whole
	// reason the in-flight view exists.
	return "thinking"
}

// SpawnedTasks lists the task names a turn dispatched, newest last. Reported against
// the thread that MADE the calls - which is all the capture can honestly support, since
// the spawn cannot be joined to the thread it created (see TaskPath).
func SpawnedTasks(turns []*turn.Turn) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range turns {
		for _, tc := range t.ToolCalls {
			if tc.TaskName == "" || seen[tc.TaskName] {
				continue
			}
			seen[tc.TaskName] = true
			out = append(out, TaskName(tc.TaskName))
		}
	}
	return out
}

func describeTool(tc turn.ToolCall, content bool) string {
	name := tc.Name
	if name == "" {
		name = "tool"
	}
	if !content || tc.Input == "" {
		return name
	}
	arg := oneLine(tc.Input, toolChars)
	if arg == "" {
		return name
	}
	return name + " " + arg
}

// oneLine collapses text to a single trimmed line of at most max runes.
//
// Rune-counted, not byte-counted: a byte cut lands mid-sequence on any non-ASCII prompt
// and produces invalid UTF-8, which json.Marshal silently rewrites to U+FFFD - so the
// bug would surface as mojibake in the browser rather than as an error anywhere near
// here.
func oneLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Fold every run of whitespace, newlines included, into single spaces. These are
	// multi-line bodies and taking literally the first line usually yields a bare
	// heading.
	var b strings.Builder
	space := false
	n := 0
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			if n++; n > max {
				break
			}
			b.WriteRune(' ')
		}
		space = false
		if n++; n > max {
			break
		}
		b.WriteRune(r)
	}
	out := b.String()
	if n > max {
		out += "…"
	}
	return out
}
