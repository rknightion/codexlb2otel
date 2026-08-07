package loki

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/rknightion/codexlb2otel/internal/archive"
	"github.com/rknightion/codexlb2otel/internal/attr"
	"github.com/rknightion/codexlb2otel/internal/fixture"
	"github.com/rknightion/codexlb2otel/internal/frame"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

// Mirrors internal/attr's helper of the same name; duplicated rather than exported
// because a test helper is not API.
var (
	corpusOnce  sync.Once
	corpusCache []*turn.Turn
	corpusErr   error
)

// corpusTurns reduces the WHOLE corpus, once per process.
//
// It read the three cheapest archives until 2026-08-07. The budget assertion below is
// a claim about the largest line the corpus can produce, and a sample cannot support
// a claim about a maximum - the same mistake #20 turned out to be, and the third
// place in this repo it had been made. It matters concretely here now that Turn
// carries a tool catalogue (#22 item 1): the catalogue rides on only 5.2% of
// response.create events, so the three cheapest archives may contain no line of the
// shape this test most needs to measure.
//
// Memoized because several tests here want the same reduction. The failure is
// memoized too rather than raised with t.Fatal from inside the Once, since Fatal is
// a Goexit that would still mark the Once done and leave later tests reporting "no
// turns reduced" instead of the real cause.
func corpusTurns(t *testing.T) []*turn.Turn {
	t.Helper()

	// fixture.All is called OUTSIDE the memo deliberately - see the identical note in
	// internal/attr/corpus_test.go. It is what SKIPS the test when no corpus is
	// present (CLB_NO_CORPUS, which is how CI runs), and a skip is a runtime.Goexit
	// that would otherwise mark the Once done with an empty cache, leaving every test
	// after the first to fail against zero turns. This repo's first CI run failed
	// exactly that way.
	paths := fixture.All(t)

	corpusOnce.Do(func() {
		r := turn.New()
		var out []*turn.Turn
		for _, path := range paths {
			res, err := archive.DecodeMembers(fixture.Load(t, path))
			if err != nil {
				corpusErr = fmt.Errorf("%s: %w", fixture.Name(path), err)
				return
			}
			err = frame.Lines(res.Data, func(rec *frame.Record) error {
				done, err := r.Add(rec)
				if done != nil {
					out = append(out, done)
				}
				return err
			})
			if err != nil {
				corpusErr = fmt.Errorf("%s: %w", fixture.Name(path), err)
				return
			}
		}
		corpusCache = append(out, r.Flush()...)
	})
	if corpusErr != nil {
		t.Fatalf("reducing the corpus: %v", corpusErr)
	}
	return corpusCache
}

// TestCorpus_NoLineExceedsBudget is issue #6's first acceptance criterion: feed the
// corpus and assert the maximum. Run at the DEFAULT MaxLineBytes (192KB) so the
// measured peak reflects the real production config, not an artificially tight one.
func TestCorpus_NoLineExceedsBudget(t *testing.T) {
	const budget = 192 << 10
	turns := corpusTurns(t)
	if len(turns) == 0 {
		t.Fatal("no turns reduced; this test asserts nothing")
	}

	guard := attr.NewGuard()
	rej := newFakeRejecter()
	maxSeen := 0
	maxRecordType := ""
	totalLines := 0
	totalBytes := 0
	streams := map[string]struct{}{}

	for _, tn := range turns {
		lines := buildLines(tn, guard, "codexlb2otel", attr.DefaultLabels, budget, nil, rej)
		for _, l := range lines {
			if len(l.body) > budget {
				t.Fatalf("record_type=%s line is %d bytes, exceeds the %d budget", l.recordType, len(l.body), budget)
			}
			if len(l.body) > maxSeen {
				maxSeen = len(l.body)
				maxRecordType = l.recordType
			}
			totalLines++
			totalBytes += len(l.body)
			streams[streamKey(l.labels)] = struct{}{}
		}
	}

	t.Logf("corpus measurement (default MaxLineBytes=%d, %d turns, whole corpus):", budget, len(turns))
	t.Logf("  lines emitted:        %d", totalLines)
	t.Logf("  total line bytes:     %d (%.2f MB)", totalBytes, float64(totalBytes)/(1<<20))
	t.Logf("  peak line size:       %d bytes, record_type=%s", maxSeen, maxRecordType)
	t.Logf("  distinct streams:     %d (default labels: %v)", len(streams), attr.DefaultLabels)
	t.Logf("  locally dropped:      %+v", rej.counts)

	if maxSeen == 0 {
		t.Fatal("no lines were measured; buildLines produced nothing")
	}
}

