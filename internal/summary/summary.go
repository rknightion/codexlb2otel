// Package summary turns captured agent sessions into an account of what work they
// accomplished.
//
// It answers one question - what got DONE - and everything here is shaped by it. The
// digest sent to the model is conversation content only: prompts, assistant messages,
// inter-agent messages, tool calls and their results. No timing, token, cost, tier or
// rate-limit field is ever sent, because those are already exported to Grafana and
// dashboarded, and a language model restating them would be slower, dearer and wrong.
//
// # This is the project's only third-party egress
//
// Loki and the OTLP gateway are endpoints the operator chose. OpenRouter routes to
// whichever provider serves the model, so real prompts and real command output leave the
// operator's control. That is why the feature is off by default and why the routing
// preferences default to zero-data-retention and data_collection=deny rather than to
// OpenRouter's own defaults.
//
// # Shape
//
// Summarising is map-reduce over the thread forest that internal/live builds:
//
//  1. each subagent thread is digested and summarised alone;
//  2. a root's digest plus its subagents' summaries produce the session summary;
//  3. every session summary produces the window roll-up.
//
// A thread whose digest exceeds the budget is chunked at turn boundaries first, adding an
// inner map-reduce. All of it goes through Client, a one-method interface, so no test in
// this package makes a network call.
package summary

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rknightion/codexlb2otel/internal/live"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

// Client is the whole LLM surface this package needs.
//
// Narrow on purpose: the OpenRouter Go SDK is pre-1.0 and moving fast - 32 patch releases
// on v0.7 alone - so exactly one file in this repo imports it, and everything else,
// including every test, codes against these three arguments.
type Client interface {
	Complete(ctx context.Context, system, user string) (string, error)
}

// Options are the budgets and limits. They mirror config.Summarize, which the command
// converts; the package does not import config, so it stays usable from a test with no
// configuration at all.
type Options struct {
	MaxCharsPerSession    int
	MaxCharsPerToolInput  int
	MaxCharsPerToolOutput int
	// MaxCharsPerPrompt and MaxCharsPerMessage bound prose. Deliberately large - the
	// human's ask and the agent's own account of what it did are the highest-value
	// content in the capture, and clipping them to save room for console output would be
	// exactly the wrong trade.
	MaxCharsPerPrompt  int
	MaxCharsPerMessage int
	Concurrency        int
	// Progress is called as work completes. Nil is fine.
	//
	// It exists because a run of a hundred calls otherwise emits nothing at all between
	// starting and finishing, which is indistinguishable from a hang - and interrupting it
	// is what turns a working run into a report of nothing but cancellations.
	//
	// Called from every summarising goroutine, so an implementation must be safe for
	// concurrent use.
	Progress func(Event)
}

// Event is one completed piece of work.
type Event struct {
	// Calls is how many model calls have finished across the whole run.
	Calls int
	// Session names a session that has just finished. Empty on a bare call.
	Session string
	// Err is that session's outcome, and is only meaningful alongside Session.
	Err error
}

func (o *Options) setDefaults() {
	if o.MaxCharsPerSession <= 0 {
		o.MaxCharsPerSession = 1_500_000
	}
	if o.MaxCharsPerToolInput <= 0 {
		o.MaxCharsPerToolInput = 20_000
	}
	if o.MaxCharsPerToolOutput <= 0 {
		o.MaxCharsPerToolOutput = 2_000
	}
	if o.MaxCharsPerPrompt <= 0 {
		o.MaxCharsPerPrompt = 200_000
	}
	if o.MaxCharsPerMessage <= 0 {
		o.MaxCharsPerMessage = 200_000
	}
	if o.Concurrency <= 0 {
		o.Concurrency = 4
	}
}

// Source supplies the turns of a thread. live.Store satisfies it.
type Source interface {
	Turns(threadID string) []*turn.Turn
}

// Session is one root thread and its subagent subtree, selected for summarising.
type Session struct {
	Thread *live.Thread
}

