// Command clbprobe answers one question fast: has the archive format changed?
//
// It reads the .gz files in place - no decompression to disk - and reduces them to
// a content-free signature that can be committed. Comparing a fresh capture against
// that baseline reports new event types, new fields, new enum values, and the one
// failure that silently destroys data: an existing field that starts arriving as a
// second JSON type, which makes Go's decoder abandon the whole event.
//
// The default is a FULL scan. Sampling exists (-sampled) and resynchronises each
// window onto a gzip member boundary, which is only possible because codex-lb closes
// a member per batch - but a full pass over 1.6GB is about 30 seconds on a laptop,
// and the trade sampling makes is not worth that: it detects drift in common shapes
// and proves NOTHING about rare ones. The shapes that matter here are rare by nature
// (20 websocket control frames in 1.5M lines; one connection-limit error in a day),
// so a sampled scan is most likely to miss exactly what the tool exists to find.
//
//	clbprobe corpus/processed                  # drift check against the baseline
//	clbprobe -update corpus/processed          # accept current shape as the baseline
//	clbprobe -sampled corpus/processed         # fast, and cannot prove absence
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/rknightion/codexlb2otel/internal/frame"
	"github.com/rknightion/codexlb2otel/internal/profile"
)

// DefaultBaseline is the committed signature. It is content-free by construction -
// see profile.Signature - which is what makes it safe to keep in a repository whose
// corpus never can be.
const DefaultBaseline = "corpus.sig.json"

// Exit codes. Distinct so a scheduled run can act on the difference.
const (
	exitOK      = 0
	exitDrift   = 1
	exitError   = 2
	exitNoInput = 3
)

func main() {
	os.Exit(run())
}

func run() int {
	var (
		baseline = flag.String("baseline", DefaultBaseline, "committed signature to compare against")
		update   = flag.String("update", "", "write the signature to this path instead of comparing (\"-\" for stdout)")
		sampled  = flag.Bool("sampled", false, "sample windows instead of reading every byte; faster, but cannot prove a shape is absent")
		windows  = flag.Int("windows", 24, "sampled windows per file")
		window   = flag.Int("window-bytes", 1<<20, "compressed bytes per sampled window")
		failOn   = flag.String("fail-on", "new", "exit non-zero at this severity or above: info|new|breaking")
		asJSON   = flag.Bool("json", false, "emit findings as JSON")
		quiet    = flag.Bool("quiet", false, "suppress the shape summary, print findings only")
	)
	flag.Usage = usage
	flag.Parse()

	// -update with the default value means "update the default baseline".
	updating := isFlagSet("update")
	updatePath := *update
	if updating && updatePath == "" {
		updatePath = *baseline
	}

	threshold, ok := profile.ParseSeverity(*failOn)
	if !ok {
		fmt.Fprintf(os.Stderr, "clbprobe: -fail-on must be info, new or breaking (got %q)\n", *failOn)
		return exitError
	}

	files, err := expand(flag.Args())
	if err != nil {
		fmt.Fprintf(os.Stderr, "clbprobe: %v\n", err)
		return exitError
	}
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "clbprobe: no .jsonl.gz files found")
		usage()
		return exitNoInput
	}

	total, cov, failures := scanAll(files, !*sampled, *windows, *window)
	for _, e := range failures {
		fmt.Fprintf(os.Stderr, "clbprobe: %v\n", e)
	}
	sig := total.Signature(cov)

	if !*quiet {
		// -json makes stdout a machine channel. Printing the human summary in front
		// of the document leaves stdout unparseable - `clbprobe -json | jq` fails on
		// line 1 - so under -json the summary goes to stderr and a human still sees it.
		out := io.Writer(os.Stdout)
		if *asJSON {
			out = os.Stderr
		}
		summarise(out, total, cov, files)
	}

	if updating {
		if err := writeSignature(updatePath, sig); err != nil {
			fmt.Fprintf(os.Stderr, "clbprobe: %v\n", err)
			return exitError
		}
		if updatePath != "-" {
			fmt.Fprintf(os.Stderr, "wrote %s\n", updatePath)
			if cov.Sampled {
				fmt.Fprintln(os.Stderr,
					"WARNING: baseline written from a SAMPLED scan. It will not contain rare shapes, "+
						"so the next run may report them as new. Drop -sampled.")
			}
		}
		return exitOK
	}

	base, err := readSignature(*baseline)
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(os.Stderr,
			"clbprobe: no baseline at %s.\nRun `clbprobe -update %s` once to record the current shape.\n",
			*baseline, strings.Join(flag.Args(), " "))
		return exitNoInput
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "clbprobe: %v\n", err)
		return exitError
	}

	findings := profile.Diff(base, sig)
	if *asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"coverage": cov,
			"findings": findings,
		})
	} else {
		printFindings(os.Stdout, findings, cov)
	}

	if len(failures) > 0 {
		return exitError
	}
	for _, f := range findings {
		if f.Severity >= threshold {
			return exitDrift
		}
	}
	return exitOK
}

// scanAll profiles every file concurrently and merges the results.
func scanAll(files []string, full bool, windows, window int) (*profile.Profile, profile.Coverage, []error) {
	total := profile.New()
	var cov profile.Coverage
	var errs []error
	var mu sync.Mutex

	sem := make(chan struct{}, max(1, runtime.NumCPU()/2))
	var wg sync.WaitGroup
	for _, path := range files {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			var (
				p *profile.Profile
				c profile.Coverage
			)
			var err error
			if full {
				p, c, err = profile.ScanFile(path, profile.DefaultChunk)
			} else {
				p, c, err = profile.ScanSampled(path, windows, window)
			}

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", filepath.Base(path), err))
				return
			}
			total.Merge(p)
			cov.Files += c.Files
			cov.FileBytes += c.FileBytes
			cov.ReadBytes += c.ReadBytes
			cov.Windows += c.Windows
			cov.Resynced += c.Resynced
			cov.DeadWindows += c.DeadWindows
			cov.Sampled = cov.Sampled || c.Sampled
		}(path)
	}
	wg.Wait()
	return total, cov, errs
}

