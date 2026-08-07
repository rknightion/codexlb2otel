package turn

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/rknightion/codexlb2otel/internal/archive"
	"github.com/rknightion/codexlb2otel/internal/fixture"
	"github.com/rknightion/codexlb2otel/internal/frame"
)

// These tests run against the real captured corpus, which is never committed - it
// holds full conversation content. Two consequences shape everything below.
//
// They assert INVARIANTS, not counts. The corpus is refreshed as new traffic shapes
// turn up, and pinning "1245 responses" only produced churn. What matters is that
// the reduction stays self-consistent whatever the input.
//
// And they DISCOVER their fixtures rather than naming them. Naming files is what
// silently detached this suite from its data once already: the corpus was
// reorganised, every t.Skip fired, and `go test ./...` still printed ok for every
// package while asserting nothing at all. See internal/fixture.
func reduceFiles(t *testing.T, paths ...string) []*Turn {
	t.Helper()
	r := New()
	var out []*Turn
	for _, p := range paths {
		out = append(out, feed(t, r, p)...)
	}
	out = append(out, r.Flush()...)
	sortByStart(out)
	return out
}

// corpusTurns reduces the n cheapest archives in the corpus.
func corpusTurns(t *testing.T, n int) []*Turn {
	t.Helper()
	return reduceFiles(t, fixture.Any(t, n)...)
}

// reduceUntil feeds archives, cheapest first, until the corpus has supplied the
// property the test needs - then stops. Some shapes are rare (the probe family was
// 204 records in 1.32M) and live in whichever hour happened to capture them, so a
// test cannot name the file that holds them.
//
// Exhausting the corpus without the property is a FAILURE, not a skip: it means the
// corpus no longer covers that case and the property is silently untested.
func reduceUntil(t *testing.T, what string, want func([]*Turn) bool) []*Turn {
	t.Helper()
	files := fixture.Files(t)
	r := New()
	var out []*Turn
	for i, p := range files {
		out = append(out, feed(t, r, p)...)
		if want(out) {
			t.Logf("%s found after %d/%d archives", what, i+1, len(files))
			out = append(out, r.Flush()...)
			sortByStart(out)
			return out
		}
	}
	out = append(out, r.Flush()...)
	sortByStart(out)
	if !want(out) {
		t.Fatalf("the corpus under %s does not contain %s (%d archives, %d turns). "+
			"Add an archive hour that does - otherwise this property is untested.",
			fixture.Root(t), what, len(files), len(out))
	}
	return out
}

func feed(t *testing.T, r *Reducer, path string) []*Turn {
	t.Helper()
	res, err := archive.DecodeMembers(fixture.Load(t, path))
	if err != nil {
		t.Fatalf("%s: %v", fixture.Name(path), err)
	}
	var out []*Turn
	err = frame.Lines(res.Data, func(rec *frame.Record) error {
		done, err := r.Add(rec)
		if done != nil {
			out = append(out, done)
		}
		return err
	})
	if err != nil {
		t.Fatalf("%s: %v", fixture.Name(path), err)
	}
	return out
}

// The delta conversion is the correctness core. The server's timing metrics are
// cumulative over a logical turn, so summing them raw overcounts by ~5.7x. Summed as
// deltas they must instead track the independent per-response usage figure.
func TestReducer_DeltasTrackUsage(t *testing.T) {
	turns := corpusTurns(t, 3)
	if len(turns) < 100 {
		t.Fatalf("only %d turns; fixtures look wrong", len(turns))
	}

	// Exclude cold-start responses: with no prior baseline their delta absorbs work
	// done before the reducer started watching, which is an over-count by definition
	// and would swamp the ratio on a short fixture.
	var input, promptDelta, skipped int
	for _, x := range turns {
		if x.BaselineReset {
			skipped++
			continue
		}
		input += x.InputTokens
		promptDelta += x.EnginePromptTokensDelta
	}
	t.Logf("compared %d turns, skipped %d cold-start", len(turns)-skipped, skipped)
	ratio := float64(promptDelta) / float64(input)
	if ratio < 0.98 || ratio > 1.02 {
		t.Errorf("engine-prompt-delta / input-tokens = %.4f, want ~1.0; a large value "+
			"means the cumulative metrics are being summed raw", ratio)
	}
}

// A negative delta means a logical-turn boundary was missed, which silently corrupts
// every downstream counter.
func TestReducer_NoNegativeDeltas(t *testing.T) {
	for _, x := range corpusTurns(t, 3) {
		if x.EngineCallsDelta < 0 || x.SampledTokensDelta < 0 ||
			x.EnginePromptTokensDelta < 0 || x.EngineCachedTokensDelta < 0 ||
			x.TurnTimeSecondsDelta < 0 || x.ClientToolPauseMsDelta < 0 {
			t.Fatalf("negative delta in %s at %s: calls=%d sampled=%d prompt=%d cached=%d",
				x.LogicalTurnID, x.FirstTS.Format("15:04:05"), x.EngineCallsDelta,
				x.SampledTokensDelta, x.EnginePromptTokensDelta, x.EngineCachedTokensDelta)
		}
	}
}

