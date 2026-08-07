// Command clbsync pulls new conversation archives down from the codex-lb host.
//
// The hard part is not copying files, it is deciding which ones are new. Matching
// by NAME alone is wrong, and provably so: codex-lb opens the archive
// O_APPEND|O_CREAT per batch, so moving a file away makes it recreate the same path
// from scratch. 2026-08-06T18.jsonl.gz has already existed as two entirely
// unrelated captures. A name-only sync keeps whichever it saw first and silently
// never fetches the second.
//
// Files are therefore identified by a fingerprint of their first bytes, which is
// stable as a file grows and changes when it is recreated. That distinguishes the
// three cases that matter:
//
//	new         no local file has this fingerprint          -> fetch
//	grown       same fingerprint, remote is larger          -> refresh (the live hour)
//	replaced    the name exists locally, fingerprint differs -> fetch alongside, keep both
//
//	clbsync                 # pull anything new, then offer to probe it
//	clbsync -dry-run        # say what it would do
//	clbsync -yes            # no prompts, run the probe afterwards
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// fingerprintBytes is how much of a file's head identifies it. Archive members are
// ~1KB, so 64KB spans dozens of them - far past any chance of two distinct captures
// agreeing by accident, and cheap to read over ssh.
const fingerprintBytes = 64 << 10

// archiveExt is the suffix codex-lb writes. Anything else in the directory is
// ignored rather than guessed at.
const archiveExt = ".jsonl.gz"

type remoteFile struct {
	Name string
	Size int64
	FP   string
}

type localFile struct {
	Path string
	Size int64
	FP   string
}

// action is what the sync decided to do about one remote file.
type action struct {
	remote remoteFile
	kind   string // new | grown | replaced | current
	target string // local path to write
	from   int64  // existing local size, for grown/replaced reporting
}

func main() {
	os.Exit(run())
}

func run() int {
	var (
		host      = flag.String("host", "camden", "ssh host holding the archives")
		remoteDir = flag.String("remote-dir", "/opt/codex-lb/data/codex-lb/conversation-archive", "archive directory on the host")
		localDir  = flag.String("local", filepath.Join("corpus", "processed"), "local corpus directory")
		dryRun    = flag.Bool("dry-run", false, "report what would be fetched and stop")
		yes       = flag.Bool("yes", false, "do not prompt; fetch and run the probe")
		noProbe   = flag.Bool("no-probe", false, "never offer to run clbprobe afterwards")
	)
	flag.Usage = usage
	flag.Parse()

	remote, err := listRemote(*host, *remoteDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clbsync: listing %s:%s: %v\n", *host, *remoteDir, err)
		return 2
	}
	if len(remote) == 0 {
		fmt.Fprintf(os.Stderr, "clbsync: no %s files in %s:%s\n", archiveExt, *host, *remoteDir)
		return 3
	}

	local, err := listLocal(*localDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clbsync: reading %s: %v\n", *localDir, err)
		return 2
	}

	actions := plan(remote, local, *localDir)
	report(remote, local, actions)

	todo := pending(actions)
	if len(todo) == 0 {
		fmt.Println("\nnothing to fetch - the local corpus is current.")
		return 0
	}
	if *dryRun {
		fmt.Println("\n-dry-run: stopping without fetching.")
		return 0
	}
	if !*yes && !confirm(fmt.Sprintf("\nfetch %d file(s), %s?", len(todo), bytesH(pendingBytes(todo)))) {
		fmt.Println("nothing fetched.")
		return 0
	}

	if err := os.MkdirAll(*localDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "clbsync: %v\n", err)
		return 2
	}
	var failed int
	for _, a := range todo {
		fmt.Printf("\n==> %s (%s, %s)\n", a.remote.Name, a.kind, bytesH(a.remote.Size))
		if err := fetch(*host, *remoteDir, a); err != nil {
			fmt.Fprintf(os.Stderr, "clbsync: %s: %v\n", a.remote.Name, err)
			failed++
		}
	}
	if failed > 0 {
		fmt.Fprintf(os.Stderr, "\n%d file(s) failed\n", failed)
		return 2
	}

	if *noProbe {
		return 0
	}
	if *yes || confirm("\nrun clbprobe over the corpus to check for format drift?") {
		return probe(*localDir)
	}
	fmt.Printf("skipped. Run it later with:  go run ./cmd/clbprobe %s\n", *localDir)
	return 0
}

// listRemote inventories the host in a single round trip. Reading each file's head
// remotely and hashing it there means the fingerprint costs 64KB of disk read per
// file and no network transfer at all.
func listRemote(host, dir string) ([]remoteFile, error) {
	script := fmt.Sprintf(
		`cd %s || exit 1; for f in *%s; do [ -e "$f" ] || continue; `+
			`printf '%%s\t%%s\t%%s\n' "$f" "$(stat -c%%s "$f")" `+
			`"$(head -c %d "$f" | sha256sum | cut -d" " -f1)"; done`,
		shellQuote(dir), archiveExt, fingerprintBytes)

	cmd := exec.Command("ssh", "-o", "BatchMode=yes", host, script)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var files []remoteFile
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		parts := strings.Split(sc.Text(), "\t")
		if len(parts) != 3 {
			continue
		}
		size, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			continue
		}
		files = append(files, remoteFile{Name: parts[0], Size: size, FP: parts[2]})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, sc.Err()
}

