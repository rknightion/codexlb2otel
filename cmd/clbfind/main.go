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

	if flag.NArg() != 1 {
		usage()
		return 2
	}
	id := flag.Arg(0)

	files, err := archives(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clbfind: %v\n", err)
		return 2
	}
	if len(files) == 0 {
		fmt.Fprintf(os.Stderr, "clbfind: no archives under %s\n", *dir)
		return 3
	}

	fmt.Fprintf(os.Stderr, "searching %d archives for %q...\n", len(files), id)
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
		if err := transcript(os.Stdout, files, id, opts, *maxText); err != nil {
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
		err := streamLines(f, func(line []byte) error {
			var rec frame.Record
			if json.Unmarshal(line, &rec) != nil {
				return nil
			}
			if !wanted[f][rec.RequestID] {
				return nil
			}
			done, err := r.Add(&rec)
			if done != nil {
				turns = append(turns, done)
			}
			return err
		})
		if err != nil {
			return err
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

			_ = streamLines(path, func(line []byte) error {
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
	r := turn.NewWithOptions(opts)
	var out []*turn.Turn

	err := streamLines(h.file, func(line []byte) error {
		var rec frame.Record
		if json.Unmarshal(line, &rec) != nil {
			return nil
		}
		if rec.RequestID != h.requestID {
			return nil
		}
		done, err := r.Add(&rec)
		if done != nil {
			out = append(out, done)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return append(out, r.Flush()...), nil
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
				pending = append(pending, res.Data...)
				for {
					i := bytes.IndexByte(pending, '\n')
					if i < 0 {
						break
					}
					if line := bytes.TrimSpace(pending[:i]); len(line) > 0 {
						if err := fn(line); err != nil {
							return err
						}
					}
					pending = pending[i+1:]
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

func archives(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".jsonl.gz") {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
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
	fmt.Fprint(os.Stderr, `usage: clbfind [flags] <id>

Finds a response in the corpus and prints what the pipeline would ship for it:
the model and reasoning parameters, the routing metadata that explains them, the
timing breakdown, and the conversation itself.

The id can be an OpenAI response id (resp_*), an archive request id (ws_* or a
UUID), a thread id, a turn id, or a tool call id.

flags:
`)
	flag.PrintDefaults()
}
