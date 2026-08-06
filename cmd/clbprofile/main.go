// Command clbprofile induces the shape of one or more conversation archives.
//
// It assumes nothing about the schema, so it reports exactly the fields, event types
// and value ranges present - including the ones the typed decoders ignore. Run it
// against a fresh capture before trusting that the extractor still sees everything.
//
//	clbprofile -out shapes.json testdata/live/*.jsonl.gz
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/rknightion/codexlb2otel/internal/archive"
	"github.com/rknightion/codexlb2otel/internal/profile"
)

func main() {
	out := flag.String("out", "", "write the full induced schema as JSON to this path")
	chunk := flag.Int("chunk", 16<<20, "bytes of compressed input to decode per pass")
	top := flag.Int("top", 12, "how many histogram entries to print per field")
	flag.Parse()

	files := flag.Args()
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "usage: clbprofile [-out shapes.json] <archive.jsonl.gz>...")
		os.Exit(2)
	}

	total := profile.New()
	sem := make(chan struct{}, max(1, runtime.NumCPU()/2))
	var wg sync.WaitGroup
	var failed int64
	var mu sync.Mutex

	for _, path := range files {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			p, err := profileFile(path, *chunk)
			if err != nil {
				mu.Lock()
				failed++
				mu.Unlock()
				fmt.Fprintf(os.Stderr, "%s: %v\n", filepath.Base(path), err)
				return
			}
			total.Merge(p)
			fmt.Fprintf(os.Stderr, "profiled %s\n", filepath.Base(path))
		}(path)
	}
	wg.Wait()

	report(os.Stdout, total, *top)

	if *out != "" {
		blob, err := json.MarshalIndent(total, "", "  ")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := os.WriteFile(*out, blob, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "wrote %s (%d bytes)\n", *out, len(blob))
	}
	if failed > 0 {
		os.Exit(1)
	}
}

// profileFile streams one archive, decoding only whole gzip members so memory stays
// bounded regardless of file size.
func profileFile(path string, chunk int) (*profile.Profile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	p := profile.New()
	fp := profile.FileProfile{Name: filepath.Base(path)}

	var pending []byte // decompressed bytes not yet ending in a newline
	buf := make([]byte, 0, chunk)
	tmp := make([]byte, chunk)
	for {
		n, rerr := f.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			res, derr := archive.DecodeMembers(buf)
			if derr != nil {
				return nil, derr
			}
			if res.Consumed > 0 {
				fp.Bytes += int64(len(res.Data))
				fp.Members += int64(res.Members)
				pending = consume(p, &fp, append(pending, res.Data...))
				buf = append(buf[:0], buf[res.Consumed:]...)
			}
		}
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) {
				return nil, rerr
			}
			break
		}
	}
	if len(bytes.TrimSpace(pending)) > 0 {
		p.AddLine(bytes.TrimSpace(pending), &fp)
	}

	p.Bytes = fp.Bytes
	p.Members = fp.Members
	p.Files = []profile.FileProfile{fp}
	return p, nil
}

// consume folds every complete line into the profile and returns the trailing partial.
func consume(p *profile.Profile, fp *profile.FileProfile, data []byte) []byte {
	for {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			return data
		}
		if line := bytes.TrimSpace(data[:i]); len(line) > 0 {
			p.AddLine(line, fp)
		}
		data = data[i+1:]
	}
}

