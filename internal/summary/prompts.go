package summary

import (
	"fmt"
	"strings"

	"github.com/rknightion/codexlb2otel/internal/live"
)

// The prompts all serve one instruction: report the WORK, not the conversation.
//
// The failure mode they exist to prevent is the obvious one - handed a transcript, a model
// narrates it ("the user asked X, the assistant then ran Y, then explained Z"), which is
// just the transcript again and longer. What is wanted is the standup line: what exists
// now that did not exist before.
//
// The second failure mode is invention. These digests are truncated by construction, and
// a model that fills gaps confidently produces a work log that is wrong in exactly the
// places nobody can check, which is worse than one that says it does not know.

const common = `Report only what the transcript shows. Never invent a result, a file name,
a command or an outcome that is not there. The transcript is truncated in places, marked
"[N chars elided]" - treat those as unknown, not as nothing.

Say plainly when work failed, was abandoned, or achieved nothing. A session that spent an
hour and finished nothing is a useful thing to be told, and dressing it up as progress is
the worst output you can produce.

Do not report token counts, durations, costs, model names or response times. Those are
measured elsewhere and are not what you are for.

Do not narrate the conversation turn by turn. Nobody wants "the user asked, the assistant
replied". Write about the work.

Do not open with a title or a top-level heading. Your text is dropped into a section of a
document that already has one, so a heading of your own reads as a duplicate. Sub-headings
WITHIN your answer are fine when the material genuinely needs them.`

const subagentPrompt = `You are reading the transcript of a sub-agent that was spawned to do
one piece of a larger task.

Report what this sub-agent accomplished, in at most a short paragraph. Name the concrete
things: files it changed, commands it ran, what it found, what it concluded, whether it
finished. If it was a review or an investigation, its verdict is the important part.

` + common

const sessionPrompt = `You are reading the transcript of one agent coding session.

Report what was accomplished. Write it the way an engineer writes up their own day: what
was built, changed, fixed, deployed, investigated or decided. Name the concrete artifacts -
features, files, commands, services, tests, commits - because "improved the codebase" tells
the reader nothing they did not already assume.

Lead with the substance. Follow with anything notable: work that failed or was reverted,
problems hit, decisions made and why, anything left unfinished.

Where sub-agent summaries are supplied, fold their work in as part of the account rather
than listing them separately - the reader wants what the session achieved, not its
org chart. Mention a sub-agent explicitly only where what it did or found matters on its
own.

A few short paragraphs. Prose, not bullet points, unless the work genuinely was a list of
unrelated items.

` + common

const chunkPrompt = `You are reading ONE PART of a long agent session transcript, split
because it was too large to read at once.

Report what was accomplished in this part alone. Be concrete and be complete - this
summary, not the transcript, is what a later pass will read, so anything you leave out is
lost. Do not speculate about what happened in the other parts.

` + common

const windowPrompt = `You are reading summaries of several agent coding sessions that ran
over one period of time.

Write an account of what was accomplished across the whole period, grouped BY THEME rather
than by session. Work that spans several sessions is one story and should be told once:
"the live view gained adaptive retention, stall detection and deep links across three
sessions" beats three separate mentions of the live view.

Lead with what was actually delivered. Then anything notable across the period: recurring
problems, work that failed or was abandoned, threads left unfinished.

Do not restate each session in turn - the reader already has the per-session detail below
your section. Give them the shape of the period.

` + common

// combinePreamble introduces chunk summaries to the pass that combines them, so the model
// treats them as consecutive parts of one session rather than as separate sessions.
const combinePreamble = `The following are summaries of consecutive parts of ONE session,
in order. Combine them into a single account of what that session accomplished. Work
continued across the part boundaries, so do not report the same thing twice because it
appears in two parts.

`

// sessionInput assembles the root digest's summary and its subagents' for the final
// session pass.
func sessionInput(th *live.Thread, root string, subs []string) string {
	var b strings.Builder
	if th.Ask != "" {
		fmt.Fprintf(&b, "The session was asked to: %s\n\n", th.Ask)
	}
	b.WriteString("## What the main agent did\n")
	if root == "" {
		b.WriteString("(nothing was captured for the main agent; the work below was done by its sub-agents)\n")
	} else {
		b.WriteString(root + "\n")
	}

	var kept []string
	for _, s := range subs {
		if s != "" {
			kept = append(kept, s)
		}
	}
	if len(kept) > 0 {
		fmt.Fprintf(&b, "\n## Sub-agents it spawned (%d)\n\n%s\n", len(kept), joinBlank(kept))
	}
	return b.String()
}

// windowInput assembles every successful session summary for the roll-up pass.
func windowInput(done []Result) string {
	var b strings.Builder
	for i, s := range done {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "## Session: %s (%s to %s)\n%s\n",
			s.Name,
			s.First.Local().Format("2006-01-02 15:04"),
			s.Last.Local().Format("15:04"),
			s.Summary)
	}
	return b.String()
}
