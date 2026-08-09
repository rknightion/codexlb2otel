// clbsum reports what work agent sessions actually accomplished over a chosen window.
//
// It is the semantic counterpart to everything else here: Grafana already knows what the
// sessions COST and how long they took, and Loki holds every word they said. Neither
// answers "what did I get done this afternoon". This picks sessions from a time window and
// has a language model report the work.
//
// It is also the only part of this project that sends captured content to a third party,
// which is why it does nothing at all until summarize.enabled is set.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/rknightion/codexlb2otel/internal/config"
	"github.com/rknightion/codexlb2otel/internal/live"
	"github.com/rknightion/codexlb2otel/internal/summary"
)

// defaultConfigPath is looked for in the working directory and is allowed to be absent -
// clbsum is useful against a corpus with no config at all.
const defaultConfigPath = "config.yaml"

func main() { os.Exit(run()) }

func run() int {
	var (
		dir     = flag.String("corpus", "", "corpus directory to read (default: archive.dir from the config)")
		cfgPath = flag.String("config", defaultConfigPath, "config file; a missing one just means defaults")
		since   = flag.Duration("since", 0, "summarise sessions active in the last duration, e.g. 4h")
		fromS   = flag.String("from", "", "window start, RFC3339 or 2006-01-02T15:04 (local)")
		toS     = flag.String("to", "", "window end, RFC3339 or 2006-01-02T15:04 (local); defaults to now")
		pick    = flag.String("pick", "", "comma-separated row numbers to summarise, skipping the picker")
		all     = flag.Bool("all", false, "summarise every session in the window, skipping the picker")
		list    = flag.Bool("list", false, "list the sessions in the window and exit")
		dry     = flag.Bool("dry-run", false, "report digest sizes and the calls that would be made, then exit")
		out     = flag.String("out", "-", "write the report to a file; - is stdout")
		model   = flag.String("model", "", "override summarize.model")
		slop    = flag.Duration("slop", defaultSlop, "how far outside the window to read, so a session straddling the edge is not cut short")
	)
	flag.Usage = usage
	flag.Parse()

	if *pick != "" && *all {
		fmt.Fprintln(os.Stderr, "clbsum: -pick and -all are mutually exclusive")
		return 2
	}

	from, to, err := window(*since, *fromS, *toS)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clbsum: %v\n", err)
		return 2
	}

	// LoadOverlay, not Load: Load enforces what the SERVICE needs - an enabled,
	// credentialed sink - and clbsum pushes to no sink. Requiring that would make
	// `clbsum -list` on a laptop demand Loki credentials it never uses.
	cfg, err := config.LoadOverlay(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clbsum: %v\n", err)
		return 2
	}
	if *model != "" {
		cfg.Summarize.Model = *model
	}
	corpus := *dir
	if corpus == "" {
		corpus = cfg.Archive.Dir
	}

	// Ctrl-C aborts in-flight calls rather than killing the process, so whatever already
	// completed is still printed. A summarising run costs real money; discarding eleven
	// finished sessions because the twelfth was interrupted would be the wrong trade.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, snap, err := load(ctx, os.Stderr, corpus, from, to, *slop)
	switch {
	case errors.Is(err, errNoArchives):
		fmt.Fprintf(os.Stderr, "clbsum: no archives cover %s to %s in %s\n",
			from.Format("2006-01-02 15:04"), to.Format("2006-01-02 15:04"), corpus)
		return 0
	case err != nil:
		fmt.Fprintf(os.Stderr, "clbsum: %v\n", err)
		return 2
	}
	rows := selectable(store, snap, from, to)
	if len(rows) == 0 {
		fmt.Fprintf(os.Stderr, "clbsum: no sessions in %s to %s\n",
			from.Format("2006-01-02 15:04"), to.Format("2006-01-02 15:04"))
		return 0
	}

	if *list {
		printList(os.Stdout, rows)
		return 0
	}

	chosen, err := choose(rows, *pick, *all)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clbsum: %v\n", err)
		return 2
	}
	if len(chosen) == 0 {
		fmt.Fprintln(os.Stderr, "clbsum: nothing selected")
		return 0
	}

	opts := summary.Options{
		MaxCharsPerSession:    cfg.Summarize.MaxCharsPerSession,
		MaxCharsPerToolInput:  cfg.Summarize.MaxCharsPerToolInput,
		MaxCharsPerToolOutput: cfg.Summarize.MaxCharsPerToolOutput,
		Concurrency:           cfg.Summarize.Concurrency,
	}

	if *dry {
		printDryRun(os.Stdout, store, chosen, opts, cfg.Summarize.Model)
		return 0
	}

	// Checked here rather than at load time so -list and -dry-run work on a machine that
	// has never had an OpenRouter key - inspecting what WOULD be sent should not require
	// the credential that sends it.
	if !cfg.Summarize.Enabled {
		fmt.Fprintln(os.Stderr, "clbsum: summarize.enabled is false.\n"+
			"This sends conversation content - prompts, assistant messages and command output - to\n"+
			"OpenRouter, which is outside your infrastructure. Set summarize.enabled: true to allow it.\n"+
			"Use -list or -dry-run to inspect what would be sent without sending it.")
		return 2
	}
	if err := cfg.Summarize.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "clbsum: %v\n", err)
		return 2
	}
	key, err := cfg.Summarize.APIKey.Resolve()
	if err != nil {
		fmt.Fprintf(os.Stderr, "clbsum: summarize.api_key: %v\n", err)
		return 2
	}
	client, err := summary.NewOpenRouter(summary.OpenRouterOptions{
		APIKey:         key,
		Model:          cfg.Summarize.Model,
		BaseURL:        cfg.Summarize.BaseURL,
		Timeout:        cfg.Summarize.Timeout,
		MaxRetries:     cfg.Summarize.MaxRetries,
		ZDR:            cfg.Summarize.ZDR,
		DataCollection: cfg.Summarize.DataCollection,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "clbsum: %v\n", err)
		return 2
	}

	sessions := make([]summary.Session, len(chosen))
	for i, th := range chosen {
		sessions[i] = summary.Session{Thread: th}
	}
	fmt.Fprintf(os.Stderr, "summarising %s with %s...\n",
		plural(len(sessions), "session", "sessions"), cfg.Summarize.Model)

	rep := summary.Run(ctx, client, store, sessions, opts)
	rep.From, rep.To = from, to

	w := os.Stdout
	if *out != "-" {
		f, err := os.Create(*out)
		if err != nil {
			fmt.Fprintf(os.Stderr, "clbsum: %v\n", err)
			return 2
		}
		defer f.Close()
		w = f
	}
	if err := summary.Render(w, rep); err != nil {
		fmt.Fprintf(os.Stderr, "clbsum: %v\n", err)
		return 2
	}
	if *out != "-" {
		fmt.Fprintf(os.Stderr, "wrote %s\n", *out)
	}

	if rep.Failed() {
		return 1
	}
	return 0
}