// expand turns arguments into a file list, walking directories so that a corpus
// organised into per-source subdirectories can be passed as one path.
func expand(args []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, a := range args {
		st, err := os.Stat(a)
		if err != nil {
			return nil, err
		}
		if !st.IsDir() {
			if !seen[a] {
				seen[a] = true
				out = append(out, a)
			}
			continue
		}
		err = filepath.WalkDir(a, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && strings.HasSuffix(p, ".jsonl.gz") && !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}

// summarise prints the shape a human reads before deciding whether to dig in with
// clbprofile.
func summarise(w io.Writer, p *profile.Profile, cov profile.Coverage, files []string) {
	mode := "FULL"
	if cov.Sampled {
		mode = "SAMPLED"
	}
	fmt.Fprintf(w, "%s scan: %d file(s), read %s of %s (%.1f%%)\n",
		mode, len(files), bytesH(cov.ReadBytes), bytesH(cov.FileBytes), 100*cov.Fraction())
	if cov.Sampled {
		fmt.Fprintf(w, "  %d windows, %d resynced onto a member boundary, %d found no member\n",
			cov.Windows, cov.Resynced, cov.DeadWindows)
	}
	fmt.Fprintf(w, "  %d lines / %d members / %s decompressed, %d undecodable\n",
		p.Lines, p.Members, bytesH(p.Bytes), p.Bad)

	fmt.Fprintf(w, "\npayload framing: %s\n", inlineCounts(p.PayloadFraming))
	if p.PayloadUnparsed > 0 {
		fmt.Fprintf(w, "  %d payloads did not parse as JSON\n", p.PayloadUnparsed)
	}
	// Reported separately because they are not protocol payloads at all: a control
	// frame's text is a close reason, and counting it as unparseable reads as damage.
	if p.TransportFrames > 0 {
		fmt.Fprintf(w, "  %d websocket control frames (close/error; text is a reason string, not JSON)\n",
			p.TransportFrames)
	}

	// Event types the typed decoders do not name are data on the floor. This is the
	// check that found the entirely-unhandled `error` event.
	known := frame.KnownEventTypes()
	type ev struct {
		name string
		n    int64
	}
	var unknown []ev
	for name, ep := range p.Events {
		if strings.HasPrefix(name, "<") {
			continue
		}
		if !known[name] {
			unknown = append(unknown, ev{name, ep.Count})
		}
	}
	sort.Slice(unknown, func(i, j int) bool { return unknown[i].n > unknown[j].n })
	fmt.Fprintf(w, "\nevent types: %d seen, %d not named by internal/frame\n", len(p.Events), len(unknown))
	for _, e := range unknown {
		fmt.Fprintf(w, "  UNHANDLED  %-46s %9d\n", e.name, e.n)
	}
	fmt.Fprintln(w)
}

func printFindings(w io.Writer, f []profile.Finding, cov profile.Coverage) {
	if len(f) == 0 {
		fmt.Fprintln(w, "no drift against the baseline.")
		if cov.Sampled {
			fmt.Fprintln(w, "(sampled scan - this does not prove a rare shape is absent; use -full)")
		}
		return
	}
	var breaking, isNew, info int
	for _, x := range f {
		switch x.Severity {
		case profile.SevBreaking:
			breaking++
		case profile.SevNew:
			isNew++
		default:
			info++
		}
	}
	fmt.Fprintf(w, "%d finding(s): %d breaking, %d new, %d info\n\n", len(f), breaking, isNew, info)
	for _, x := range f {
		fmt.Fprintf(w, "  %-8s %-26s %s\n", x.Level, x.Kind, x.Subject)
		if x.Detail != "" {
			fmt.Fprintf(w, "           %s\n", x.Detail)
		}
	}
	if cov.Sampled {
		fmt.Fprintln(w, "\n(sampled scan - 'absent' findings are usually the sample, not a real removal)")
	}
}

func readSignature(path string) (*profile.Signature, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s profile.Signature
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &s, nil
}

func writeSignature(path string, s *profile.Signature) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if path == "-" {
		_, err := os.Stdout.Write(b)
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func isFlagSet(name string) bool {
	found := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

func inlineCounts(m map[string]int64) string {
	type kv struct {
		k string
		v int64
	}
	out := make([]kv, 0, len(m))
	for k, v := range m {
		out = append(out, kv{k, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].v > out[j].v })
	parts := make([]string, 0, len(out))
	for _, e := range out {
		parts = append(parts, fmt.Sprintf("%s=%d", e.k, e.v))
	}
	if len(parts) == 0 {
		return "(none)"
	}
	return strings.Join(parts, " ")
}

func bytesH(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGTP"[exp])
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: clbprobe [flags] <path>...

Paths may be archive files or directories; directories are walked for *.jsonl.gz.

  clbprobe corpus/                          full drift check against `+DefaultBaseline+`
  clbprobe -sampled corpus/processed        fast; cannot prove a shape is absent
  clbprobe -update corpus/                  accept the current shape as baseline

exit: 0 clean, 1 drift at or above -fail-on, 2 error, 3 nothing to do

flags:
`)
	flag.PrintDefaults()
}