func listLocal(dir string) ([]localFile, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var files []localFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), archiveExt) {
			continue
		}
		p := filepath.Join(dir, e.Name())
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		fp, err := fingerprint(p)
		if err != nil {
			return nil, err
		}
		files = append(files, localFile{Path: p, Size: info.Size(), FP: fp})
	}
	return files, nil
}

// fingerprint hashes a file's first fingerprintBytes, matching what the remote
// side computes. A short file hashes whatever it has, so a partially-fetched file
// still identifies itself.
func fingerprint(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, fingerprintBytes)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// plan decides what to do with each remote file.
func plan(remote []remoteFile, local []localFile, dir string) []action {
	byFP := map[string]localFile{}
	taken := map[string]bool{}
	for _, l := range local {
		byFP[l.FP] = l
		taken[filepath.Base(l.Path)] = true
	}

	out := make([]action, 0, len(remote))
	for _, r := range remote {
		if l, ok := byFP[r.FP]; ok {
			// Same capture. Only the hour codex-lb is still writing will have grown.
			if r.Size > l.Size {
				out = append(out, action{remote: r, kind: "grown", target: l.Path, from: l.Size})
				continue
			}
			out = append(out, action{remote: r, kind: "current", target: l.Path, from: l.Size})
			continue
		}
		if taken[r.Name] {
			// The name is in use by a DIFFERENT capture. Keeping both is the whole
			// point: overwriting would destroy an hour of traffic that exists nowhere
			// else, and skipping would never fetch the new one.
			target := nextGeneration(dir, r.Name, taken)
			taken[filepath.Base(target)] = true
			out = append(out, action{remote: r, kind: "replaced", target: target})
			continue
		}
		out = append(out, action{remote: r, kind: "new", target: filepath.Join(dir, r.Name)})
	}
	return out
}

// nextGeneration names a recreated capture so it sits alongside its predecessor and
// is still discovered by anything globbing for archives.
func nextGeneration(dir, name string, taken map[string]bool) string {
	base := strings.TrimSuffix(name, archiveExt)
	for gen := 2; ; gen++ {
		candidate := fmt.Sprintf("%s.gen%d%s", base, gen, archiveExt)
		if !taken[candidate] {
			return filepath.Join(dir, candidate)
		}
	}
}

func pending(actions []action) []action {
	var out []action
	for _, a := range actions {
		if a.kind != "current" {
			out = append(out, a)
		}
	}
	return out
}

func pendingBytes(actions []action) int64 {
	var n int64
	for _, a := range actions {
		n += a.remote.Size - a.from
	}
	return n
}

func report(remote []remoteFile, local []localFile, actions []action) {
	var localBytes int64
	for _, l := range local {
		localBytes += l.Size
	}
	fmt.Printf("remote: %d archives\nlocal:  %d archives, %s\n\n", len(remote), len(local), bytesH(localBytes))

	for _, a := range actions {
		switch a.kind {
		case "current":
			fmt.Printf("  %-30s current\n", a.remote.Name)
		case "grown":
			fmt.Printf("  %-30s GROWN     %s -> %s (codex-lb is still writing this hour)\n",
				a.remote.Name, bytesH(a.from), bytesH(a.remote.Size))
		case "replaced":
			fmt.Printf("  %-30s REPLACED  same name, different capture - keeping both, fetching as %s\n",
				a.remote.Name, filepath.Base(a.target))
		default:
			fmt.Printf("  %-30s NEW       %s\n", a.remote.Name, bytesH(a.remote.Size))
		}
	}
}

// fetch copies one archive. rsync is used for its resume and delta transfer: the
// live hour is refetched every run and is usually only a few megabytes longer than
// the copy already held.
func fetch(host, remoteDir string, a action) error {
	src := fmt.Sprintf("%s:%s", host, filepath.Join(remoteDir, a.remote.Name))
	// Flags kept to the portable set. macOS ships openrsync ("rsync version 2.6.9
	// compatible"), which rejects --info=progress2 and exits 1 with a usage dump -
	// so the modern spelling fails every transfer on the machine this is run from.
	cmd := exec.Command("rsync", "-a", "--partial", "--progress", src, a.target)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func probe(dir string) int {
	fmt.Println()
	cmd := exec.Command("go", "run", "./cmd/clbprobe", dir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// clbprobe exits 1 on drift. That is a finding to read, not a sync failure.
			return ee.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "clbsync: running clbprobe: %v\n", err)
		return 2
	}
	return 0
}

func confirm(prompt string) bool {
	fmt.Printf("%s [y/N] ", prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// shellQuote makes a path safe inside the single remote command.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
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
	fmt.Fprint(os.Stderr, `usage: clbsync [flags]

Pulls conversation archives from the codex-lb host into the local corpus, then
offers to check them for format drift.

Files are identified by a fingerprint of their first bytes, not by name: codex-lb
recreates an archive at the same path after one is moved away, so a name-only sync
silently misses the second capture.

flags:
`)
	flag.PrintDefaults()
}