// Result is one summarised session.
type Result struct {
	ThreadID string
	Name     string
	// First and Last are the session's REAL span, which may exceed the requested window:
	// a selected thread is read whole, because truncating a conversation at a clock
	// boundary summarises half a job.
	First, Last time.Time
	Orphaned    bool
	// Summary is the prose. Empty when Err is set.
	Summary string
	// Passes is how many chunks the root digest needed; >1 means the narrative is a
	// summary of summaries and the output says so.
	Passes int
	// Subagents is how many child threads were summarised into this.
	Subagents int
	Err       error
}

// Report is the whole run.
type Report struct {
	From, To time.Time
	Sessions []Result
	// Window is the themed roll-up across every session that succeeded.
	Window    string
	WindowErr error
	// Canceled marks a run stopped from outside - an interrupt, or the terminal going away.
	//
	// Recorded rather than inferred from the sessions, because every session that had not
	// started reports a bare context cancellation and a report full of those reads as a
	// broken tool rather than an interrupted one.
	Canceled bool
}

// Failed reports whether nothing at all was produced, which is the only case that should
// set a non-zero exit status. One session failing is a section that says so, not a run
// that failed.
func (r Report) Failed() bool {
	if len(r.Sessions) == 0 {
		return false
	}
	// A session with no error but no prose either produced nothing, so it does not count
	// as survival - otherwise a run that emitted an empty report would exit 0.
	for _, s := range r.Sessions {
		if s.Err == nil && s.Summary != "" {
			return r.WindowErr != nil
		}
	}
	return true
}

// Run summarises every session, then the window.
//
// Errors are collected onto their session rather than returned: one thread that fails to
// summarise must not discard the work of summarising the other eleven, which have already
// been paid for.
func Run(ctx context.Context, c Client, src Source, sessions []Session, o Options) Report {
	o.setDefaults()

	rep := Report{Sessions: make([]Result, len(sessions))}

	// Counting wraps the caller's client rather than threading a counter through every
	// summarising function, so no call site can forget to report one.
	counted := &tallyClient{inner: c, on: o.Progress}

	// ONE semaphore for the whole run, shared with every subagent pass. Giving each
	// session its own would multiply: a per-session limit of 4 across 4 concurrent
	// sessions is 16 in-flight calls, and a fan-out of 127 subagents would push that hard
	// enough to earn the rate limiting the backoff then has to absorb.
	sem := make(chan struct{}, o.Concurrency)

	var wg sync.WaitGroup
	for i, s := range sessions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res := summariseSession(ctx, counted, src, s, o, sem)
			rep.Sessions[i] = res
			counted.finished(res.Name, res.Err)
		}()
	}
	wg.Wait()

	// An interrupted run stops here. The roll-up would fail on the dead context anyway, and
	// summarising a window from the fraction of sessions that happened to finish would
	// describe the period as quieter than it was.
	if ctx.Err() != nil {
		rep.Canceled = true
		return rep
	}

	var done []Result
	for _, s := range rep.Sessions {
		if s.Err == nil && s.Summary != "" {
			done = append(done, s)
		}
	}
	if len(done) == 0 {
		return rep
	}
	rep.Window, rep.WindowErr = counted.Complete(ctx, windowPrompt, windowInput(done))
	return rep
}

// tallyClient reports every completed call to the progress callback.
type tallyClient struct {
	inner Client
	on    func(Event)
	n     atomic.Int64
}

func (c *tallyClient) Complete(ctx context.Context, system, user string) (string, error) {
	out, err := c.inner.Complete(ctx, system, user)
	n := c.n.Add(1)
	c.emit(Event{Calls: int(n)})
	return out, err
}

// finished reports a whole session landing, which is the progress that means something to
// a reader: a call count says the run is alive, a session name says what it has bought.
func (c *tallyClient) finished(name string, err error) {
	c.emit(Event{Calls: int(c.n.Load()), Session: name, Err: err})
}

