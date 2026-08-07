// Command clbfind answers "why did this call do that, and what was said?".
//
// You paste an id you got somewhere else - codex-lb's UI, a dashboard, an alert -
// and it finds the response in the corpus and prints everything the pipeline would
// ship for it: the model and reasoning parameters and where they came from, the
// routing metadata, the timing breakdown, and the conversation itself.
//
// It is deliberately built on the same internal/turn reducer that feeds the
// exporters, so what it prints is what will land in Loki rather than a second,
// separately-drifting view of the archive. That makes it the working prototype of
// the query this whole project exists to enable.
//
// Any id in a record works: the OpenAI response id (resp_*), the archive request id
// (ws_* or a UUID), a thread id, a turn id, a call id.
//
//	clbfind resp_052b6a1e90eb18d3016a75b032be908191
//	clbfind resp_052b6a... corpus/processed/2026-08-07T10.jsonl.gz
//	clbfind -thread resp_052b6a...
//	clbfind -json ws_481fa8fa32724155a2ff8f372d7448ce
package main

import (
	"bytes"
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

	"github.com/rknightion/codexlb2otel/internal/archive"
	"github.com/rknightion/codexlb2otel/internal/frame"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

const chunkSize = 16 << 20

// shardChunk is deliberately smaller than chunkSize: a shard's compressed buffer
// decompresses to roughly three times its size, and every core running one at once
// is what decides the scan's peak memory.
const shardChunk = 4 << 20

func main() {
	os.Exit(run())
}

func run() int {
	var (
		dir     = flag.String("corpus", filepath.Join("corpus", "processed"), "corpus directory to search")
		asJSON  = flag.Bool("json", false, "print the reduced Turn as JSON instead of a report")
		full    = flag.Bool("full", false, "do not truncate captured content")
		thread  = flag.Bool("thread", false, "also print the whole conversation this response belongs to")
		maxText = flag.Int("max-text", 4000, "characters of each body to print in the report")
	)
	flag.Usage = usage
	flag.Parse()

	if flag.NArg() < 1 {
		usage()
		return 2
	}
	id := flag.Arg(0)

	// Any paths after the id narrow the search. codex-lb's own UI names the archive a
	// response came from, so pasting that alongside the id turns a whole-corpus scan
	// into a single-file one - seconds instead of a minute, and it grows worse as the
	// corpus does.
	searchPaths := flag.Args()[1:]
	if len(searchPaths) == 0 {
		searchPaths = []string{*dir}
	}

	files, err := archives(searchPaths)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clbfind: %v\n", err)
		return 2
	}
	if len(files) == 0 {
		fmt.Fprintf(os.Stderr, "clbfind: no archives in %s\n", strings.Join(searchPaths, " "))
		return 3
	}

	fmt.Fprintf(os.Stderr, "searching %d archive(s) for %q...\n", len(files), id)
	hits := search(files, id)
	if len(hits) == 0 {
		fmt.Fprintf(os.Stderr, "clbfind: %q not found in %s\n", id, *dir)
		return 1
	}

	opts := turn.DefaultOptions()
	if *full {
		opts.MaxToolOutputChars = 1 << 30
		opts.MaxPromptChars = 1 << 30
	}

	var turns []*turn.Turn
	for _, h := range hits {
		fmt.Fprintf(os.Stderr, "found in %s: request_id=%s (%d matching frames)\n",
			filepath.Base(h.file), h.requestID, h.frames)
		ts, err := reduce(h, opts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "clbfind: %s: %v\n", filepath.Base(h.file), err)
			return 2
		}
		turns = append(turns, ts...)
	}
	fmt.Fprintln(os.Stderr)

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		for _, t := range turns {
			if err := enc.Encode(t); err != nil {
				fmt.Fprintf(os.Stderr, "clbfind: %v\n", err)
				return 2
			}
		}
		return 0
	}
	for _, t := range turns {
		report(os.Stdout, t, *maxText)
	}

	if *thread {
		id := threadOf(turns)
		if id == "" {
			fmt.Fprintln(os.Stderr, "clbfind: no thread id on this response; cannot expand the conversation")
			return 1
		}
		// A conversation runs for hours and crosses archive rotations, so expanding it
		// deliberately searches the WHOLE corpus even when the id lookup was narrowed
		// to one file. Honouring the narrowing here would silently truncate the
		// transcript at the hour boundary.
		wide := files
		if all, err := archives([]string{*dir}); err == nil && len(all) > len(files) {
			fmt.Fprintf(os.Stderr, "expanding the search to all %d archives - a thread spans hours\n", len(all))
			wide = all
		}
		if err := transcript(os.Stdout, wide, id, opts, *maxText); err != nil {
			fmt.Fprintf(os.Stderr, "clbfind: %v\n", err)
			return 2
		}
	}
	return 0
}