// Guards the cardinality rules the metric pipeline depends on. Anything asserted here
// is safe to use as a metric attribute; everything else must stay a log field.
func TestReducer_CardinalityAssumptions(t *testing.T) {
	turns := corpusTurns(t, 3)

	models, efforts, statuses, tools, errs := set{}, set{}, set{}, set{}, set{}
	for _, x := range turns {
		models.add(x.Model)
		efforts.add(x.Effort)
		statuses.add(x.Status)
		errs.add(x.ErrorCode)
		for _, tc := range x.ToolCalls {
			tools.add(tc.Name)
		}
	}
	for _, c := range []struct {
		name string
		s    set
		max  int
	}{
		{"models", models, 12},
		{"reasoning efforts", efforts, 6},
		{"statuses", statuses, 6},
		{"tool names", tools, 40},
		{"error codes", errs, 30},
	} {
		if len(c.s) > c.max {
			t.Errorf("%d distinct %s (max %d) - too many for a metric attribute: %v",
				len(c.s), c.name, c.max, c.s.keys())
		}
	}
	t.Logf("models=%v tools=%d statuses=%v", models.keys(), len(tools), statuses.keys())
}

// Every turn must carry a logical-turn id, including ones that never reported timing
// metrics. Without this, error and prewarm responses fall out of turn-level rollups.
func TestReducer_EveryTurnHasIdentity(t *testing.T) {
	for _, x := range corpusTurns(t, 3) {
		if x.LogicalTurnID == "" {
			t.Fatalf("turn %s has no logical_turn_id (status %q)", x.RequestID, x.Status)
		}
		if x.Status == "" {
			t.Fatalf("turn %s has no status", x.RequestID)
		}
	}
}