// TestCorpus_MetadataSurvivesOversizedToolOutput finds the turn with the largest
// captured tool output in the corpus and proves - against real data, not a
// synthetic fixture - that squeezing MaxLineBytes down to force truncation on that
// output leaves the turn-metadata line for the SAME turn intact and far smaller
// than the content that overflowed.
func TestCorpus_MetadataSurvivesOversizedToolOutput(t *testing.T) {
	turns := corpusTurns(t)

	var biggest *turn.Turn
	biggestChars := 0
	for _, tn := range turns {
		for _, to := range tn.ToolOutputs {
			if to.Chars > biggestChars {
				biggestChars = to.Chars
				biggest = tn
			}
		}
	}
	if biggest == nil || biggestChars < 2000 {
		t.Fatalf("the corpus under %s does not contain a tool output large enough to make this property "+
			"interesting (largest found: %d chars). Add an archive hour that does.", fixture.Root(t), biggestChars)
	}

	// A budget comfortably under the biggest tool output but comfortably over what
	// bare turn metadata needs, so truncation is forced on content without ever
	// touching the metadata line.
	budget := biggestChars / 4
	if budget > 4096 {
		budget = 4096
	}

	guard := attr.NewGuard()
	rej := newFakeRejecter()
	lines := buildLines(biggest, guard, "codexlb2otel", attr.DefaultLabels, budget, nil, rej)

	var turnLine *outLine
	for i := range lines {
		if lines[i].recordType == attr.RecordTurn {
			turnLine = &lines[i]
		}
	}
	if turnLine == nil {
		t.Fatalf("no turn-metadata line survived for request_id=%s despite its %d-char tool output", biggest.RequestID, biggestChars)
	}
	if len(turnLine.body) > budget {
		t.Fatalf("turn-metadata line is %d bytes, over the %d budget", len(turnLine.body), budget)
	}
	var decoded turn.Turn
	if err := json.Unmarshal(turnLine.body, &decoded); err != nil {
		t.Fatalf("turn-metadata line is not valid JSON: %v", err)
	}
	if decoded.RequestID != biggest.RequestID || decoded.Model != biggest.Model {
		t.Fatalf("turn-metadata line lost identity: got request_id=%s model=%s, want %s/%s",
			decoded.RequestID, decoded.Model, biggest.RequestID, biggest.Model)
	}
	if decoded.InputTokens != biggest.InputTokens || decoded.OutputTokens != biggest.OutputTokens {
		t.Fatalf("turn-metadata line lost token counts: got in=%d out=%d, want in=%d out=%d",
			decoded.InputTokens, decoded.OutputTokens, biggest.InputTokens, biggest.OutputTokens)
	}

	t.Logf("request_id=%s tool output %d chars against a %d-byte budget: turn-metadata line survived at %d bytes",
		biggest.RequestID, biggestChars, budget, len(turnLine.body))
}