func threadOf(turns []*turn.Turn) string {
	for _, t := range turns {
		if t.ThreadID != "" {
			return t.ThreadID
		}
	}
	return ""
}

// transcript reconstructs the whole conversation a response belongs to.
//
// One response is rarely the answer to "what was said". A mid-turn continuation
// carries only the tool result that provoked it - the human's actual request is
// several responses back, and only the thread as a whole reads as a conversation.
//
// Responses are replayed through ONE reducer in archive order so the cumulative
// counters diff correctly, which a per-response replay cannot do.
func transcript(w io.Writer, files []string, threadID string, opts turn.Options, maxText int) error {
	fmt.Fprintf(os.Stderr, "expanding thread %s...\n", threadID)
	hits := search(files, threadID)
	if len(hits) == 0 {
		return fmt.Errorf("thread %s not found", threadID)
	}

	wanted := map[string]map[string]bool{}
	for _, h := range hits {
		if wanted[h.file] == nil {
			wanted[h.file] = map[string]bool{}
		}
		wanted[h.file][h.requestID] = true
	}
	ordered := make([]string, 0, len(wanted))
	for f := range wanted {
		ordered = append(ordered, f)
	}
	sort.Strings(ordered)

	r := turn.NewWithOptions(opts)
	var turns []*turn.Turn
	for _, f := range ordered {
		recs, err := collect(f, func(rec *frame.Record) bool { return wanted[f][rec.RequestID] })
		if err != nil {
			return err
		}
		for _, rec := range recs {
			done, err := r.Add(rec)
			if done != nil {
				turns = append(turns, done)
			}
			if err != nil {
				return err
			}
		}
	}
	turns = append(turns, r.Flush()...)
	sort.SliceStable(turns, func(i, j int) bool { return turns[i].FirstTS.Before(turns[j].FirstTS) })

	rule(w, fmt.Sprintf("THREAD TRANSCRIPT - %d responses in %s", len(turns), threadID))
	for i, t := range turns {
		fmt.Fprintf(w, "\n--- [%d/%d] %s  %s %s/%s  %s  in=%d out=%d  %s\n",
			i+1, len(turns), t.FirstTS.Format("15:04:05"), t.Model, t.Effort, t.Verbosity,
			t.Status, t.InputTokens, t.OutputTokens, t.ResponseID)
		for _, p := range t.Prompts {
			fmt.Fprintf(w, "\n  %s (%d chars)\n", strings.ToUpper(p.Role), p.Chars)
			body(w, p.Text, maxText)
		}
		for _, o := range t.ToolOutputs {
			trunc := ""
			if o.Truncated {
				trunc = " TRUNCATED"
			}
			fmt.Fprintf(w, "\n  TOOL RESULT %s (%d chars%s)\n", o.CallID, o.Chars, trunc)
			body(w, o.Text, maxText)
		}
		for _, m := range t.Messages {
			fmt.Fprintf(w, "\n  ASSISTANT %s (%d chars)\n", m.Phase, m.Chars)
			body(w, m.Text, maxText)
		}
		for _, c := range t.ToolCalls {
			fmt.Fprintf(w, "\n  TOOL CALL %s %s call=%s (%d chars)\n", c.Kind, c.Name, c.CallID, c.InputChars)
			body(w, c.Input, maxText)
		}
		for _, m := range t.AgentMessages {
			fmt.Fprintf(w, "\n  AGENT MESSAGE %s -> %s (%d chars)\n", m.Author, m.Recipient, m.Chars)
			body(w, m.Text, maxText)
		}
	}
	fmt.Fprintln(w)
	return nil
}