// A restart must not turn the next cumulative reading into one giant fake delta.
// This is the reason Snapshot/Restore exists.
func TestReducer_StateSurvivesRestart(t *testing.T) {
	// Enough archives that the comparison below has real weight: two of the smallest
	// left only 47 turns spanning the restart, which is thin evidence for the one
	// mechanism this test exists to prove.
	files := fixture.Any(t, 4)
	full := reduceFiles(t, files...)
	before, after := files[:2], files[2:]

	r1 := New()
	for _, f := range before {
		feed(t, r1, f)
	}
	blob, err := json.Marshal(r1.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var s State
	if err := json.Unmarshal(blob, &s); err != nil {
		t.Fatal(err)
	}
	r2 := New()
	r2.Restore(s)
	var resumed []*Turn
	for _, f := range after {
		resumed = append(resumed, feed(t, r2, f)...)
	}

	// Key by request_id, not position: a turn spanning the restart is handled
	// differently by the two runs and would shift every index after it.
	want := map[string]*Turn{}
	for _, x := range full {
		want[x.RequestID] = x
	}
	var compared int
	for _, got := range resumed {
		exp, ok := want[got.RequestID]
		if !ok || exp.Status != "completed" || got.Status != "completed" {
			continue
		}
		compared++
		if got.EnginePromptTokensDelta != exp.EnginePromptTokensDelta {
			t.Errorf("%s: prompt delta after restart = %d, want %d",
				got.RequestID, got.EnginePromptTokensDelta, exp.EnginePromptTokensDelta)
		}
	}
	if compared < 50 {
		t.Fatalf("only compared %d turns; the fixture or keying is wrong", compared)
	}
	t.Logf("verified %d turns identical across a restart", compared)
}

// Re-sent conversation history must not be emitted repeatedly. response.create
// replays the whole thread every turn, so without dedup the captured content would
// be quadratic in thread length.
//
// The scoping is deliberate and is what this asserts: developer/instructions prompts
// are harness boilerplate deduplicated GLOBALLY (one 66 KB system preamble stored
// once, not once per thread), while user and assistant messages are deduplicated
// PER THREAD so each thread still reconstructs in full - a subagent legitimately
// receives a copy of the user's request.
func TestReducer_InputContentIsDeduplicated(t *testing.T) {
	turns := corpusTurns(t, 3)

	var prompts, outputs int
	perThread, global := set{}, set{}
	for _, x := range turns {
		prompts += len(x.Prompts)
		outputs += len(x.ToolOutputs)
		for _, p := range x.Prompts {
			// Include Chars: Text is truncated, so two different long prompts can
			// share a prefix without being the same prompt.
			id := fmt.Sprintf("%s\x00%d\x00%s", p.Role, p.Chars, p.Text)
			scope, dupe := perThread, x.ThreadID+"\x00"+id
			if p.Role == "developer" || p.Role == "instructions" {
				scope, dupe = global, id
			}
			if !scope.add(dupe) {
				t.Errorf("prompt emitted twice in scope: role=%s chars=%d thread=%s",
					p.Role, p.Chars, x.ThreadID)
			}
		}
	}
	if prompts == 0 {
		t.Fatal("no prompts captured; the input half of the conversation is missing")
	}
	if outputs == 0 {
		t.Fatal("no tool outputs captured")
	}
	t.Logf("captured %d prompts + %d tool outputs", prompts, outputs)
}

// The record's own transport field reads "websocket" for every record in the capture,
// including the HTTP ones and the synthetic health checks, so Family must come from
// the request-id shape and the originator instead. Getting this wrong silently merges
// codex-lb's own "say OK" probes into the user-facing latency and cost metrics.
func TestReducer_ClassifiesRecordFamilies(t *testing.T) {
	turns := reduceUntil(t, "all three record families", haveAllFamilies)

	families := map[string]int{}
	probeModels := set{}
	var probeMaxInput, realTotal int
	for _, x := range turns {
		families[x.Family]++
		if x.Family != frame.FamilyProbe {
			realTotal++
			continue
		}
		probeModels.add(x.Model)
		// Probes must be identifiable by originator alone; if one is not, the
		// classifier has caught a real request by accident.
		if x.Originator != frame.OriginatorProbe {
			t.Errorf("probe-family turn %s has originator %q, not %q",
				x.RequestID, x.Originator, frame.OriginatorProbe)
		}
		if x.InputTokens > probeMaxInput {
			probeMaxInput = x.InputTokens
		}
	}
	// Probes deliberately exercise BOTH a mini model and the real one, so model overlap
	// with user traffic is expected and cannot be used to tell them apart. What does
	// separate them is size: the probe prompts are literally "say OK" and "hi", so a
	// probe carrying a real conversation's worth of context means the split has leaked.
	if probeMaxInput > 5000 {
		t.Errorf("largest probe input is %d tokens; real traffic is being classified "+
			"as a health check", probeMaxInput)
	}
	if families[frame.FamilyProbe]*5 > realTotal {
		t.Errorf("probes are %d of %d turns - too many to be health checks",
			families[frame.FamilyProbe], len(turns))
	}
	t.Logf("families=%v probe models=%v largest probe input=%d tokens",
		families, probeModels.keys(), probeMaxInput)
}

// The turn id must come from response.create's client_metadata, NOT from the
// x-codex-turn-metadata request header. The header is written once when the websocket
// opens and never refreshed, so on a live connection it reports request_kind=prewarm
// with an empty turn_id long after real turns have begun - measured at 177 of 199
// turns mislabelled. This test fails if anyone repoints it at the header.
func TestReducer_TurnIdentityComesFromClientMetadata(t *testing.T) {
	turns := corpusTurns(t, 3)

	var withTurnID, prewarm, realTurn int
	for _, x := range turns {
		if x.TurnID != "" {
			withTurnID++
			if x.LogicalTurnID != x.TurnID {
				t.Errorf("%s: logical_turn_id %q ignores the server turn id %q",
					x.RequestID, x.LogicalTurnID, x.TurnID)
			}
		}
		switch x.RequestKind {
		case frame.KindPrewarm:
			prewarm++
		case frame.KindTurn:
			realTurn++
		}
	}
	if withTurnID == 0 {
		t.Fatal("no turn carries a server turn id; client_metadata is not being read")
	}
	// The header would put nearly everything in the prewarm bucket. Real traffic is
	// overwhelmingly real turns, so a prewarm majority means we are reading the header.
	if prewarm > realTurn {
		t.Errorf("%d prewarm vs %d real turns - that ratio is the signature of reading "+
			"the stale x-codex-turn-metadata HEADER instead of client_metadata",
			prewarm, realTurn)
	}
	t.Logf("server turn ids on %d/%d turns; kinds: turn=%d prewarm=%d",
		withTurnID, len(turns), realTurn, prewarm)
}

// critical_path is scoped per-response by the server, so it needs no delta conversion
// and should agree with the delta arithmetic where both are available. Divergence
// means one of the two is being misread.
func TestReducer_CriticalPathAgreesWithDeltas(t *testing.T) {
	turns := corpusTurns(t, 3)

	var complete, partial, agree, compared int
	for _, x := range turns {
		if x.CriticalPath.Coverage == "" {
			continue
		}
		if x.CriticalPath.Complete() {
			complete++
		} else {
			partial++
		}
		// Engine calls are integers reported by both paths, so they can be compared
		// exactly. Skip cold starts, whose deltas absorb unobserved work.
		if x.BaselineReset || !x.CriticalPath.Complete() || x.CriticalPath.EngineCalls == 0 {
			continue
		}
		compared++
		if x.CriticalPath.EngineCalls == x.EngineCallsDelta {
			agree++
		}
	}
	if complete == 0 {
		t.Fatal("no critical_path captured; the per-response timings are being dropped")
	}
	if compared < 20 {
		t.Fatalf("only %d comparable responses; fixture too small to validate", compared)
	}
	// These are two independent derivations of the same quantity. They will not match
	// everywhere - the delta path resets at turn boundaries the server draws slightly
	// differently - but broad disagreement means one is wrong.
	if ratio := float64(agree) / float64(compared); ratio < 0.8 {
		t.Errorf("critical_path engine calls agree with the delta only %.0f%% of the "+
			"time (%d/%d); one of the two derivations is wrong", ratio*100, agree, compared)
	}
	t.Logf("critical_path: %d complete, %d partial; engine calls agree %d/%d",
		complete, partial, agree, compared)
}

// codex-lb balances across several ChatGPT accounts, so account identity and per-account
// rate-limit headroom are the point of the exercise. Losing them makes every headroom
// figure an average across accounts, which hides the exhaustion it exists to show.
func TestReducer_CapturesRoutingDimensions(t *testing.T) {
	turns := reduceUntil(t, "routing dimensions (account, plan, rate limits)", haveRoutingDimensions)

	accounts, plans, safety, kinds := set{}, set{}, set{}, set{}
	var withRateLimit, withExtraLimits int
	for _, x := range turns {
		accounts.add(x.AccountID)
		plans.add(x.PlanType)
		safety.add(x.SafetyID)
		kinds.add(x.ThreadSource)
		if x.RateLimitUsedPercent > 0 {
			withRateLimit++
		}
		if len(x.ExtraRateLimits) > 0 {
			withExtraLimits++
		}
	}
	// Every one of these is a metric attribute, so an unbounded value would be a
	// cardinality incident rather than a cosmetic problem.
	if len(accounts) > 10 || len(plans) > 5 || len(kinds) > 5 {
		t.Errorf("routing dimensions too wide for metric labels: accounts=%d plans=%v kinds=%v",
			len(accounts), plans.keys(), kinds.keys())
	}
	t.Logf("accounts=%d plans=%v thread_sources=%v rate-limited=%d per-model=%d",
		len(accounts), plans.keys(), kinds.keys(), withRateLimit, withExtraLimits)
}

// Instructions run to 67 KB and take only a handful of distinct values across a whole
// day, so the body must be emitted once while every response still carries the hash.
// Shipping the body per response would dominate log spend for no extra information.
func TestReducer_InstructionsAreHashedNotRepeated(t *testing.T) {
	turns := corpusTurns(t, 3)

	hashes, bodies := set{}, 0
	var chars int
	for _, x := range turns {
		if x.InstructionsHash == "" {
			continue
		}
		hashes.add(x.InstructionsHash)
		if x.InstructionsChars > chars {
			chars = x.InstructionsChars
		}
		for _, p := range x.Prompts {
			if p.Role == "instructions" {
				bodies++
			}
		}
	}
	if len(hashes) == 0 {
		t.Skip("no instructions in fixture")
	}
	if bodies > len(hashes) {
		t.Errorf("emitted %d instruction bodies for %d distinct prompts; the dedup is "+
			"not holding and every response is carrying up to %d bytes of boilerplate",
			bodies, len(hashes), chars)
	}
	t.Logf("%d distinct instruction prompts, largest %d bytes, %d bodies emitted",
		len(hashes), chars, bodies)
}

type set map[string]bool

// add records k (ignoring empties) and reports whether it was new.
func (s set) add(k string) bool {
	if k == "" || s[k] {
		return false
	}
	s[k] = true
	return true
}

func (s set) keys() []string {
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	return out
}

// ---- corpus-coverage predicates ----
//
// These say what a test needs the corpus to CONTAIN, separately from what it then
// asserts about it. Keeping the two apart is what lets a missing shape be reported
// as "the corpus no longer covers this" rather than as a failed assertion, which
// would send the next reader looking for a bug in the reducer.

func haveAllFamilies(turns []*Turn) bool {
	seen := set{}
	for _, x := range turns {
		seen.add(x.Family)
	}
	for _, f := range []string{frame.FamilyWebsocket, frame.FamilyHTTP, frame.FamilyProbe} {
		if !seen[f] {
			return false
		}
	}
	return true
}

func haveRoutingDimensions(turns []*Turn) bool {
	var account, plan, safety, limits, perModel bool
	for _, x := range turns {
		account = account || x.AccountID != ""
		plan = plan || x.PlanType != ""
		safety = safety || x.SafetyID != ""
		limits = limits || x.RateLimitUsedPercent > 0
		perModel = perModel || len(x.ExtraRateLimits) > 0
	}
	return account && plan && safety && limits && perModel
}