// TestCorpus_ContentDedupFlowsThroughUnchanged is issue #5's dedup acceptance
// criterion applied to the sink rather than the reducer:
// TestReducer_InputContentIsDeduplicated already proves the reducer emits each
// distinct instruction body once GLOBALLY and each prompt once WITHIN ITS DEDUP
// SCOPE - developer messages (and the system instructions) dedupe globally because
// they are identical harness boilerplate across every thread, while user/assistant
// messages dedupe per-thread because they are the actual conversation and each
// thread must still reconstruct in full (see reducer.go's captureInput). Two
// different threads legitimately opening with the same short message ("hi") is not
// a dedup failure - it would only be one if the SAME scope emitted it twice.
//
// What this test proves is that the Loki sink does not re-introduce duplication (or
// drop content) on top of the reducer's own guarantee: each Turn.Prompts entry
// becomes exactly one Loki line, so a body that is unique within its scope in the
// reducer's output stays unique within that scope after passing through this sink.
//
// "GLOBALLY" above is bounded, not absolute, and this test used to assert otherwise.
// turn.seenSet is an LRU of SeenSetSize entries (32768 by default), so a body is
// deduped against the last N bodies rather than all of them, and one evicted after
// tens of thousands of intervening bodies is correctly re-emitted. Reading three
// archives never filled the cap, so the absolute assertion passed and looked true;
// the whole corpus fills it and it is false. The assertion is now on the rate, which
// is the property that actually distinguishes healthy eviction from a broken dedup.
func TestCorpus_ContentDedupFlowsThroughUnchanged(t *testing.T) {
	turns := corpusTurns(t)

	guard := attr.NewGuard()
	rej := newFakeRejecter()
	seenBodies := map[string]string{} // dedup-scope key -> first request_id that emitted it
	instructionLines := 0
	promptLines := 0
	dupes := 0

	for _, tn := range turns {
		lines := buildLines(tn, guard, "codexlb2otel", attr.DefaultLabels, 192<<10, nil, rej)
		for _, l := range lines {
			if l.recordType != attr.RecordPrompt && l.recordType != attr.RecordInstructions {
				continue
			}
			var p turn.Prompt
			if err := json.Unmarshal(l.body, &p); err != nil {
				t.Fatalf("prompt/instructions line is not valid JSON: %v", err)
			}
			if l.recordType == attr.RecordInstructions {
				instructionLines++
			} else {
				promptLines++
			}
			// Mirror the reducer's own dedup scope exactly: instructions and
			// developer messages are global, user/assistant are per-thread.
			scope := "global"
			if l.recordType == attr.RecordPrompt && p.Role != "developer" {
				scope = tn.ThreadID
			}
			// Include Chars (the reducer's UNTRUNCATED length) alongside the
			// possibly-truncated Text: two distinct bodies over MaxPromptChars that
			// happen to share their first 32768 characters would otherwise look
			// like one duplicated body once truncated, when the reducer correctly
			// treated them as different (different InstructionsHash / different
			// full-body dedup key - see reducer.go's captureInput and applyCreate).
			key := fmt.Sprintf("%s\x00%s\x00%d\x00%s", scope, p.Role, p.Chars, p.Text)
			if first, ok := seenBodies[key]; ok {
				dupes++
				t.Logf("%s body re-emitted in scope %s (LRU eviction): first on request_id=%s, again on request_id=%s",
					l.recordType, scope, first, tn.RequestID)
				continue
			}
			seenBodies[key] = tn.RequestID
		}
	}

	if instructionLines == 0 && promptLines == 0 {
		t.Fatal("no prompt/instructions lines were emitted at all; this test asserts nothing")
	}

	// A repeat is NOT a failure, and asserting otherwise was wrong - it only looked
	// right while this test read three archives. turn.seenSet is a bounded LRU
	// (SeenSetSize, default 32768), so "global" dedup means "global up to the cap":
	// a body evicted after tens of thousands of intervening bodies is legitimately
	// re-emitted. That is the deliberate trade - a duplicate Loki line costs bytes,
	// an unbounded dedup set costs the process - and it is exactly what the whole
	// corpus exercises and a sample never does.
	//
	// What must hold is that duplication stays RARE. If eviction were thrashing, the
	// dedup would be doing no useful work and the re-sent-history problem it exists
	// to solve would be back. A few percent is eviction; a large fraction is a bug.
	total := instructionLines + promptLines
	if dupes*10 > total {
		t.Errorf("%d of %d content lines are in-scope repeats (>10%%); the dedup set is "+
			"thrashing rather than evicting, which means re-sent history is being "+
			"shipped again in bulk", dupes, total)
	}
	t.Logf("instructions lines: %d, prompt lines: %d, in-scope repeats after LRU eviction: %d (%.2f%% of %d)",
		instructionLines, promptLines, dupes, 100*float64(dupes)/float64(total), total)
}

// TestCorpus_ForcedTruncation_MarksAndPreservesLength repeats the marker/length
// property from the synthetic unit test against real corpus content, with a budget
// tight enough to force truncation on ordinary (not artificially huge) tool output.
func TestCorpus_ForcedTruncation_MarksAndPreservesLength(t *testing.T) {
	turns := corpusTurns(t)
	const budget = 200 // small enough that most non-trivial tool output must be cut

	guard := attr.NewGuard()
	rej := newFakeRejecter()
	found := false

	for _, tn := range turns {
		lines := buildLines(tn, guard, "codexlb2otel", attr.DefaultLabels, budget, nil, rej)
		for _, l := range lines {
			if l.recordType != attr.RecordToolOutput {
				continue
			}
			var to turn.ToolOutput
			if err := json.Unmarshal(l.body, &to); err != nil {
				t.Fatalf("truncated tool_output line is not valid JSON: %v\nbody: %s", err, l.body)
			}
			if to.Chars > 0 && strings.Contains(to.Text, "truncated") {
				found = true
			}
		}
	}
	if !found {
		t.Skipf("no tool output in this slice of the corpus was large enough to force truncation at a %d-byte budget", budget)
	}
}