// hit is one archive containing the wanted id, and the request the id belongs to.
type hit struct {
	file      string
	requestID string
	frames    int
}

// search scans decompressed bytes for the literal id before parsing anything.
//
// A byte search over the whole archive is far cheaper than decoding 1.5M records as
// JSON, and the id is the discriminator - so the expensive work only happens for
// the handful of frames that match. Whichever request_id those frames carry is what
// the second pass then reduces.
func search(files []string, id string) []hit {
	needle := []byte(id)

	var mu sync.Mutex
	found := map[string]*hit{}

	sem := make(chan struct{}, max(1, runtime.NumCPU()/2))
	var wg sync.WaitGroup
	for _, path := range files {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			_ = scanFile(path, func(_ int, line []byte) error {
				if !bytes.Contains(line, needle) {
					return nil
				}
				var rec frame.Record
				if json.Unmarshal(line, &rec) != nil {
					return nil
				}
				key := path + "\x00" + rec.RequestID
				mu.Lock()
				if found[key] == nil {
					found[key] = &hit{file: path, requestID: rec.RequestID}
				}
				found[key].frames++
				mu.Unlock()
				return nil
			})
		}(path)
	}
	wg.Wait()

	out := make([]hit, 0, len(found))
	for _, h := range found {
		out = append(out, *h)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].file != out[j].file {
			return out[i].file < out[j].file
		}
		return out[i].requestID < out[j].requestID
	})
	return out
}

// reduce replays only the frames belonging to the matched request.
//
// Their cumulative counters therefore have no prior baseline, so the delta fields
// come back flagged BaselineReset - correctly. They are upper bounds absorbing work
// done before the first frame we replayed, and the report says so rather than
// presenting them as measurements.
func reduce(h hit, opts turn.Options) ([]*turn.Turn, error) {
	recs, err := collect(h.file, func(rec *frame.Record) bool { return rec.RequestID == h.requestID })
	if err != nil {
		return nil, err
	}

	r := turn.NewWithOptions(opts)
	var out []*turn.Turn
	for _, rec := range recs {
		done, err := r.Add(rec)
		if done != nil {
			out = append(out, done)
		}
		if err != nil {
			return nil, err
		}
	}
	return append(out, r.Flush()...), nil
}

// collect gathers the records matching want, IN FILE ORDER, scanning concurrently.
//
// Order is not optional here: the reducer is a state machine over a response's
// frames, so replaying them out of sequence produces a different turn. Shards write
// into their own slice and are concatenated by index afterwards, which is exact -
// no locking on the hot path and no reliance on timestamps to reconstruct order.
func collect(path string, want func(*frame.Record) bool) ([]*frame.Record, error) {
	var mu sync.Mutex
	perShard := map[int][]*frame.Record{}

	err := scanFile(path, func(shard int, line []byte) error {
		var rec frame.Record
		if json.Unmarshal(line, &rec) != nil {
			return nil
		}
		if !want(&rec) {
			return nil
		}
		mu.Lock()
		perShard[shard] = append(perShard[shard], &rec)
		mu.Unlock()
		return nil
	})
	if err != nil {
		return nil, err
	}

	idx := make([]int, 0, len(perShard))
	for i := range perShard {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	var out []*frame.Record
	for _, i := range idx {
		out = append(out, perShard[i]...)
	}
	return out, nil
}

// shards splits a file into byte ranges that can be scanned concurrently.
//
// This is only sound because codex-lb closes a gzip member per batch: a worker can
// seek into the middle of the file and resynchronise onto the next member boundary
// (archive.FindMemberStart) instead of decoding everything before it. Boundaries
// cannot overlap or leave a gap, because "the first member start at or after X" is
// deterministic - so worker k stops at X and worker k+1 begins at the same member.
func shards(size int64, n int) [][2]int64 {
	if n < 1 {
		n = 1
	}
	out := make([][2]int64, 0, n)
	step := size / int64(n)
	if step < minShard {
		return [][2]int64{{0, size}}
	}
	for i := range n {
		start := int64(i) * step
		end := start + step
		if i == n-1 {
			end = size
		}
		out = append(out, [2]int64{start, end})
	}
	return out
}

// minShard keeps the work per goroutine worth the resync it costs. A var so tests
// can force sharding on a fixture small enough to be quick.
var minShard int64 = 8 << 20

// scanFile scans one archive concurrently, calling fn for every complete line with
// the index of the shard it came from.
//
// fn is called from several goroutines, so it must be safe for concurrent use. The
// shard index is what lets a caller that needs FILE ORDER recover it: shards cover
// ascending byte ranges, so appending per shard and concatenating in index order
// reproduces the original sequence exactly - without inventing an ordering from
// timestamps, which frames written in the same microsecond would not supply.
func scanFile(path string, fn func(shard int, line []byte) error) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	ranges := shards(st.Size(), max(1, runtime.NumCPU()-1))

	var wg sync.WaitGroup
	errs := make([]error, len(ranges))
	for i, r := range ranges {
		wg.Add(1)
		go func(i int, start, end int64) {
			defer wg.Done()
			errs[i] = streamRange(path, start, end, func(line []byte) error { return fn(i, line) })
		}(i, r[0], r[1])
	}
	wg.Wait()
	return errors.Join(errs...)
}

