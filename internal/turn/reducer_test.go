package turn

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

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

// enoughArchivesFor returns the fewest archives, cheapest first, that between them
// reduce to at least want turns - and always an even number, so a caller splitting
// the list in half gets two non-empty sides.
//
// For the tests that need the FILE LIST rather than just the turns. Same contract as
// reduceUntil otherwise, including exhausting the corpus being a hard failure rather
// than a silent pass over too little data.
func enoughArchivesFor(t *testing.T, want int) []string {
	t.Helper()
	total := len(fixture.Files(t))
	// fixture.Any is what selects the cheapest k AND puts them back in chronological
	// order. Reducing them in size order instead would put the restart split at an
	// arbitrary point in time, which is not the thing being tested.
	for k := 2; k <= total; k += 2 {
		files := fixture.Any(t, k)
		if n := len(reduceFiles(t, files...)); n >= want {
			t.Logf("%d turns across the %d cheapest archives", n, k)
			return files
		}
	}
	t.Fatalf("the corpus under %s does not reduce to %d turns across all %d archives. "+
		"Add an archive hour - otherwise the property is tested against too little "+
		"data to mean anything.", fixture.Root(t), want, total)
	return nil
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
	// Asks for the turns it needs, not for three archives that used to hold them:
	// corpusTurns takes the CHEAPEST n, and a 2026-08-08 sync of two quiet overnight
	// hours (146 and 250 lines) left the three cheapest holding 13 turns. See #34.
	turns := reduceUntil(t, "at least 100 turns to measure the delta ratio against",
		func(ts []*Turn) bool { return len(ts) >= 100 })

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

// A negative delta means a series boundary was missed, which silently corrupts every
// downstream counter.
//
// This runs over the WHOLE corpus, not the three cheapest archives it used to read.
// That is the reason it took until 2026-08-07 to catch the interleaved-memory bug
// (see TestReducer_InterleavedMemoryKeepsItsOwnBaseline): the failure needs a memory
// response to land inside a user turn, which happened in twelve turns out of 8,078,
// none of them in the three smallest hours. A guard on the package's core correctness
// property is worth the minute it costs; sampling it defeated the point.
func TestReducer_NoNegativeDeltas(t *testing.T) {
	turns := reduceFiles(t, fixture.All(t)...)
	for _, x := range turns {
		if x.EngineCallsDelta < 0 || x.SampledTokensDelta < 0 ||
			x.EnginePromptTokensDelta < 0 || x.EngineCachedTokensDelta < 0 ||
			x.TurnTimeSecondsDelta < 0 || x.ClientToolPauseMsDelta < 0 {
			t.Fatalf("negative delta in %s (kind %q) at %s: calls=%d sampled=%d prompt=%d cached=%d",
				x.LogicalTurnID, x.RequestKind, x.FirstTS.Format("15:04:05"), x.EngineCallsDelta,
				x.SampledTokensDelta, x.EnginePromptTokensDelta, x.EngineCachedTokensDelta)
		}
		// The nine cumulative fields #12's corpus comment added get the identical
		// guard: a negative value here is the exact bug class #20 was (one series
		// diffed against another's baseline), just on a field the original guard
		// predates.
		if x.EngineServiceInferenceMsDelta < 0 || x.EngineServiceSamplingMsDelta < 0 ||
			x.EngineIapiInferenceMsDelta < 0 || x.EngineIapiSamplingMsDelta < 0 ||
			x.ResponsesExclEngineAndToolMsDelta < 0 || x.ResponsesExclEngineWaitSamplingMsDelta < 0 ||
			x.ResponsesExclEngineWaitSamplingIapiMsDelta < 0 || x.ResponsesAPIExclClientToolsMsDelta < 0 ||
			x.EngineUncachedPromptTokensDelta < 0 {
			t.Fatalf("negative delta in %s (kind %q) at %s: "+
				"service_inference=%.2f service_sampling=%.2f iapi_inference=%.2f iapi_sampling=%.2f "+
				"excl_engine_tool=%.2f excl_engine_wait_sampling=%.2f excl_engine_wait_sampling_iapi=%.2f "+
				"excl_client_tools=%.2f uncached_prompt=%d",
				x.LogicalTurnID, x.RequestKind, x.FirstTS.Format("15:04:05"),
				x.EngineServiceInferenceMsDelta, x.EngineServiceSamplingMsDelta,
				x.EngineIapiInferenceMsDelta, x.EngineIapiSamplingMsDelta,
				x.ResponsesExclEngineAndToolMsDelta, x.ResponsesExclEngineWaitSamplingMsDelta,
				x.ResponsesExclEngineWaitSamplingIapiMsDelta, x.ResponsesAPIExclClientToolsMsDelta,
				x.EngineUncachedPromptTokensDelta)
		}
	}
	t.Logf("checked %d turns across the whole corpus", len(turns))
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
	// Enough archives that the comparison below has real weight, chosen by how many
	// turns they actually hold rather than by a fixed count of files. Four of the
	// cheapest was the previous rule and it decayed twice: to 47 turns once, and to 8
	// after a 2026-08-08 sync of two quiet overnight hours (#34). A file count is a
	// proxy for a turn count, and it is the proxy that keeps going stale.
	//
	// This test cannot use reduceUntil like its neighbours - it needs the FILE LIST to
	// split into a before and an after, not just the turns - so it grows the list
	// itself, on the same principle.
	files := enoughArchivesFor(t, 200)
	full := reduceFiles(t, files...)
	half := len(files) / 2
	before, after := files[:half], files[half:]

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
//
// Split deliberately, because corpus coverage of a family is not something a test can
// rely on. The corpus currently holds no HTTP or probe traffic at all - no UUID request
// ids, no codex_cli_rs originator - so requiring them would make this test unpassable.
//
// That is a COVERAGE gap, not a protocol change, and the distinction matters: codex-lb's
// request_logs shows 199 HTTP requests in the same 48 hours, all between 16:00 and 18:42
// on 2026-08-06, which is precisely the window whose captures were deleted. The family
// will reappear in future captures.
//
// So the corpus proves what a corpus can prove - that every real turn gets classified -
// and the families it happens not to contain are proven against constructed records,
// where the input is known rather than hoped for. When HTTP and probe traffic returns,
// the corpus test picks it up and applies the probe assertions automatically.
func TestReducer_ClassifiesRecordFamilies(t *testing.T) {
	turns := corpusTurns(t, 3)

	families := map[string]int{}
	for _, x := range turns {
		if x.Family == "" {
			t.Fatalf("turn %s has no family", x.RequestID)
		}
		families[x.Family]++
		if x.Family != frame.FamilyProbe {
			continue
		}
		if x.Originator != frame.OriginatorProbe {
			t.Errorf("probe-family turn %s has originator %q, not %q",
				x.RequestID, x.Originator, frame.OriginatorProbe)
		}
		// Probes deliberately exercise BOTH a mini model and the real one, so model
		// overlap with user traffic cannot separate them. Size can: the probe prompts
		// are literally "say OK" and "hi", so a probe carrying a real conversation's
		// worth of context means the split has leaked.
		if x.InputTokens > 5000 {
			t.Errorf("probe turn %s has %d input tokens; real traffic is being "+
				"classified as a health check", x.RequestID, x.InputTokens)
		}
	}
	t.Logf("families in the corpus: %v", families)
}

// The families the corpus no longer supplies, proven against constructed records.
func TestReducer_ClassifiesFamiliesFromConstructedRecords(t *testing.T) {
	for _, tc := range []struct {
		name      string
		requestID string
		headers   frame.Headers
		want      string
	}{
		{
			name:      "websocket TUI",
			requestID: "ws_481fa8fa32724155a2ff8f372d7448ce",
			headers:   frame.Headers{"originator": "codex-tui"},
			want:      frame.FamilyWebsocket,
		},
		{
			// Same client over per-request HTTP: a UUID request id matching x-request-id.
			name:      "HTTP TUI",
			requestID: "550e8400-e29b-41d4-a716-446655440000",
			headers: frame.Headers{
				"originator": "codex-tui", "x-request-id": "550e8400-e29b-41d4-a716-446655440000",
				"x-codex-turn-state": "http_turn_active",
			},
			want: frame.FamilyHTTP,
		},
		{
			// codex-lb's own health check. Originator wins over the id shape: a probe
			// runs over the websocket too, so the id alone would misclassify it.
			name:      "health probe over websocket",
			requestID: "ws_0000000000000000000000000000dead",
			headers:   frame.Headers{"originator": frame.OriginatorProbe, "version": "0.146.1"},
			want:      frame.FamilyProbe,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := New()
			base := time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC)
			var got *Turn
			for i, ev := range []string{
				`{"type":"response.created","response":{"id":"resp_x","model":"gpt-5.6-sol"}}`,
				`{"type":"response.completed","response":{"id":"resp_x","model":"gpt-5.6-sol",` +
					`"status":"completed","usage":{"input_tokens":12,"output_tokens":3,"total_tokens":15}}}`,
			} {
				done, err := r.Add(&frame.Record{
					RequestID: tc.requestID,
					Kind:      "responses",
					Transport: "websocket", // constant across every family; never a discriminator
					Direction: frame.ToCodex,
					Headers:   tc.headers,
					Timestamp: base.Add(time.Duration(i) * time.Second),
					Payload:   frame.Payload{Text: ev},
				})
				if err != nil {
					t.Fatal(err)
				}
				if done != nil {
					got = done
				}
			}
			if got == nil {
				t.Fatal("no turn emitted")
			}
			if got.Family != tc.want {
				t.Errorf("family = %q, want %q", got.Family, tc.want)
			}
			if tc.want == frame.FamilyProbe && got.Originator != frame.OriginatorProbe {
				t.Errorf("originator = %q, want %q", got.Originator, frame.OriginatorProbe)
			}
		})
	}
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
// comparableToDelta is the subset this test can actually check: critical_path was
// captured AND the server called it trustworthy AND the delta path had a baseline to
// diff against. Shared between the fixture predicate and the loop so the two cannot
// drift into disagreeing about what "comparable" means.
func comparableToDelta(x *Turn) bool {
	return x.CriticalPath.Complete() && !x.BaselineReset && x.CriticalPath.EngineCalls != 0
}

func TestReducer_CriticalPathAgreesWithDeltas(t *testing.T) {
	// Widened from the three cheapest archives for the reason in #34: a quiet hour
	// carries few responses and proportionally fewer that clear the bar above.
	turns := reduceUntil(t, "at least 20 responses comparable against the delta path",
		func(ts []*Turn) bool {
			n := 0
			for _, x := range ts {
				if comparableToDelta(x) {
					n++
				}
			}
			return n >= 20
		})

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
		if !comparableToDelta(x) {
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

// The tool catalogue gets the identical dedup-by-hash treatment as instructions
// above, mirrored deliberately per #22 item 1. Corpus note: the wire protocol never
// carries a top-level response.create "tools" field (measured against all 8,916
// response.create events in the 2026-08-07 corpus) - the only tools[] is nested
// inside an "additional_tools" input item, which is what this test constructs.
func TestReducer_ToolCatalogueDedupedByHash(t *testing.T) {
	const thread = "thread-tools"
	r := New()
	base := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)

	create := func(req, toolsJSON string, ts time.Time) *Turn {
		ev := fmt.Sprintf(`{"type":"response.create","model":"gpt-5.6-sol",`+
			`"client_metadata":{"thread_id":%q},`+
			`"input":[{"type":"additional_tools","role":"developer","tools":%s}]}`,
			thread, toolsJSON)
		var out *Turn
		for i, e := range []string{ev,
			`{"type":"response.completed","response":{"status":"completed","model":"gpt-5.6-sol"}}`,
		} {
			done, err := r.Add(&frame.Record{
				RequestID: req,
				Headers:   frame.Headers{"thread-id": thread, "originator": "codex-tui"},
				Timestamp: ts.Add(time.Duration(i) * time.Millisecond),
				Payload:   frame.Payload{Text: e},
			})
			if err != nil {
				t.Fatal(err)
			}
			if done != nil {
				out = done
			}
		}
		if out == nil {
			t.Fatalf("%s: no turn emitted", req)
		}
		return out
	}

	toolsA := `[{"type":"custom","name":"exec","description":"run code","strict":true}]`
	toolsB := `[{"type":"custom","name":"exec","description":"run code","strict":true},` +
		`{"type":"custom","name":"wait","description":"pause"}]`

	first := create("req-1", toolsA, base)
	second := create("req-2", toolsA, base.Add(time.Second))  // identical catalogue, re-sent
	third := create("req-3", toolsB, base.Add(2*time.Second)) // catalogue changed

	if first.ToolsHash == "" {
		t.Fatal("first response has no tools_hash; the catalogue was not decoded")
	}
	if len(first.Tools) != 1 || first.Tools[0].Name != "exec" || !first.Tools[0].Strict {
		t.Fatalf("first response tools = %+v, want one ToolDef named exec, strict", first.Tools)
	}
	if second.ToolsHash != first.ToolsHash {
		t.Errorf("second response tools_hash = %q, want %q (byte-identical catalogue)",
			second.ToolsHash, first.ToolsHash)
	}
	if len(second.Tools) != 0 {
		t.Errorf("second response repopulated Tools (%d entries) for an UNCHANGED catalogue; "+
			"the dedup is not holding and every response would carry the full body", len(second.Tools))
	}
	if third.ToolsHash == first.ToolsHash {
		t.Error("third response's changed catalogue hashed the same as the first")
	}
	if len(third.Tools) != 2 {
		t.Errorf("third response tools = %d entries, want 2; a CHANGED catalogue must repopulate", len(third.Tools))
	}
}

// The server runs memory-consolidation responses CONCURRENTLY with the user's turn on
// the SAME thread_id, and each keeps its own logical-turn counter series starting at
// num_engine_calls=1. A single per-thread baseline therefore diffs one series against
// the other's last reading, and the response that follows an interleaved memory
// response absorbs the whole of the memory series as its own delta.
//
// The numbers below are the measured shape of that failure on the 2026-08-07 corpus,
// thread 019fdba7 at 11:06:04: the turn series went 731,429 -> 916,716 engine prompt
// tokens (a true delta of 185,287) but was diffed against the memory series' 121,470,
// reporting 795,246 - a 4.3x over-count. The only visible symptom was that sampled
// tokens went NEGATIVE, which is why the guard test below is worth having at all.
func TestReducer_InterleavedMemoryKeepsItsOwnBaseline(t *testing.T) {
	const thread = "019fdba7-23e6-7bc1-96ec-7c30f4c1d3e5"

	// One response: create (declaring its request_kind), timing (the cumulative
	// reading), then completed.
	respond := func(r *Reducer, req, kind string, calls, sampled, prompt int, ts time.Time) *Turn {
		meta, err := json.Marshal(map[string]any{
			"thread_id": thread, "request_kind": kind,
		})
		if err != nil {
			t.Fatal(err)
		}
		var out *Turn
		for i, ev := range []string{
			fmt.Sprintf(`{"type":"response.create","model":"gpt-5.6-sol","client_metadata":`+
				`{"thread_id":%q,"x-codex-turn-metadata":%q}}`, thread, meta),
			fmt.Sprintf(`{"type":"responsesapi.websocket_timing","timing_metrics":{`+
				`"timing_scope":"logical_turn","num_engine_calls":%d,`+
				`"num_sampled_tokens_total":%d,"engine_total_prompt_tokens_total":%d}}`,
				calls, sampled, prompt),
			`{"type":"response.completed","response":{"status":"completed","model":"gpt-5.6-sol"}}`,
		} {
			done, err := r.Add(&frame.Record{
				RequestID: req,
				Headers:   frame.Headers{"thread-id": thread, "originator": "codex-tui"},
				Timestamp: ts.Add(time.Duration(i) * time.Millisecond),
				Payload:   frame.Payload{Text: ev},
			})
			if err != nil {
				t.Fatal(err)
			}
			if done != nil {
				out = done
			}
		}
		if out == nil {
			t.Fatalf("%s: no turn emitted", req)
		}
		return out
	}

	base := time.Date(2026, 8, 7, 11, 5, 51, 0, time.UTC)
	r := New()

	// The user's turn, mid-flight: engine call 4.
	respond(r, "ws_169efba", frame.KindTurn, 4, 1509, 731429, base)
	// A memory response interleaves, on the same thread, with its own series at call 1.
	mem := respond(r, "ws_a784f96", frame.KindMemory, 1, 2600, 121470, base.Add(10*time.Second))
	// The user's turn continues at engine call 5.
	turn := respond(r, "ws_edfb8e1", frame.KindTurn, 5, 2115, 916716, base.Add(13*time.Second))

	if got, want := turn.EnginePromptTokensDelta, 916716-731429; got != want {
		t.Errorf("engine prompt delta = %d, want %d; the turn series was diffed against "+
			"the interleaved memory series", got, want)
	}
	if got, want := turn.SampledTokensDelta, 2115-1509; got != want {
		t.Errorf("sampled delta = %d, want %d", got, want)
	}
	if turn.BaselineReset {
		t.Error("the turn series had a baseline; it must not be flagged as a cold start")
	}
	// The memory response is genuinely the first of its own series, so its full
	// cumulative reading IS its delta - and it must say so.
	if got, want := mem.EnginePromptTokensDelta, 121470; got != want {
		t.Errorf("memory prompt delta = %d, want %d", got, want)
	}
	if !mem.BaselineReset {
		t.Error("the memory response opened a new series with no baseline and must be " +
			"flagged, or its delta reads as a measurement rather than an upper bound")
	}
}

// The per-tool-call duration array is cumulative across a logical turn and only
// grows, so Turn.ToolCallDurationsMs must carry a SUFFIX DIFF - entries newly added
// since the previous response of the same series - and never re-emit anything a
// prior response of the same series already reported.
//
// The third response also exercises the independent safety net: #12's corpus
// comment measured the array shrinking in exactly the responses where the
// engine-calls turn-boundary detector also fires (300/8,663 pairs, matching
// exactly), but that is an empirical property of THIS corpus, not a protocol
// guarantee - so a length decrease is treated as its own reset signal even while
// num_engine_calls keeps climbing normally, which is what response 3 below does.
func TestReducer_ToolCallDurationsAreSuffixDiffed(t *testing.T) {
	const thread = "thread-suffix-diff"
	r := New()
	base := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)

	respond := func(req string, calls int, arr []float64, ts time.Time) *Turn {
		arrJSON, err := json.Marshal(arr)
		if err != nil {
			t.Fatal(err)
		}
		var out *Turn
		for i, ev := range []string{
			fmt.Sprintf(`{"type":"response.create","model":"gpt-5.6-sol",`+
				`"client_metadata":{"thread_id":%q}}`, thread),
			fmt.Sprintf(`{"type":"responsesapi.websocket_timing","timing_metrics":{`+
				`"timing_scope":"logical_turn","num_engine_calls":%d,`+
				`"responsesapi_duration_excl_engine_per_tool_call_ms":%s}}`, calls, arrJSON),
			`{"type":"response.completed","response":{"status":"completed","model":"gpt-5.6-sol"}}`,
		} {
			done, err := r.Add(&frame.Record{
				RequestID: req,
				Headers:   frame.Headers{"thread-id": thread, "originator": "codex-tui"},
				Timestamp: ts.Add(time.Duration(i) * time.Millisecond),
				Payload:   frame.Payload{Text: ev},
			})
			if err != nil {
				t.Fatal(err)
			}
			if done != nil {
				out = done
			}
		}
		if out == nil {
			t.Fatalf("%s: no turn emitted", req)
		}
		return out
	}

	r1 := respond("req-1", 1, []float64{1, 2}, base)
	r2 := respond("req-2", 2, []float64{1, 2, 3, 4}, base.Add(5*time.Second))
	r3 := respond("req-3", 3, []float64{9}, base.Add(10*time.Second))

	if got, want := r1.ToolCallDurationsMs, []float64{1, 2}; !floatSliceEqual(got, want) {
		t.Errorf("response 1 durations = %v, want %v (first sighting, whole array)", got, want)
	}
	if got, want := r2.ToolCallDurationsMs, []float64{3, 4}; !floatSliceEqual(got, want) {
		t.Errorf("response 2 durations = %v, want %v (suffix only - must not re-emit 1,2)", got, want)
	}
	if got, want := r3.ToolCallDurationsMs, []float64{9}; !floatSliceEqual(got, want) {
		t.Errorf("response 3 durations = %v, want %v (array shrank while engine_calls kept "+
			"advancing - the shrink itself must be treated as a reset)", got, want)
	}
}

func floatSliceEqual(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The HTTP family arrived in the 2026-08-07 capture, so it can finally be proven
// against real records rather than constructed ones.
//
// The worry it settles is id-space collision. Both families key the reducer on
// request_id, but the HTTP family's is a UUID where the websocket family's is
// "ws_<hex32>" - and #3 records that Postgres uses request_id for something else
// again. A collision would not error; it would merge two unrelated responses into one
// turn, which is why this asserts the shapes are disjoint rather than trusting them to
// be. It also checks the turns are genuinely reduced and not merely classified: an
// HTTP turn with no model or no tokens is the silent-degradation failure.
func TestReducer_HTTPFamilyReducesLikeAnyOther(t *testing.T) {
	turns := reduceUntil(t, "HTTP-family traffic (a UUID request id)", haveHTTPFamily)

	byShape := map[string]set{}
	var http, degraded int
	for _, x := range turns {
		if x.RequestID == "" {
			continue
		}
		shape := "uuid"
		if strings.HasPrefix(x.RequestID, "ws_") {
			shape = "ws_"
		}
		if byShape[shape] == nil {
			byShape[shape] = set{}
		}
		byShape[shape].add(x.RequestID)

		if x.Family != frame.FamilyHTTP {
			continue
		}
		http++
		if x.Status != "completed" {
			continue // an error or an interrupted response legitimately has neither
		}
		if x.Model == "" || x.TotalTokens == 0 || x.ThreadID == "" {
			degraded++
			t.Errorf("HTTP turn %s completed but reduced to model=%q tokens=%d thread=%q",
				x.RequestID, x.Model, x.TotalTokens, x.ThreadID)
		}
	}

	for id := range byShape["uuid"] {
		if byShape["ws_"][id] {
			t.Errorf("request id %q appears in both id spaces; the two families would "+
				"merge into one turn", id)
		}
	}
	t.Logf("%d HTTP turns (%d degraded); %d distinct uuid ids vs %d ws_ ids, disjoint",
		http, degraded, len(byShape["uuid"]), len(byShape["ws_"]))
}

// Multimodal input must be COUNTED and never CARRIED.
//
// An image arrives as an inline base64 data URI - 784 KB for one screenshot in the
// captured traffic. Loki discards an over-long line whole rather than truncating it,
// so a single inlined screenshot would destroy the user message it belongs to. Before
// 2026-08-07 the opposite failure was live: flattenText finds no text field on an
// image part, so an image-only message flattened to "" and was dropped entirely, with
// nothing recording that an image had ever been sent.
func TestReducer_ImagesAreCountedNotCarried(t *testing.T) {
	turns := reduceUntil(t, "multimodal input (an image part on response.create)", haveImages)

	var withImages, images int
	for _, x := range turns {
		if x.InputImages == 0 {
			continue
		}
		withImages++
		images += x.InputImages
		for _, p := range x.Prompts {
			if p.Images == 0 {
				continue
			}
			// The prefix is enough: a data URI declares itself before the payload, and
			// asserting on the whole body would put a copy of it in the failure output.
			if strings.Contains(p.Text, "data:image") || strings.Contains(p.Text, ";base64,") {
				t.Errorf("prompt text carries image payload (%d chars) - a Loki line over "+
					"the limit is dropped whole, taking the message with it", len(p.Text))
			}
		}
	}
	t.Logf("%d turns carried %d newly-seen image parts", withImages, images)
}

// The media type is read from the data URI's own declaration, never sniffed from the
// payload and never guessed - so it stays bounded, and an image that declares nothing
// reports nothing rather than reporting a plausible lie.
func TestImageParts_MediaTypeIsDeclaredNotGuessed(t *testing.T) {
	for _, tc := range []struct {
		name     string
		content  string
		wantN    int
		wantMIME string
	}{
		{"png data uri", `[{"type":"input_image","image_url":"data:image/png;base64,iVBORw0KGgo"}]`, 1, "image/png"},
		{"jpeg data uri", `[{"type":"input_image","image_url":"data:image/jpeg;base64,/9j/4AAQ"}]`, 1, "image/jpeg"},
		// No media type declared: RFC 2397 says that defaults to text/plain, but
		// asserting a default the sender did not write is a guess like any other.
		{"no media type", `[{"type":"input_image","image_url":"data:;base64,AAAA"}]`, 1, ""},
		{"remote url", `[{"type":"input_image","image_url":"https://example.invalid/a.png"}]`, 1, ""},
		{"text only", `[{"type":"input_text","text":"hi"}]`, 0, ""},
		// An object-valued image_url must still COUNT. A typed string here would fail
		// the whole array decode and silently report zero images.
		{"object image_url", `[{"type":"input_image","image_url":{"url":"data:image/png;base64,AA"}}]`, 1, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, mime, fp := imageParts(json.RawMessage(tc.content))
			if n != tc.wantN {
				t.Errorf("count = %d, want %d", n, tc.wantN)
			}
			if mime != tc.wantMIME {
				t.Errorf("mime = %q, want %q", mime, tc.wantMIME)
			}
			if tc.wantN > 0 && fp == "" {
				t.Error("fingerprint is empty; two different images would dedup to each other")
			}
			if strings.Contains(fp, "base64,") && len(fp) > 80 {
				t.Errorf("fingerprint is carrying payload, not a bounded prefix: %d bytes", len(fp))
			}
		})
	}
}

// The corpus proves the request kinds the reducer's series key depends on, and any
// kind it does NOT name is one whose counter series is being diffed against another's.
func TestReducer_RequestKindsAreNamed(t *testing.T) {
	kinds := set{}
	for _, x := range reduceFiles(t, fixture.All(t)...) {
		kinds.add(x.RequestKind)
	}
	known := map[string]bool{
		frame.KindTurn: true, frame.KindPrewarm: true,
		frame.KindCompaction: true, frame.KindMemory: true,
	}
	for k := range kinds {
		if !known[k] {
			t.Errorf("request_kind %q is in the corpus but not named in internal/frame. "+
				"Each kind runs its own cumulative counter series concurrently on the same "+
				"thread, so an unnamed one is not a labelling gap - it is a series being "+
				"diffed against the wrong baseline. Add it and re-check the deltas.", k)
		}
	}
	t.Logf("request kinds present: %v", kinds.keys())
}

// The requested and the served service tier are two fields and must stay two fields.
//
// Constructed rather than corpus-driven on purpose: the disagreement this pins is a
// property of the wire protocol, but "priority" only entered the capture at
// 2026-08-08T12:13:41Z, so a corpus test would assert nothing at all against any
// archive set older than that - and would do it silently, which is the exact failure
// mode internal/fixture exists to prevent. The values below are the real observed
// combination (asked priority, served default), not an invented one.
func TestReducer_RequestedAndServedServiceTiersAreSeparate(t *testing.T) {
	r := New()
	base := time.Date(2026, 8, 8, 12, 13, 41, 0, time.UTC)
	var got *Turn
	for i, ev := range []struct {
		dir  string
		text string
	}{
		{frame.ToServer, `{"type":"response.create","model":"gpt-5.6-sol",` +
			`"service_tier":"priority","reasoning":{"effort":"medium"}}`},
		// The server reports "auto" while streaming and settles on "default" - both
		// are fed, because the last one wins and the completed value is the one the
		// contract calls served.
		{frame.ToCodex, `{"type":"response.created","response":{"id":"resp_x",` +
			`"model":"gpt-5.6-sol","service_tier":"auto"}}`},
		{frame.ToCodex, `{"type":"response.completed","response":{"id":"resp_x",` +
			`"model":"gpt-5.6-sol","status":"completed","service_tier":"default",` +
			`"usage":{"input_tokens":12,"output_tokens":3,"total_tokens":15}}}`},
	} {
		done, err := r.Add(&frame.Record{
			RequestID: "ws_481fa8fa32724155a2ff8f372d7448ce",
			Kind:      "responses",
			Transport: "websocket",
			Direction: ev.dir,
			Headers:   frame.Headers{"originator": "codex-tui"},
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Payload:   frame.Payload{Text: ev.text},
		})
		if err != nil {
			t.Fatal(err)
		}
		if done != nil {
			got = done
		}
	}
	if got == nil {
		t.Fatal("no turn emitted")
	}
	if got.ServiceTierRequested != "priority" {
		t.Errorf("requested tier = %q, want priority - response.create's top-level "+
			"service_tier is the only record of what was asked for", got.ServiceTierRequested)
	}
	if got.ServiceTier != "default" {
		t.Errorf("served tier = %q, want default", got.ServiceTier)
	}
}

// The *float64/*int/*bool idiom on timingEvent exists so a genuinely missing
// measurement is distinguishable from one reported as exactly zero - the trap is a
// silently-absent field reading as 0ms and pulling an average toward zero for every
// response that simply never populated it. This pins the decode layer directly:
// omitting a key must leave the pointer nil, and an explicit zero/false must NOT.
func TestTimingEvent_AbsentFieldsDecodeAsNilNotZero(t *testing.T) {
	var absent timingEvent
	raw := []byte(`{"timing_metrics":{"timing_scope":"logical_turn","num_engine_calls":1}}`)
	if err := json.Unmarshal(raw, &absent); err != nil {
		t.Fatal(err)
	}
	m := absent.M
	if m.ServiceInferenceMs != nil {
		t.Errorf("engine_service_inference_total_ms absent from the wire but decoded as %v, not nil", *m.ServiceInferenceMs)
	}
	if m.ServiceTBTMs != nil {
		t.Errorf("engine_service_tbt_across_engine_calls_ms absent but decoded as %v, not nil", *m.ServiceTBTMs)
	}
	if m.ToolCallDurationsMs != nil {
		t.Errorf("per-tool-call duration array absent but decoded as %v, not nil", *m.ToolCallDurationsMs)
	}
	if m.ToolCallDurationsTruncated != nil {
		t.Errorf("truncation flag absent but decoded as %v, not nil", *m.ToolCallDurationsTruncated)
	}
	if m.UncachedPromptTokensTotal != nil {
		t.Errorf("engine_uncached_prompt_tokens_total absent but decoded as %v, not nil", *m.UncachedPromptTokensTotal)
	}

	// Contrast: an explicit zero/false DOES decode to a non-nil pointer holding that
	// value. If this half failed instead, the pointer idiom would not be doing
	// anything - both cases would look identical and "absent" would be meaningless.
	var explicit timingEvent
	raw2 := []byte(`{"timing_metrics":{"timing_scope":"logical_turn","num_engine_calls":1,` +
		`"engine_service_inference_total_ms":0,` +
		`"responsesapi_duration_excl_engine_per_tool_call_truncated":false}}`)
	if err := json.Unmarshal(raw2, &explicit); err != nil {
		t.Fatal(err)
	}
	if explicit.M.ServiceInferenceMs == nil || *explicit.M.ServiceInferenceMs != 0 {
		t.Errorf("explicit 0 must decode to a non-nil pointer holding 0, got %v", explicit.M.ServiceInferenceMs)
	}
	if explicit.M.ToolCallDurationsTruncated == nil || *explicit.M.ToolCallDurationsTruncated {
		t.Errorf("explicit false must decode to a non-nil pointer holding false, got %v", explicit.M.ToolCallDurationsTruncated)
	}

	// And the reducer must not crash or fabricate a value when applyTiming sees the
	// all-absent case end to end.
	tn := &Turn{ThreadID: "t-absent", ItemCounts: map[string]int{}}
	r := New()
	r.applyTiming(tn, frame.Event{Raw: raw})
	if tn.EngineServiceTBTMs != 0 || tn.ToolCallDurationsMs != nil || tn.ToolCallDurationsTruncated {
		t.Errorf("applyTiming fabricated a value from an absent field: tbt=%v durations=%v truncated=%v",
			tn.EngineServiceTBTMs, tn.ToolCallDurationsMs, tn.ToolCallDurationsTruncated)
	}
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

func haveHTTPFamily(turns []*Turn) bool {
	for _, x := range turns {
		// A completed one: the transport-only turns are classified without ever being
		// reduced, so they would satisfy the predicate without exercising anything.
		if x.Family == frame.FamilyHTTP && x.Status == "completed" {
			return true
		}
	}
	return false
}

func haveImages(turns []*Turn) bool {
	for _, x := range turns {
		if x.InputImages > 0 {
			return true
		}
	}
	return false
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