func (c *tallyClient) emit(ev Event) {
	if c.on != nil {
		c.on(ev)
	}
}

// label is how a subagent thread is named to the reader and to the model. The task path is
// preferred where one was recovered: "maintenance_wiring" identifies the work, while the
// thread name is often the first line of its prompt.
func label(t *live.Thread) string {
	if t.TaskPath != "" {
		return t.TaskPath
	}
	return t.Name
}

// summariseSession runs the two-stage map-reduce over one root and its subtree.
func summariseSession(ctx context.Context, c Client, src Source, s Session, o Options, sem chan struct{}) Result {
	th := s.Thread
	res := Result{
		ThreadID: th.ThreadID,
		Name:     th.Name,
		First:    th.FirstSeen,
		Last:     th.LastSeen,
		Orphaned: th.Orphaned,
	}

	kids := descendants(th)
	res.Subagents = len(kids)

	// Subagents first and concurrently - they are independent, and their summaries are
	// input to the root's.
	subs := make([]string, len(kids))
	var wg sync.WaitGroup
	for i, k := range kids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			d := Build(src.Turns(k.ThreadID), o)
			text, err := summariseDigest(ctx, c, subagentPrompt, d)
			name := label(k)
			switch {
			case err != nil:
				// A subagent that could not be summarised is reported to the parent pass
				// as unknown rather than dropped. Silently omitting it would make the
				// session summary claim work happened with fewer agents than it did.
				subs[i] = fmt.Sprintf("### subagent %s\n(could not be summarised: %v)\n", name, err)
			case text != "":
				subs[i] = fmt.Sprintf("### subagent %s\n%s\n", name, text)
			}
		}()
	}
	wg.Wait()

	root := Build(src.Turns(th.ThreadID), o)
	res.Passes = root.Passes()
	if root.Chars == 0 && len(kids) == 0 {
		res.Err = errors.New("no conversation content captured for this session")
		return res
	}

	sem <- struct{}{}
	rootText, err := summariseDigest(ctx, c, sessionPrompt, root)
	<-sem
	if err != nil {
		res.Err = err
		return res
	}

	if len(kids) == 0 {
		res.Summary = rootText
		return res
	}
	sem <- struct{}{}
	res.Summary, res.Err = c.Complete(ctx, sessionPrompt, sessionInput(th, rootText, subs))
	<-sem
	return res
}

// summariseDigest summarises one digest, collapsing chunks when there is more than one.
func summariseDigest(ctx context.Context, c Client, system string, d Digest) (string, error) {
	switch len(d.Chunks) {
	case 0:
		return "", nil
	case 1:
		return c.Complete(ctx, system, d.Chunks[0])
	}

	// Sequential rather than concurrent: chunks are consecutive parts of one
	// conversation, and the calls are already bounded by the caller's semaphore.
	parts := make([]string, 0, len(d.Chunks))
	for i, ch := range d.Chunks {
		text, err := c.Complete(ctx, chunkPrompt, fmt.Sprintf("Part %d of %d.\n\n%s", i+1, len(d.Chunks), ch))
		if err != nil {
			return "", fmt.Errorf("part %d of %d: %w", i+1, len(d.Chunks), err)
		}
		parts = append(parts, fmt.Sprintf("## Part %d of %d\n%s", i+1, len(d.Chunks), text))
	}
	return c.Complete(ctx, system, combinePreamble+joinBlank(parts))
}

// descendants flattens a thread's subagent subtree, deepest last, in a stable order.
func descendants(th *live.Thread) []*live.Thread {
	var out []*live.Thread
	var walk func(*live.Thread)
	walk = func(t *live.Thread) {
		for _, c := range t.Children {
			out = append(out, c)
			walk(c)
		}
	}
	walk(th)
	sort.SliceStable(out, func(i, j int) bool { return out[i].FirstSeen.Before(out[j].FirstSeen) })
	return out
}

func joinBlank(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += "\n\n"
		}
		out += s
	}
	return out
}