// streamRange hands over the lines of every member that BEGINS within [start, end).
//
// "Begins within" is the whole contract, and it is what makes shards tile the file
// exactly. A member straddling `end` belongs to this shard, because its start is
// inside the range; the next shard resynchronises past it. Getting this wrong is not
// subtle in effect but is invisible in code: an earlier version simply read a chunk
// and checked the offset afterwards, so with a chunk larger than a shard every
// worker ran to EOF and the scan emitted every line about five times.
func streamRange(path string, start, end int64, fn func([]byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	pos := start
	if start > 0 {
		// Land on a real member boundary before decoding anything.
		head := make([]byte, min(int64(shardChunk), end-start+resyncSlack))
		n, err := f.ReadAt(head, start)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		off, ok := archive.FindMemberStart(head[:n])
		if !ok {
			return nil // no member begins in this range
		}
		pos = start + int64(off)
	}
	if pos >= end {
		return nil
	}
	if _, err := f.Seek(pos, io.SeekStart); err != nil {
		return err
	}

	var pending []byte
	var buf []byte
	tmp := make([]byte, shardChunk)

	// Phase one: everything wholly inside the range. Reads are clamped to `end` so
	// no member starting beyond it can be decoded by accident.
	for pos < end {
		want := min(int64(len(tmp)), end-pos-int64(len(buf)))
		if want <= 0 {
			break
		}
		n, rerr := f.Read(tmp[:want])
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			res, derr := archive.DecodeMembers(buf)
			if derr != nil {
				return derr
			}
			if res.Consumed > 0 {
				if pending, err = emitLines(pending, res.Data, fn); err != nil {
					return err
				}
				buf = append(buf[:0], buf[res.Consumed:]...)
				pos += int64(res.Consumed)
			}
		}
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) {
				return rerr
			}
			break
		}
	}

	// Phase two: the member that straddles `end`. It started before `end`, so it is
	// ours - read past the boundary far enough to finish it, and exactly one member.
	if len(buf) > 0 {
		slack := make([]byte, resyncSlack)
		n, rerr := f.Read(slack)
		if rerr != nil && !errors.Is(rerr, io.EOF) {
			return rerr
		}
		buf = append(buf, slack[:n]...)
		data, consumed, derr := archive.DecodeMember(buf)
		if derr != nil {
			return derr
		}
		if consumed > 0 {
			if pending, err = emitLines(pending, data, fn); err != nil {
				return err
			}
		}
	}

	if line := bytes.TrimSpace(pending); len(line) > 0 {
		return fn(line)
	}
	return nil
}

// resyncSlack is how far past a shard's end a worker may read to finish the member
// it is already decoding.
const resyncSlack = 4 << 20

