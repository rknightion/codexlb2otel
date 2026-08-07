package main

import (
	"path/filepath"
	"testing"
)

const dir = "corpus/processed"

func find(t *testing.T, actions []action, name string) action {
	t.Helper()
	for _, a := range actions {
		if a.remote.Name == name {
			return a
		}
	}
	t.Fatalf("no action for %s", name)
	return action{}
}

// The three cases the sync has to tell apart, and the one that has actually bitten:
// codex-lb recreates an archive at the same path after a file is moved away, so a
// name-only comparison either keeps the wrong capture or never fetches the new one.
func TestPlan(t *testing.T) {
	local := []localFile{
		{Path: filepath.Join(dir, "2026-08-06T18.jsonl.gz"), Size: 100, FP: "fp-18a"},
		{Path: filepath.Join(dir, "2026-08-07T09.jsonl.gz"), Size: 200, FP: "fp-09"},
		{Path: filepath.Join(dir, "2026-08-07T10.jsonl.gz"), Size: 300, FP: "fp-10"},
	}
	remote := []remoteFile{
		// Same name, DIFFERENT capture - the file was moved away and recreated.
		{Name: "2026-08-06T18.jsonl.gz", Size: 90, FP: "fp-18b"},
		// Unchanged.
		{Name: "2026-08-07T09.jsonl.gz", Size: 200, FP: "fp-09"},
		// The hour codex-lb is still writing.
		{Name: "2026-08-07T10.jsonl.gz", Size: 500, FP: "fp-10"},
		// Never seen.
		{Name: "2026-08-07T11.jsonl.gz", Size: 400, FP: "fp-11"},
	}

	actions := plan(remote, local, dir)

	replaced := find(t, actions, "2026-08-06T18.jsonl.gz")
	if replaced.kind != "replaced" {
		t.Errorf("same name + different fingerprint = %q, want replaced", replaced.kind)
	}
	// Both captures must survive. Overwriting destroys an hour of traffic that exists
	// nowhere else; skipping means the new capture is never fetched at all.
	if want := filepath.Join(dir, "2026-08-06T18.gen2.jsonl.gz"); replaced.target != want {
		t.Errorf("replacement target = %q, want %q", replaced.target, want)
	}

	if got := find(t, actions, "2026-08-07T09.jsonl.gz"); got.kind != "current" {
		t.Errorf("identical file = %q, want current", got.kind)
	}

	grown := find(t, actions, "2026-08-07T10.jsonl.gz")
	if grown.kind != "grown" {
		t.Errorf("same fingerprint + larger = %q, want grown", grown.kind)
	}
	if grown.target != local[2].Path {
		t.Errorf("grown target = %q, want the existing file %q", grown.target, local[2].Path)
	}

	if got := find(t, actions, "2026-08-07T11.jsonl.gz"); got.kind != "new" {
		t.Errorf("unseen fingerprint = %q, want new", got.kind)
	}

	if n := len(pending(actions)); n != 3 {
		t.Errorf("%d files to fetch, want 3 (replaced, grown, new)", n)
	}
}

// A capture already held under a generation suffix must not be re-fetched forever.
// Identity is the fingerprint, so the name it happens to be stored under is
// irrelevant - which is the property that keeps repeated syncs idempotent.
func TestPlan_GenerationsAreNotRefetched(t *testing.T) {
	local := []localFile{
		{Path: filepath.Join(dir, "2026-08-06T18.jsonl.gz"), Size: 100, FP: "fp-18a"},
		{Path: filepath.Join(dir, "2026-08-06T18.gen2.jsonl.gz"), Size: 90, FP: "fp-18b"},
	}
	remote := []remoteFile{{Name: "2026-08-06T18.jsonl.gz", Size: 90, FP: "fp-18b"}}

	actions := plan(remote, local, dir)
	if got := actions[0]; got.kind != "current" {
		t.Fatalf("a capture already held as %s was planned as %q, want current",
			filepath.Base(local[1].Path), got.kind)
	}

	// A THIRD distinct capture at the same name takes the next free slot.
	remote = append(remote, remoteFile{Name: "2026-08-06T18.jsonl.gz", Size: 80, FP: "fp-18c"})
	actions = plan(remote, local, dir)
	if want := filepath.Join(dir, "2026-08-06T18.gen3.jsonl.gz"); actions[1].target != want {
		t.Errorf("third capture target = %q, want %q", actions[1].target, want)
	}
}

// An empty local corpus is the first-run case: everything is new, nothing collides.
func TestPlan_EmptyLocal(t *testing.T) {
	remote := []remoteFile{
		{Name: "2026-08-07T09.jsonl.gz", Size: 1, FP: "a"},
		{Name: "2026-08-07T10.jsonl.gz", Size: 2, FP: "b"},
	}
	actions := plan(remote, nil, dir)
	if len(pending(actions)) != 2 {
		t.Fatalf("%d of 2 remote files planned for fetch on an empty corpus", len(pending(actions)))
	}
	for _, a := range actions {
		if a.kind != "new" {
			t.Errorf("%s = %q, want new", a.remote.Name, a.kind)
		}
		if a.target != filepath.Join(dir, a.remote.Name) {
			t.Errorf("%s target = %q, want the plain name", a.remote.Name, a.target)
		}
	}
}

// bytesH gains a decimal at gigabyte scale precisely so a fetch of tens of megabytes
// is visible against a corpus already past 1GB - which is the case the totals exist
// for, and the one a single decimal rounds away.
func TestBytesH(t *testing.T) {
	for in, want := range map[int64]string{
		0:             "0B",
		999:           "999B",
		1 << 10:       "1.0KB",
		1536:          "1.5KB",
		15_900_000:    "15.2MB",
		1_331_000_000: "1.24GB",
		1_396_000_000: "1.30GB",
	} {
		if got := bytesH(in); got != want {
			t.Errorf("bytesH(%d) = %q, want %q", in, got, want)
		}
	}

	// The whole point: a 65MB fetch on top of a 1.2GB corpus must not round away.
	const corpus, fetch = 1_331_000_000, 65_300_000
	if before, after := bytesH(corpus), bytesH(corpus+fetch); before == after {
		t.Errorf("a %s fetch shows as %s -> %s; the totals hide the change they exist to show",
			bytesH(fetch), before, after)
	}
}

// A build-tag mistake would make diskFree silently report nothing on the platform
// this actually runs on, and the free-space column would just quietly vanish.
func TestDiskFree(t *testing.T) {
	n, ok := diskFree(t.TempDir())
	if !ok {
		t.Skip("diskFree unsupported on this platform")
	}
	if n <= 0 {
		t.Errorf("diskFree reported %d bytes free", n)
	}
}