// window resolves the flags into a concrete span.
func window(since time.Duration, fromS, toS string) (time.Time, time.Time, error) {
	now := time.Now()
	switch {
	case since > 0 && fromS != "":
		return time.Time{}, time.Time{}, fmt.Errorf("-since and -from are mutually exclusive")
	case since > 0:
		return now.Add(-since), now, nil
	case fromS == "":
		return time.Time{}, time.Time{}, fmt.Errorf("give a window: -since 4h, or -from and -to")
	}

	from, err := parseWhen(fromS)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("-from %q: %w", fromS, err)
	}
	to := now
	if toS != "" {
		if to, err = parseWhen(toS); err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("-to %q: %w", toS, err)
		}
	}
	if !to.After(from) {
		return time.Time{}, time.Time{}, fmt.Errorf("-to must be after -from")
	}
	return from, to, nil
}

// choose resolves the selection, interactively or otherwise.
func choose(rows []*live.Thread, pick string, all bool) ([]*live.Thread, error) {
	if all {
		return rows, nil
	}
	if pick != "" {
		return byNumber(rows, pick)
	}

	m, err := tea.NewProgram(newPicker(rows)).Run()
	if err != nil {
		// The case that actually happens: a cron job or a pipeline, where there is no
		// terminal to draw on. Naming the two flags that work there turns a dead end into
		// a fix.
		return nil, fmt.Errorf("picker: %w\n"+
			"There is no terminal to draw on. Use -all, or -list to get the row numbers for -pick", err)
	}
	return m.(*picker).selected(), nil
}