// emitLines hands every complete line in data to fn and returns the trailing
// partial for the next chunk to prepend.
//
// data is scanned in place. Archive members hold whole records, so the carry is
// almost always empty and the common path copies nothing - which is what keeps a
// sharded scan CPU-bound instead of drowning the collector.
func emitLines(carry, data []byte, fn func([]byte) error) ([]byte, error) {
	if len(carry) > 0 {
		data = append(carry, data...)
	}
	for {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			break
		}
		if line := bytes.TrimSpace(data[:i]); len(line) > 0 {
			if err := fn(line); err != nil {
				return nil, err
			}
		}
		data = data[i+1:]
	}
	if len(data) == 0 {
		return nil, nil
	}
	return append([]byte(nil), data...), nil
}

// streamLines decodes whole gzip members and hands over complete lines, so memory
// stays bounded no matter how large the archive is.
func streamLines(path string, fn func([]byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	var pending []byte
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
			if res.Consumed > 0 {
				var err error
				if pending, err = emitLines(pending, res.Data, fn); err != nil {
					return err
				}
				buf = append(buf[:0], buf[res.Consumed:]...)
			}
		}
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) {
				return rerr
			}
			break
		}
	}
	if line := bytes.TrimSpace(pending); len(line) > 0 {
		return fn(line)
	}
	return nil
}