func report(w io.Writer, p *profile.Profile, top int) {
	fmt.Fprintf(w, "== files ==\n")
	sort.Slice(p.Files, func(i, j int) bool { return p.Files[i].Name < p.Files[j].Name })
	for _, f := range p.Files {
		fmt.Fprintf(w, "  %-28s %9d lines %7d members %11d B  %s .. %s\n",
			f.Name, f.Lines, f.Members, f.Bytes,
			f.FirstTS.Format("15:04:05"), f.LastTS.Format("15:04:05"))
	}
	fmt.Fprintf(w, "\ntotal: %d lines, %d undecodable, %d members, %d decompressed bytes\n",
		p.Lines, p.Bad, p.Members, p.Bytes)

	fmt.Fprintf(w, "\n== record keys ==\n")
	printCounts(w, p.RecordKeys, p.Lines, 100)

	fmt.Fprintf(w, "\n== record fields ==\n")
	printHists(w, p.RecordFields, top)

	fmt.Fprintf(w, "\n== request_id shapes ==\n")
	printCounts(w, p.RequestIDShapes, p.Lines, 20)

	fmt.Fprintf(w, "\n== payload shapes ==\n")
	printCounts(w, p.PayloadShapes, p.Lines, 20)
	if p.PayloadUnparsed > 0 {
		fmt.Fprintf(w, "  payload.text not JSON: %d\n", p.PayloadUnparsed)
		for _, s := range p.UnparsedSamples {
			fmt.Fprintf(w, "    %s\n", s)
		}
	}

	fmt.Fprintf(w, "\n== headers ==\n")
	names := sortedKeys(p.Headers)
	for _, n := range names {
		h := p.Headers[n]
		var seen int64
		for _, c := range h.Counts {
			seen += c
		}
		if h.Distinct == 1 && h.Counts["<set>"] > 0 {
			fmt.Fprintf(w, "  %-34s %9d\n", n, seen)
			continue
		}
		fmt.Fprintf(w, "  %-34s %9d  %d distinct%s\n", n, seen, h.Distinct, capNote(h.Capped))
		for _, kv := range topN(h.Counts, top) {
			fmt.Fprintf(w, "      %-40s %9d\n", kv.k, kv.v)
		}
	}

	fmt.Fprintf(w, "\n== event types by record kind ==\n")
	for _, kind := range sortedKeys(p.KindEvents) {
		fmt.Fprintf(w, "  kind=%s\n", kind)
		printCounts(w, p.KindEvents[kind], p.Lines, 100)
	}

	fmt.Fprintf(w, "\n== induced event schemas ==\n")
	type ev struct {
		name string
		p    *profile.EventProfile
	}
	evs := make([]ev, 0, len(p.Events))
	for n, e := range p.Events {
		evs = append(evs, ev{n, e})
	}
	sort.Slice(evs, func(i, j int) bool { return evs[i].p.Count > evs[j].p.Count })
	for _, e := range evs {
		fmt.Fprintf(w, "\n  %s  (%d frames, %d sampled%s)\n",
			e.name, e.p.Count, e.p.Sampled, capNote(e.p.PathsCapped))
		paths := sortedKeys(e.p.Paths)
		for _, path := range paths {
			pi := e.p.Paths[path]
			kinds := make([]string, 0, len(pi.Types))
			for k := range pi.Types {
				kinds = append(kinds, k)
			}
			sort.Strings(kinds)
			line := fmt.Sprintf("    %-52s %-16s n=%d", path, strings.Join(kinds, "|"), pi.Count)
			if pi.Values != nil && pi.Values.Distinct <= 12 {
				line += "  " + strings.Join(valueList(pi.Values), ",")
			} else if pi.Sample != "" {
				line += "  eg " + pi.Sample
			}
			fmt.Fprintln(w, line)
		}
	}

	fmt.Fprintf(w, "\n== one record per transport/kind/direction ==\n")
	for _, k := range sortedKeys(p.Samples) {
		fmt.Fprintf(w, "\n  [%s]\n  %s\n", k, p.Samples[k])
	}
}

func valueList(h *profile.Hist) []string {
	out := make([]string, 0, len(h.Counts))
	for _, kv := range topN(h.Counts, 12) {
		out = append(out, fmt.Sprintf("%s=%d", kv.k, kv.v))
	}
	return out
}

type kv struct {
	k string
	v int64
}

func topN(m map[string]int64, n int) []kv {
	out := make([]kv, 0, len(m))
	for k, v := range m {
		out = append(out, kv{k, v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].v != out[j].v {
			return out[i].v > out[j].v
		}
		return out[i].k < out[j].k
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func printCounts(w io.Writer, m map[string]int64, total int64, n int) {
	for _, e := range topN(m, n) {
		pct := ""
		if total > 0 {
			pct = fmt.Sprintf("  %5.1f%%", 100*float64(e.v)/float64(total))
		}
		fmt.Fprintf(w, "  %-56s %9d%s\n", e.k, e.v, pct)
	}
	if len(m) > n {
		fmt.Fprintf(w, "  ...and %d more\n", len(m)-n)
	}
}

func printHists(w io.Writer, m map[string]*profile.Hist, n int) {
	for _, name := range sortedKeys(m) {
		h := m[name]
		fmt.Fprintf(w, "  %s (%d distinct%s)\n", name, h.Distinct, capNote(h.Capped))
		for _, e := range topN(h.Counts, n) {
			fmt.Fprintf(w, "      %-52s %9d\n", e.k, e.v)
		}
	}
}

func capNote(c bool) string {
	if c {
		return ", CAPPED"
	}
	return ""
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
