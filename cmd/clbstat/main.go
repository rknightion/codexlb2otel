// Command clbstat surveys conversation-archive files: what event types appear, how
// much volume each accounts for, and what the reducer makes of them.
//
// It exists to answer sizing and coverage questions against real captures - notably
// "which event types is the reducer ignoring?", which is how unhandled protocol
// changes get noticed.
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
	"sort"
	"strings"

	"github.com/rknightion/codexlb2otel/internal/archive"
	"github.com/rknightion/codexlb2otel/internal/attr"
	"github.com/rknightion/codexlb2otel/internal/frame"
	"github.com/rknightion/codexlb2otel/internal/sink/otlpmetric"
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

		metricGuard:  attr.NewGuard(),
		metricSeries: map[string]map[string]int{},
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
	// Keep scanning after a failure so every bad archive is named, but do not
	// let a partial scan report success.
	scanFailed := false
	for _, f := range files {
		if err := s.scanFile(f, r, out); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", filepath.Base(f), err)
			scanFailed = true
		}
	}
	for _, t := range r.Flush() {
		if err := s.record(t, out); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	s.report()
	if scanFailed {
		os.Exit(1)
	}
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

	metricGuard  *attr.Guard
	metricSeries map[string]map[string]int
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
	return s.linesLoose(data, func(rec *frame.Record) error {
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
			if err := s.record(done, out); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *stats) linesLoose(data []byte, fn func(*frame.Record) error) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		s.lines++
		rec, ok := s.decodeRecord(raw)
		if !ok {
			s.unparseable++
			continue
		}
		if err := fn(rec); err != nil {
			return err
		}
	}
}

func (s *stats) decodeRecord(raw json.RawMessage) (*frame.Record, bool) {
	var probe struct {
		Payload struct {
			Text json.RawMessage `json:"text"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, false
	}
	if len(probe.Payload.Text) > 0 && string(probe.Payload.Text) != "null" {
		var text string
		if err := json.Unmarshal(probe.Payload.Text, &text); err != nil {
			return nil, false
		}
	}
	var rec frame.Record
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, false
	}
	return &rec, true
}

func (s *stats) record(t *turn.Turn, out *os.File) error {
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
	for _, set := range otlpmetric.AttributeSetsForTurn(t, s.metricGuard) {
		if s.metricSeries[set.Instrument] == nil {
			s.metricSeries[set.Instrument] = map[string]int{}
		}
		s.metricSeries[set.Instrument][metricAttrKey(set.Attributes)]++
	}
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	s.turnJSONBytes += int64(len(b)) + 1
	if out != nil {
		if _, err := out.Write(b); err != nil {
			return err
		}
		if _, err := out.Write([]byte{'\n'}); err != nil {
			return err
		}
	}
	return nil
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
	s.dumpMetricSeries()
}

func (s *stats) dumpMetricSeries() {
	fmt.Printf("\n-- metric attribute combinations by instrument (%d total) --\n", s.totalMetricSeries())
	type kv struct {
		k string
		n int
	}
	var xs []kv
	for name, combos := range s.metricSeries {
		xs = append(xs, kv{name, len(combos)})
	}
	sort.Slice(xs, func(i, j int) bool {
		if xs[i].n != xs[j].n {
			return xs[i].n > xs[j].n
		}
		return xs[i].k < xs[j].k
	})
	for _, x := range xs {
		fmt.Printf("  %8d  %s\n", x.n, x.k)
	}
	if rejected := s.metricGuard.Rejected(); len(rejected) > 0 {
		fmt.Printf("guard_rejections=%v\n", rejected)
	}
}

func (s *stats) totalMetricSeries() int {
	var total int
	for _, combos := range s.metricSeries {
		total += len(combos)
	}
	return total
}

func metricAttrKey(kvs []attr.KV) string {
	parts := make([]string, 0, len(kvs))
	for _, kv := range kvs {
		parts = append(parts, kv.Key+"="+kv.Value)
	}
	sort.Strings(parts)
	return strings.Join(parts, "\x00")
}
