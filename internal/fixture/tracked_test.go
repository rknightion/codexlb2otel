package fixture

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// leakySuffixes are file shapes that carry captured conversation content. The
// archive holds real prompts, tool output and assistant messages, so one of these
// reaching git is a personal-data leak that stays in history after any later
// deletion - and this repository is intended to go public.
//
// .gitignore already covers them. This test exists because .gitignore is not a
// guarantee: `git add -f`, a rename into a tracked path, or an editor plugin all
// bypass it silently. A gate that fails is the difference between catching that
// and finding out after the push.
var leakySuffixes = []string{".jsonl", ".jsonl.gz", ".jsonl.zst", ".gz", ".zst"}

// allowedTracked are paths matching a leaky suffix that are nonetheless fine.
// Keep this empty unless a fixture is provably synthetic.
var allowedTracked = map[string]bool{}

func TestNoArchivesAreTracked(t *testing.T) {
	root := repoRoot(t)
	out, err := exec.Command("git", "-C", root, "ls-files", "-z").Output()
	if err != nil {
		t.Skipf("git not available or not a repository: %v", err)
	}

	var offenders []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f == "" || allowedTracked[f] {
			continue
		}
		for _, s := range leakySuffixes {
			if strings.HasSuffix(f, s) {
				offenders = append(offenders, f)
				break
			}
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("git is tracking %d capture-shaped file(s) - these carry conversation content "+
			"and must never enter history:\n  %s\n\nRun: git rm --cached <path>",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

// The corpus must also be ignored, not merely absent. An empty corpus directory
// today says nothing about what happens when files land in it tomorrow.
func TestCorpusDirectoryIsIgnored(t *testing.T) {
	root := repoRoot(t)
	probe := filepath.Join("corpus", "probe-source", "2026-01-01T00"+Ext)
	cmd := exec.Command("git", "-C", root, "check-ignore", "-q", probe)
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s is NOT gitignored (git check-ignore exited %v). "+
			"Captured archives dropped there would be committable.", probe, err)
	}
}
