// Command clbstat surveys conversation-archive files: what event types appear, how
// much volume each accounts for, and what the reducer makes of them.
//
// It exists to answer sizing and coverage questions against real captures - notably
// "which event types is the reducer ignoring?", which is how unhandled protocol
// changes get noticed.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/rknightion/codexlb2otel/internal/archive"
	"github.com/rknightion/codexlb2otel/internal/frame"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

// chunkSize bounds how much of a file is held in memory at once. Archive files reach
// several hundred MB, so reading one whole is not acceptable in the long-running
// service; this mirrors the bounded read the watcher will use.
const chunkSize = 8 << 20

func main() {
	dir := flag.String("dir", "", "directory of *.jsonl.gz archive files")
	dumpTurns := flag.String("dump", "", "write reduced turns as JSONL to this path")
	flag.Parse()

	files, err := filepath.Glob(filepath.Join(*dir, "*.jsonl.gz"))
	if err != nil || len(files) == 0 {
		fmt.Fprintf(os.Stderr, "no archive files in %q\n", *dir)
		os.Exit(1)
	}
	sort.Strings(files)

	s := &stats{
		events:    map[string]int{},
		evBytes:   map[string]int{},
		unhandled: map[string]int{},
		tools:     map[string]int{},
		models:    map[string]int{},
		statuses:  map[string]int{},
		itemTypes: map[string]int{},
		threads:   map[string]bool{},
		logical:   map[string]bool{},
	}

	var out *os.File
	if *dumpTurns != "" {
		if out, err = os.Create(*dumpTurns); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer out.Close()
	}

	r := turn.New()
	for _, f := range files {
		if err := s.scanFile(f, r, out); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", filepath.Base(f), err)
		}
	}
	for _, t := range r.Flush() {
		s.record(t, out)
	}
	s.report()
}

type stats struct {
	files, members, lines int
	rawBytes              int64
	turnJSONBytes         int64
	responses             int
	incomplete            int
	subagent              int
	unparseable           int

	events    map[string]int
	evBytes   map[string]int
	unhandled map[string]int
	tools     map[string]int
	models    map[string]int
	statuses  map[string]int
	itemTypes map[string]int
	threads   map[string]bool
	logical   map[string]bool
}

// handled lists the event types the reducer acts on. Anything else is reported so a
// protocol addition shows up as a number rather than as silently missing telemetry.
var handled = map[string]bool{
	frame.EvResponseCreate: true, frame.EvResponseCreated: true,
	frame.EvResponseInProgress: true, frame.EvResponseCompleted: true,
	frame.EvOutputItemAdded: true, frame.EvOutputItemDone: true,
	frame.EvRateLimits: true, frame.EvResponseMetadata: true,
	frame.EvWebsocketTiming: true, frame.EvError: true,
	frame.EvOutputTextDelta:      true,
	frame.EvCustomToolInputDelta: true, frame.EvFunctionArgsDelta: true,
	frame.EvCustomToolInputDone: true, frame.EvFunctionArgsDone: true,
	frame.EvOutputTextDone: true, frame.EvContentPartAdded: true,
	frame.EvContentPartDone: true,
}

func (s *stats) scanFile(path string, r *turn.Reducer, out *os.File) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	s.files++
	s.rawBytes += fi.Size()

	buf := make([]byte, 0, chunkSize)
	tmp := make([]byte, chunkSize)
	for {
		n, rerr := f.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			res, derr := archive.DecodeMembers(buf)
			if derr != nil {
				return derr
			}
			s.members += res.Members
			if err := s.feed(res.Data, r, out); err != nil {
				return err
			}
			buf = append(buf[:0], buf[res.Consumed:]...)
		}
		if rerr != nil {
			return nil // io.EOF, or a read error we report by stopping
		}
	}
}

func (s *stats) feed(data []byte, r *turn.Reducer, out *os.File) error {
	return frame.Lines(data, func(rec *frame.Record) error {
		s.lines++
		ev, ok := rec.ParseEvent()
		if !ok {
			s.unparseable++
		} else {
			s.events[ev.Type]++
			s.evBytes[ev.Type] += len(rec.Payload.Text)
			if !handled[ev.Type] {
				s.unhandled[ev.Type]++
			}
		}
		done, err := r.Add(rec)
		if err != nil {
			return err
		}
		if done != nil {
			s.record(done, out)
		}
		return nil
	})
}

func (s *stats) record(t *turn.Turn, out *os.File) {
	s.responses++
	if t.Status == turn.StatusIncomplete {
		s.incomplete++
	}
	if t.IsSubagent {
		s.subagent++
	}
	s.threads[t.ThreadID] = true
	s.logical[t.LogicalTurnID] = true
	s.models[t.Model]++
	s.statuses[t.Status]++
	for _, tc := range t.ToolCalls {
		s.tools[tc.Name]++
	}
	for k, v := range t.ItemCounts {
		s.itemTypes[k] += v
	}
	b, err := json.Marshal(t)
	if err != nil {
		return
	}
	s.turnJSONBytes += int64(len(b)) + 1
	if out != nil {
		out.Write(b)
		out.Write([]byte{'\n'})
	}
}

func (s *stats) report() {
	mb := func(n int64) string { return fmt.Sprintf("%.1f MB", float64(n)/(1<<20)) }
	fmt.Printf("files=%d members=%d lines=%d compressed=%s unparseable=%d\n",
		s.files, s.members, s.lines, mb(s.rawBytes), s.unparseable)
	fmt.Printf("responses=%d (incomplete=%d subagent=%d) logical_turns=%d threads=%d\n",
		s.responses, s.incomplete, s.subagent, len(s.logical), len(s.threads))
	fmt.Printf("reduced turn JSON = %s  (%.2f%% of compressed input)\n",
		mb(s.turnJSONBytes), 100*float64(s.turnJSONBytes)/float64(s.rawBytes))

	fmt.Println("\n-- event types by payload bytes --")
	type kv struct {
		k string
		n int
		b int
	}
	var all []kv
	for k, n := range s.events {
		all = append(all, kv{k, n, s.evBytes[k]})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].b > all[j].b })
	for _, e := range all {
		mark := " "
		if !handled[e.k] {
			mark = "!"
		}
		fmt.Printf(" %s %9d  %8.1f MB  %s\n", mark, e.n, float64(e.b)/(1<<20), e.k)
	}
	if len(s.unhandled) > 0 {
		fmt.Printf("\n!! %d event types are NOT handled by the reducer\n", len(s.unhandled))
	}

	dump := func(title string, m map[string]int) {
		fmt.Printf("\n-- %s (%d distinct) --\n", title, len(m))
		type kv struct {
			k string
			n int
		}
		var xs []kv
		for k, n := range m {
			xs = append(xs, kv{k, n})
		}
		sort.Slice(xs, func(i, j int) bool { return xs[i].n > xs[j].n })
		for i, x := range xs {
			if i >= 25 {
				fmt.Printf("   ... %d more\n", len(xs)-i)
				break
			}
			fmt.Printf("  %8d  %q\n", x.n, x.k)
		}
	}
	dump("models", s.models)
	dump("statuses", s.statuses)
	dump("tool calls", s.tools)
	dump("output item types", s.itemTypes)
}
