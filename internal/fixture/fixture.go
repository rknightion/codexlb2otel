// Package fixture locates the captured archives the corpus tests run against.
//
// The corpus is never committed - it holds full conversation content - so it is a
// local directory that comes and goes. That created a specific failure worth
// designing against: every corpus test used to resolve a hardcoded path and call
// t.Skip when it was missing, so reorganising the directory turned seventeen tests
// into silent skips while `go test ./...` still printed ok for every package. The
// suite reported green while asserting nothing.
//
// Two rules follow. Fixtures are DISCOVERED rather than named, so moving or
// renaming files cannot detach a test from its data. And a missing corpus is FATAL
// by default: opting out is an explicit CLB_NO_CORPUS=1, which is what CI sets,
// where the absence is a known fact rather than an accident.
package fixture

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// EnvDir overrides where the corpus lives. Default is <repo>/corpus.
const EnvDir = "CLB_CORPUS"

// EnvOptional makes an absent corpus a skip instead of a failure. Set it in CI,
// which has no corpus by design, and nowhere else.
const EnvOptional = "CLB_NO_CORPUS"

// Ext is the archive extension codex-lb writes.
const Ext = ".jsonl.gz"

// Root returns the corpus directory.
func Root(tb testing.TB) string {
	tb.Helper()
	if d := os.Getenv(EnvDir); d != "" {
		return d
	}
	return filepath.Join(repoRoot(tb), "corpus")
}

// Files returns every archive under the corpus, SMALLEST FIRST.
//
// Size order is deliberate: a test that only needs "some real traffic" gets the
// cheapest files, so the suite stays fast as the corpus grows. Tests needing a
// specific property should walk this list and stop once they have it, rather than
// naming a file that will be rotated away.
func Files(tb testing.TB) []string {
	tb.Helper()
	root := Root(tb)

	type entry struct {
		path string
		size int64
	}
	var found []entry
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, Ext) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		found = append(found, entry{p, info.Size()})
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		tb.Fatalf("walking corpus %s: %v", root, err)
	}

	if len(found) == 0 {
		if os.Getenv(EnvOptional) != "" {
			tb.Skipf("no corpus under %s and %s is set", root, EnvOptional)
		}
		tb.Fatalf("no %s files under %s.\n"+
			"Drop captured archive hours there (source-tagged subdirectories are fine, they are\n"+
			"walked recursively), point %s elsewhere, or set %s=1 to accept running without them.",
			Ext, root, EnvDir, EnvOptional)
	}

	sort.Slice(found, func(i, j int) bool {
		if found[i].size != found[j].size {
			return found[i].size < found[j].size
		}
		return found[i].path < found[j].path
	})
	out := make([]string, len(found))
	for i, e := range found {
		out[i] = e.path
	}
	return out
}

// Any returns the n cheapest archives, ordered CHRONOLOGICALLY.
//
// The two orderings do different jobs and both are needed. Size picks which files to
// read, so a test costs the least it can. Time decides what order to feed them in,
// because the reducer diffs cumulative per-thread counters and a thread's frames
// arriving out of order produce deltas that are wrong rather than merely different -
// which is precisely what the watcher never does in production. Feeding by size once
// made a restart test report a bogus mismatch on a single turn.
//
// The archive filename is the capture hour, so name order is time order.
func Any(tb testing.TB, n int) []string {
	tb.Helper()
	all := Files(tb)
	if len(all) < n {
		tb.Fatalf("corpus has %d archives under %s, test needs %d", len(all), Root(tb), n)
	}
	out := append([]string(nil), all[:n]...)
	sort.Slice(out, func(i, j int) bool {
		bi, bj := filepath.Base(out[i]), filepath.Base(out[j])
		if bi != bj {
			return bi < bj
		}
		return out[i] < out[j]
	})
	return out
}

// Load reads one archive.
func Load(tb testing.TB, path string) []byte {
	tb.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		tb.Fatalf("reading fixture: %v", err)
	}
	return b
}

// Name is a short label for test output.
func Name(path string) string {
	return filepath.Join(filepath.Base(filepath.Dir(path)), filepath.Base(path))
}

// repoRoot walks up to the directory holding go.mod, so tests resolve the corpus
// identically whichever package directory they run from.
func repoRoot(tb testing.TB) string {
	tb.Helper()
	dir, err := os.Getwd()
	if err != nil {
		tb.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			tb.Fatal("no go.mod found above the working directory")
		}
		dir = parent
	}
}