// archives resolves paths that may be individual archives or directories to walk.
//
// A named file is taken at its word rather than filtered on extension: if someone
// points at a specific archive, refusing it because it does not end in .jsonl.gz
// would be unhelpful, and it will simply fail to decode if it is not one.
func archives(paths []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !st.IsDir() {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
			continue
		}
		err = filepath.WalkDir(p, func(q string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && strings.HasSuffix(q, ".jsonl.gz") && !seen[q] {
				seen[q] = true
				out = append(out, q)
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

func report(w io.Writer, t *turn.Turn, maxText int) {
	rule(w, "WHY THIS CALL RAN AS IT DID")
	kv(w, "model", t.Model)
	kv(w, "reasoning effort", t.Effort)
	kv(w, "verbosity", t.Verbosity)
	kv(w, "reasoning context", t.ReasoningCtx)
	kv(w, "reasoning mode", t.ReasoningMode)
	kv(w, "service tier", t.ServiceTier)
	kv(w, "parallel tool calls", fmt.Sprint(t.ParallelTools))
	fmt.Fprintln(w)
	// Request kind and thread source are the actual "why": a subagent turn inherits
	// its parameters from whoever spawned it, and a prewarm or compaction is not a
	// user turn at all.
	kv(w, "request kind", t.RequestKind)
	kv(w, "thread source", t.ThreadSource)
	kv(w, "subagent kind", t.SubagentKind)
	kv(w, "is subagent", fmt.Sprint(t.IsSubagent))
	kv(w, "sandbox", t.Sandbox)
	kv(w, "family", t.Family)
	kv(w, "originator", t.Originator)
	kv(w, "client version", t.ClientVersion)

	rule(w, "IDENTITY")
	kv(w, "request_id", t.RequestID)
	kv(w, "response_id", t.ResponseID)
	kv(w, "previous_response_id", t.PrevResponseID)
	kv(w, "turn_id (server)", t.TurnID)
	kv(w, "logical_turn_id", t.LogicalTurnID)
	kv(w, "thread_id", t.ThreadID)
	kv(w, "parent_thread_id", t.ParentThreadID)
	kv(w, "forked_from_thread_id", t.ForkedFromThreadID)
	kv(w, "session_id", t.SessionID)
	kv(w, "account_id", t.AccountID)
	kv(w, "plan", t.PlanType)
	kv(w, "prompt_cache_key", t.PromptCacheKey)
	kv(w, "engine_ids", t.EngineIDs)

	rule(w, "OUTCOME")
	kv(w, "status", t.Status)
	kv(w, "error", strings.TrimSpace(t.ErrorType+" "+t.ErrorCode))
	kv(w, "error message", t.ErrorMessage)
	kv(w, "transport event", t.TransportEvent)
	if t.CloseCode != nil {
		kv(w, "close code", fmt.Sprint(*t.CloseCode))
	}

	rule(w, "TIMING")
	if !t.TurnStart.IsZero() {
		kv(w, "turn started (client)", t.TurnStart.Format("15:04:05.000"))
	}
	kv(w, "response created", tsOf(t.ServerCreatedAt))
	kv(w, "response completed", tsOf(t.ServerCompletedAt))
	if !t.ServerCreatedAt.IsZero() && !t.ServerCompletedAt.IsZero() {
		kv(w, "elapsed", fmt.Sprintf("%.1fs", t.ServerCompletedAt.Sub(t.ServerCreatedAt).Seconds()))
	}
	ms(w, "ttft", t.TTFTMs)
	ms(w, "first message ttft", t.FirstMsgTTFTMs)
	fmt.Fprintln(w)
	cp := t.CriticalPath
	kv(w, "critical path coverage", cp.Coverage)
	if !cp.Complete() && cp.Coverage != "" {
		fmt.Fprintln(w, "    NOTE: the server marked this breakdown untrustworthy; the phases below do not add up")
	}
	num(w, "engine calls", cp.EngineCalls)
	ms(w, "  pre-inference", cp.PreInferenceMs)
	ms(w, "  engine wall", cp.EngineWallMs)
	ms(w, "  sampling + stream", cp.SamplingStreamMs)
	ms(w, "  client tool pause", cp.ClientToolPauseMs)
	ms(w, "  harness unblocked", cp.HarnessUnblockedMs)
	ms(w, "  other", cp.OtherMs)

	rule(w, "TOKENS")
	num(w, "input", t.InputTokens)
	num(w, "  cached", t.CachedTokens)
	num(w, "  cache write", t.CacheWriteTokens)
	num(w, "output", t.OutputTokens)
	num(w, "  reasoning", t.ReasoningTokens)
	num(w, "total", t.TotalTokens)
	if t.CacheHitRatio > 0 {
		kv(w, "cache hit ratio", fmt.Sprintf("%.3f", t.CacheHitRatio))
	}
	if t.BaselineReset {
		fmt.Fprintln(w, "\n    NOTE: replayed from a single request, so the cumulative-metric deltas have no")
		fmt.Fprintln(w, "    prior baseline and are upper bounds, not measurements. The usage counts above")
		fmt.Fprintln(w, "    are per-response and unaffected.")
	}

	if t.RateLimitUsedPercent > 0 || len(t.ExtraRateLimits) > 0 {
		rule(w, "RATE LIMITS (this account)")
		if t.RateLimitUsedPercent > 0 {
			kv(w, "primary window used", fmt.Sprintf("%.1f%% of a %dmin window", t.RateLimitUsedPercent, t.RateLimitWindowMin))
		}
		if t.RateLimit2UsedPercent > 0 {
			kv(w, "secondary window used", fmt.Sprintf("%.1f%% of a %dmin window", t.RateLimit2UsedPercent, t.RateLimit2WindowMin))
		}
		for _, k := range sortedKeys(t.ExtraRateLimits) {
			kv(w, "  "+k, fmt.Sprintf("%.1f%%", t.ExtraRateLimits[k]))
		}
	}

	if t.InstructionsHash != "" {
		rule(w, "SYSTEM PROMPT")
		kv(w, "instructions hash", t.InstructionsHash)
		kv(w, "instructions chars", fmt.Sprint(t.InstructionsChars))
		fmt.Fprintln(w, "    (body shipped once per distinct hash, not per response)")
	}

	if len(t.Prompts) > 0 {
		rule(w, fmt.Sprintf("CONVERSATION INPUT (%d items)", len(t.Prompts)))
		for i, p := range t.Prompts {
			fmt.Fprintf(w, "\n  [%d] %s (%d chars)\n", i+1, strings.ToUpper(p.Role), p.Chars)
			body(w, p.Text, maxText)
		}
	}
	if len(t.ToolOutputs) > 0 {
		rule(w, fmt.Sprintf("TOOL OUTPUT FED BACK IN (%d items)", len(t.ToolOutputs)))
		for i, o := range t.ToolOutputs {
			trunc := ""
			if o.Truncated {
				trunc = " TRUNCATED"
			}
			fmt.Fprintf(w, "\n  [%d] call %s (%d chars%s)\n", i+1, o.CallID, o.Chars, trunc)
			body(w, o.Text, maxText)
		}
	}
	if len(t.Messages) > 0 {
		rule(w, fmt.Sprintf("ASSISTANT OUTPUT (%d messages)", len(t.Messages)))
		for i, m := range t.Messages {
			fmt.Fprintf(w, "\n  [%d] %s (%d chars)\n", i+1, m.Phase, m.Chars)
			body(w, m.Text, maxText)
		}
	}
	if len(t.ToolCalls) > 0 {
		rule(w, fmt.Sprintf("TOOL CALLS MADE (%d)", len(t.ToolCalls)))
		for i, c := range t.ToolCalls {
			fmt.Fprintf(w, "\n  [%d] %s %s  call=%s status=%s (%d chars)\n",
				i+1, c.Kind, c.Name, c.CallID, c.Status, c.InputChars)
			if c.TaskName != "" {
				fmt.Fprintf(w, "      spawns %q on %s (%s)\n", c.TaskName, c.SubModel, c.SubEffort)
			}
			body(w, c.Input, maxText)
		}
	}
	if len(t.AgentMessages) > 0 {
		rule(w, fmt.Sprintf("AGENT MESSAGES (%d)", len(t.AgentMessages)))
		for i, m := range t.AgentMessages {
			fmt.Fprintf(w, "\n  [%d] %s -> %s (%d chars)\n", i+1, m.Author, m.Recipient, m.Chars)
			body(w, m.Text, maxText)
		}
	}

	rule(w, "FRAME ACCOUNTING")
	num(w, "frames", t.Frames)
	num(w, "bytes", t.Bytes)
	num(w, "text deltas", t.TextDeltas)
	num(w, "tool input deltas", t.ToolDeltas)
	num(w, "input items", t.InputItems)
	num(w, "encrypted reasoning chars", t.ReasoningEnc)
	for _, k := range sortedKeys(t.ItemCounts) {
		num(w, "  item "+k, t.ItemCounts[k])
	}
	fmt.Fprintln(w)
}

func rule(w io.Writer, title string) {
	fmt.Fprintf(w, "\n%s\n%s\n", title, strings.Repeat("=", len(title)))
}

func kv(w io.Writer, k, v string) {
	if strings.TrimSpace(v) == "" || v == "false" {
		return
	}
	fmt.Fprintf(w, "  %-24s %s\n", k, v)
}

func num[T int | int64](w io.Writer, k string, v T) {
	if v == 0 {
		return
	}
	fmt.Fprintf(w, "  %-24s %d\n", k, v)
}

func ms(w io.Writer, k string, v float64) {
	if v == 0 {
		return
	}
	fmt.Fprintf(w, "  %-24s %.0f ms\n", k, v)
}

func tsOf(t interface{ IsZero() bool }) string {
	type formatter interface{ Format(string) string }
	if t.IsZero() {
		return ""
	}
	return t.(formatter).Format("15:04:05.000")
}

func body(w io.Writer, s string, maxText int) {
	if s == "" {
		return
	}
	cut := false
	if len(s) > maxText {
		s, cut = s[:maxText], true
	}
	for _, line := range strings.Split(s, "\n") {
		fmt.Fprintf(w, "      | %s\n", line)
	}
	if cut {
		fmt.Fprintf(w, "      | ... (printed %d chars; -max-text to raise, -full to keep more at capture)\n", maxText)
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: clbfind [flags] <id> [archive-or-directory...]

Finds a response in the corpus and prints what the pipeline would ship for it:
the model and reasoning parameters, the routing metadata that explains them, the
timing breakdown, and the conversation itself.

The id can be an OpenAI response id (resp_*), an archive request id (ws_* or a
UUID), a thread id, a turn id, or a tool call id.

Paths after the id narrow the search to those archives. codex-lb's request-details
view names the file a response came from, so pasting it makes the lookup near
instant instead of scanning the whole corpus.

  clbfind resp_052b6a...
  clbfind resp_052b6a... corpus/processed/2026-08-07T10.jsonl.gz
  clbfind -thread resp_052b6a...        expand the whole conversation

flags:
`)
	flag.PrintDefaults()
}