// byNumber resolves the 1-based row numbers -pick takes, the same numbers -list prints.
func byNumber(rows []*live.Thread, pick string) ([]*live.Thread, error) {
	var out []*live.Thread
	seen := map[int]bool{}
	for _, f := range strings.Split(pick, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		n, err := strconv.Atoi(f)
		if err != nil {
			return nil, fmt.Errorf("-pick %q: not a number", f)
		}
		if n < 1 || n > len(rows) {
			return nil, fmt.Errorf("-pick %d: out of range, there are %d sessions", n, len(rows))
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, rows[n-1])
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("-pick selected nothing")
	}
	return out, nil
}

func printList(w *os.File, rows []*live.Thread) {
	for i, th := range rows {
		fmt.Fprintf(w, "%3d  %s  %-52s  %s\n",
			i+1, th.LastSeen.Local().Format("2006-01-02 15:04"), truncate(th.Name, 52), meta(th))
	}
}

// printDryRun reports what would be sent without sending it.
//
// The call count is the number that decides whether to press on, so it counts what will
// actually be spent: one call per chunk of every digest, root and sub-agent alike, plus a
// combining call wherever a digest chunked, plus the session pass wherever sub-agents
// contributed, plus the window roll-up.
func printDryRun(w *os.File, store *live.Store, chosen []*live.Thread, opts summary.Options, model string) {
	total, calls := 0, 0

	// callsFor is a digest's own cost: one per chunk, plus a combine when it chunked.
	callsFor := func(d summary.Digest) int {
		if d.Passes() > 1 {
			return d.Passes() + 1
		}
		return d.Passes()
	}

	for i, th := range chosen {
		d := summary.Build(store.Turns(th.ThreadID), opts)
		kids := flatten(th)
		total += d.Chars
		calls += callsFor(d)
		if len(kids) > 0 {
			calls++ // the pass that folds the sub-agent summaries into the session
		}

		note := ""
		if d.Passes() > 1 {
			note = fmt.Sprintf("  CHUNKED into %d", d.Passes())
		}
		fmt.Fprintf(w, "%3d  %-52s  %9s  %s%s\n",
			i+1, truncate(th.Name, 52), bytesish(d.Chars), plural(len(kids), "sub-agent", "sub-agents"), note)

		for _, k := range kids {
			kd := summary.Build(store.Turns(k.ThreadID), opts)
			total += kd.Chars
			calls += callsFor(kd)

			kn := ""
			if kd.Passes() > 1 {
				kn = fmt.Sprintf("  CHUNKED into %d", kd.Passes())
			}
			fmt.Fprintf(w, "       └ %-48s  %9s%s\n", truncate(k.Name, 48), bytesish(kd.Chars), kn)
		}
	}
	calls++ // the window roll-up

	fmt.Fprintf(w, "\n%s of digest across %s, about %d model calls to %s\n",
		bytesish(total), plural(len(chosen), "session", "sessions"), calls, model)
	fmt.Fprintln(w, "\nNothing was sent. Drop -dry-run to summarise.")
}

func flatten(th *live.Thread) []*live.Thread {
	var out []*live.Thread
	for _, c := range th.Children {
		out = append(out, c)
		out = append(out, flatten(c)...)
	}
	return out
}

func bytesish(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `clbsum - what work did the agents actually get done?

Picks agent sessions from a time window and has a language model report what each one
accomplished, plus a roll-up across the whole window.

  clbsum -since 4h                        pick interactively from the last four hours
  clbsum -since 24h -all -out day.md      everything today, written to a file
  clbsum -from 2026-08-08T09:00 -to 2026-08-08T18:00
  clbsum -since 4h -list                  just list the sessions
  clbsum -since 4h -all -dry-run          what would be sent, without sending it
  clbsum -since 4h -pick 1,3,7            skip the picker

In the picker: space selects, a toggles all, enter summarises, q cancels.

This sends conversation content to OpenRouter, outside your infrastructure, so it does
nothing until summarize.enabled is true in the config. -list and -dry-run need no key.

`)
	flag.PrintDefaults()
}