// TestCorpus_WorkedExample_Issue15 is issue #15's acceptance test: the exact
// response id from the issue's worked example, run through buildLines, checked
// against the same fields the issue lists. The id was live-verified in the corpus
// before writing this test (`zgrep -c resp_052b6a1e...118909
// corpus/processed/2026-08-07T10.jsonl.gz` -> 4 frame matches) and cmd/clbfind's
// own -json output against that archive matches the issue's worked example field
// for field, so no substitute turn was needed.
//
// What this test proves is NOT the whole of #15 - most of it was already true
// before this test existed. buildLines computes ONE baseMeta (guard.Metadata) per
// Turn and reuses it, unmodified, as every record type's metadata (record.go's
// emit closure) - so "all four ids on every record" and "content lines carry the
// thread id" hold structurally for any turn, not because of anything specific to
// this one. This test is what turns that structural argument into a corpus-backed
// measurement for the id the issue actually names, and pins the two genuine gaps
// found while validating it (see the GAP block below).
func TestCorpus_WorkedExample_Issue15(t *testing.T) {
	const wantResponseID = "resp_052b6a1e90eb18d3016a75b032be90819198bd170a3e118909"
	const wantThread = "019fdb99-a66a-7841-a420-e9f8602d4034"
	const wantParentThread = "019fdb97-294e-7d52-af76-399e39413bce"
	const wantPrevResponseID = "resp_052b6a1e90eb18d3016a75b026c8288191b4fa31ee48e0c46b"

	turns := corpusTurns(t)
	var tn *turn.Turn
	for _, c := range turns {
		if c.ResponseID == wantResponseID {
			tn = c
			break
		}
	}
	if tn == nil {
		t.Fatalf("response id %s named in issue #15's worked example is not in the corpus under %s "+
			"any more; substitute a turn selected by property (a subagent turn with a parent_thread_id) "+
			"and say so explicitly rather than silently changing what this test pins", wantResponseID, fixture.Root(t))
	}

	// Pin turn identity against the worked example before trusting anything derived
	// from it below - this confirms it is the SAME response the issue describes, not
	// a coincidental response_id match with different content.
	if tn.Model != "gpt-5.6-sol" || tn.Effort != "xhigh" || tn.Verbosity != "low" || tn.ServiceTier != "default" {
		t.Fatalf("worked-example turn model/effort/verbosity/service_tier = %s/%s/%s/%s, want gpt-5.6-sol/xhigh/low/default",
			tn.Model, tn.Effort, tn.Verbosity, tn.ServiceTier)
	}
	if tn.ThreadSource != "subagent" || tn.SubagentKind != "thread_spawn" {
		t.Fatalf("worked-example turn thread_source/subagent_kind = %s/%s, want subagent/thread_spawn",
			tn.ThreadSource, tn.SubagentKind)
	}
	if tn.ThreadID != wantThread || tn.ParentThreadID != wantParentThread {
		t.Fatalf("worked-example turn thread_id/parent_thread_id = %s/%s, want %s/%s",
			tn.ThreadID, tn.ParentThreadID, wantThread, wantParentThread)
	}
	if tn.InputTokens != 208010 || tn.OutputTokens != 490 || tn.ReasoningTokens != 153 {
		t.Fatalf("worked-example turn token counts = in=%d out=%d reasoning=%d, want in=208010 out=490 reasoning=153",
			tn.InputTokens, tn.OutputTokens, tn.ReasoningTokens)
	}

	guard := attr.NewGuard()
	rej := newFakeRejecter()
	lines := buildLines(tn, guard, "codexlb2otel", attr.DefaultLabels, 192<<10, nil, rej)
	if len(lines) == 0 {
		t.Fatal("buildLines produced no lines for the worked-example turn")
	}

	// kvMap applies the same dot->underscore translation the real Loki push does
	// (push.go's kvMap doc comment), so checking under attr.LokiKey(...) is checking
	// what a LogQL query against real structured metadata would actually see.
	want := map[string]string{
		attr.LokiKey(attr.GenAIResponseID): tn.ResponseID,
		attr.LokiKey(attr.RequestID):       tn.RequestID,
		attr.LokiKey(attr.ThreadID):        tn.ThreadID,
		attr.LokiKey(attr.SessionID):       tn.SessionID,
	}

	// --- acceptance: "all four ids are queryable structured metadata on every
	// emitted record" - checked on every record type this turn actually produced,
	// not only RecordTurn. ---
	recordTypesSeen := map[string]bool{}
	for _, l := range lines {
		recordTypesSeen[l.recordType] = true
		md := kvMap(l.metadata)
		for k, v := range want {
			if got := md[k]; got != v {
				t.Errorf("record_type=%s structured metadata[%s] = %q, want %q", l.recordType, k, got, v)
			}
		}
	}
	// The turn produced tool_call and tool_output content in the corpus (per
	// cmd/clbfind's own output for this id) - confirm both record types were
	// actually exercised above, so the loop's pass is not vacuously true from
	// RecordTurn alone.
	for _, rt := range []string{attr.RecordToolCall, attr.RecordToolOutput} {
		if !recordTypesSeen[rt] {
			t.Errorf("worked-example turn produced no %s line; the all-record-types assertion above did not "+
				"exercise this content type", rt)
		}
	}

	// --- acceptance: "the routing fields that explain the parameters are present
	// on every turn" - checked on the turn-metadata record specifically, since
	// that is what the worked example itself lists them against. ---
	var turnMD map[string]string
	for _, l := range lines {
		if l.recordType == attr.RecordTurn {
			turnMD = kvMap(l.metadata)
		}
	}
	if turnMD == nil {
		t.Fatal("no turn-metadata line was emitted for the worked-example turn")
	}
	for k, v := range map[string]string{
		attr.LokiKey(attr.ThreadSource):   "subagent",
		attr.LokiKey(attr.SubagentKind):   "thread_spawn",
		attr.LokiKey(attr.ParentThreadID): wantParentThread,
	} {
		if got := turnMD[k]; got != v {
			t.Errorf("turn metadata[%s] = %q, want %q", k, got, v)
		}
	}

	// --- GAP, NOT fixable in this package: internal/attr's registry (attr.go) has
	// no Field entry for turn.Turn.PrevResponseID or turn.Turn.ForkedFromThreadID -
	// grepped 2026-08-07, no "previous_response_id" or "forked_from_thread_id"
	// anywhere under internal/attr. Guard.Metadata walks the registry, so a field
	// absent from it is absent from EVERY Loki line's structured metadata,
	// including the turn line, and from all five content record types.
	//
	// Both values ARE captured correctly by the reducer (asserted below) and DO
	// reach the turn-metadata line's own JSON BODY, because turnLine (record.go)
	// marshals the whole Turn struct's scalars and Turn carries both fields
	// (turn.go:22 and :55) untouched by the content-array strip - so a reader who
	// parses that one line's body can still find them. What is missing is
	// specifically the structured-metadata routing the issue's other three ids
	// already have, and their presence on the four non-turn content record types.
	// Fixing this needs two new Identity Field entries in attr.go's registry (same
	// shape as the existing ParentThreadID entry) - that file is issue #3's frozen
	// attribute contract and is not owned by this lane.
	if tn.PrevResponseID != wantPrevResponseID {
		t.Errorf("worked-example turn previous_response_id = %q, want %q (reducer-captured value, unrelated to the metadata gap above)",
			tn.PrevResponseID, wantPrevResponseID)
	}
	if tn.ForkedFromThreadID != wantParentThread {
		t.Errorf("worked-example turn forked_from_thread_id = %q, want %q (reducer-captured value, unrelated to the metadata gap above)",
			tn.ForkedFromThreadID, wantParentThread)
	}
	var turnBody turn.Turn
	for _, l := range lines {
		if l.recordType == attr.RecordTurn {
			if err := json.Unmarshal(l.body, &turnBody); err != nil {
				t.Fatalf("turn-metadata line body is not valid JSON: %v", err)
			}
		}
	}
	if turnBody.PrevResponseID != wantPrevResponseID {
		t.Errorf("turn-metadata line BODY previous_response_id = %q, want %q - it reaches the line body today even though it is absent from structured metadata",
			turnBody.PrevResponseID, wantPrevResponseID)
	}
	if turnBody.ForkedFromThreadID != wantParentThread {
		t.Errorf("turn-metadata line BODY forked_from_thread_id = %q, want %q - it reaches the line body today even though it is absent from structured metadata",
			turnBody.ForkedFromThreadID, wantParentThread)
	}
}
