package summary

import (
	"fmt"
	"strings"

	"github.com/rknightion/codexlb2otel/internal/turn"
)

// Digest is one thread rendered as prose for the model.
//
// Chunks is normally length one. It is longer only when the thread exceeded
// MaxCharsPerSession, in which case each chunk is summarised separately and the
// summaries are summarised - and the count reaches the output, so a vaguer narrative is
// explained rather than mysterious.
type Digest struct {
	Chunks []string
	// Chars is the total size of every chunk, which is what -dry-run reports.
	Chars int
	// Turns is how many turns went in, so an empty digest can say whether it found
	// nothing or was given nothing.
	Turns int
}

// Passes is how many summarising calls this digest costs.
func (d Digest) Passes() int { return len(d.Chunks) }

// Build renders turns into a digest under o's budgets.
//
// What goes in is chosen for one question - what work was ACCOMPLISHED - and that
// choice is the whole design of this function:
//
//   - Tool call ARGUMENTS get a generous budget. The paths written, the commands run and
//     the patch bodies are what say something changed; nothing else in the capture does.
//   - Tool OUTPUT gets a tight one, as a head and a tail. It is mostly console noise, but
//     a command's verdict is usually its last line, so head-only truncation reliably cuts
//     exactly the part worth keeping.
//   - Errors are never truncated away. A session that failed has to read as failed.
//
// Deliberately absent: instruction bodies (identified by hash only - they reach 67 KB and
// take a handful of distinct values a day), tool catalogues, encrypted reasoning, and
// every timing, token, cache, tier and rate-limit field. Those are already exported to
// Grafana and dashboarded; asking a language model to restate them would be slower, more
// expensive and less accurate than the metrics pipeline that already has them.
func Build(turns []*turn.Turn, o Options) Digest {
	o.setDefaults()

	d := Digest{Turns: len(turns)}
	var cur strings.Builder

	flush := func() {
		if cur.Len() > 0 {
			d.Chunks = append(d.Chunks, cur.String())
			cur.Reset()
		}
	}

	for i, t := range turns {
		block := renderTurn(i+1, t, o)
		if block == "" {
			continue
		}
		// Chunk at TURN boundaries only. Splitting mid-turn separates a tool call from
		// its result, which is worse than a chunk running over: the model then reports a
		// command that was never answered and an answer to nothing.
		if cur.Len() > 0 && cur.Len()+len(block) > o.MaxCharsPerSession {
			flush()
		}
		cur.WriteString(block)
	}
	flush()

	for _, c := range d.Chunks {
		d.Chars += len(c)
	}
	return d
}

// renderTurn renders one turn, or "" if it carries no content worth sending.
//
// Ordering within the turn follows the conversation rather than the struct: what was
// asked, what came back from the last tool, what the agent said, what it then did, and
// what it told other agents. A reader - human or model - follows that; struct order is
// an implementation detail of the reducer.
func renderTurn(n int, t *turn.Turn, o Options) string {
	var b strings.Builder

	for _, p := range t.Prompts {
		// Only genuine input. "assistant" prompts are the model's own prior messages
		// replayed by response.create, which re-sends the whole conversation every turn -
		// including them would repeat the entire transcript once per remaining turn.
		if p.Role == "assistant" || p.Text == "" {
			continue
		}
		fmt.Fprintf(&b, "\n%s: %s\n", strings.ToUpper(p.Role), clip(p.Text, o.MaxCharsPerPrompt))
		if p.Images > 0 {
			fmt.Fprintf(&b, "[%d image(s) attached]\n", p.Images)
		}
	}
	for _, out := range t.ToolOutputs {
		if out.Text == "" {
			continue
		}
		fmt.Fprintf(&b, "\nRESULT: %s\n", headTail(out.Text, o.MaxCharsPerToolOutput))
	}
	for _, m := range t.Messages {
		if m.Text == "" {
			continue
		}
		fmt.Fprintf(&b, "\nASSISTANT: %s\n", clip(m.Text, o.MaxCharsPerMessage))
	}
	for _, c := range t.ToolCalls {
		fmt.Fprintf(&b, "\nTOOL %s", c.Name)
		// A spawn names the child agent, which is the only human-meaningful label a
		// subagent ever gets - and the join that makes "it dispatched three reviewers"
		// sayable at all.
		if c.TaskName != "" {
			fmt.Fprintf(&b, " -> spawns %s", c.TaskName)
		}
		b.WriteString("\n")
		if c.Input != "" {
			fmt.Fprintf(&b, "%s\n", clip(c.Input, o.MaxCharsPerToolInput))
		}
	}
	for _, m := range t.AgentMessages {
		if m.Text == "" {
			continue
		}
		fmt.Fprintf(&b, "\nAGENT %s -> %s: %s\n",
			nonEmpty(m.Author, "?"), nonEmpty(m.Recipient, "?"), clip(m.Text, o.MaxCharsPerMessage))
	}

	// Errors last and never truncated away: they are short, they are rare, and they are
	// the difference between "it did the work" and "it tried and failed".
	if t.Status == turn.StatusError || t.ErrorType != "" || t.ErrorMessage != "" {
		fmt.Fprintf(&b, "\nERROR %s\n", strings.TrimSpace(strings.Join(
			nonEmptyAll(t.ErrorType, t.ErrorCode, clip(t.ErrorMessage, o.MaxCharsPerMessage)), " ")))
	}

	if b.Len() == 0 {
		return ""
	}
	head := fmt.Sprintf("\n--- turn %d  %s", n, t.FirstTS.UTC().Format("2006-01-02 15:04:05"))
	if t.Status != "" {
		head += "  " + t.Status
	}
	return head + "\n" + b.String()
}

// clip truncates to max characters, saying how much it removed.
//
// The marker matters: a model handed a sentence that simply stops will confidently
// describe whatever it was cut off mid-way through as the whole story.
func clip(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n…[%d chars elided]", len(s)-max)
}

// headTail keeps the first and last half of max, which is how a tool result survives
// truncation with its verdict intact. See Build for why head-only is wrong here.
func headTail(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	half := max / 2
	if half == 0 {
		return clip(s, max)
	}
	return s[:half] + fmt.Sprintf("\n…[%d chars elided]…\n", len(s)-2*half) + s[len(s)-half:]
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func nonEmptyAll(ss ...string) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
