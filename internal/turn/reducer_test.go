package turn

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/rknightion/codexlb2otel/internal/archive"
	"github.com/rknightion/codexlb2otel/internal/frame"
)

// These tests run against real captured archive hours. They deliberately assert
// INVARIANTS rather than exact counts: the fixtures get refreshed from camden as new
// traffic shapes turn up, and pinning "1245 responses" only produced churn. What
// matters is that the reduction stays self-consistent whatever the input.
const (
	hour17 = "2026-08-06T17.jsonl.gz"
	hour18 = "2026-08-06T18.jsonl.gz"
	hour21 = "2026-08-06T21.jsonl.gz"
)

func reduceFixtures(t *testing.T, names ...string) []*Turn {
	t.Helper()
	r := New()
	var out []*Turn
	for _, name := range names {
		out = append(out, feed(t, r, name)...)
	}
	out = append(out, r.Flush()...)
	sortByStart(out)
	return out
}

func feed(t *testing.T, r *Reducer, name string) []*Turn {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "live", name))
	if err != nil {
		t.Skipf("live fixture absent (%v); pull one from camden to run this", err)
	}
	res, err := archive.DecodeMembers(data)
	if err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
	}
	return out
}

// The delta conversion is the correctness core. The server's timing metrics are
// cumulative over a logical turn, so summing them raw overcounts by ~5.7x. Summed as
// deltas they must instead track the independent per-response usage figure.
func TestReducer_DeltasTrackUsage(t *testing.T) {
	turns := reduceFixtures(t, hour17, hour18, hour21)
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
	for _, x := range reduceFixtures(t, hour17, hour18, hour21) {
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
	turns := reduceFixtures(t, hour17, hour18, hour21)

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
	for _, x := range reduceFixtures(t, hour17, hour18, hour21) {
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
	full := reduceFixtures(t, hour17, hour18)

	r1 := New()
	feed(t, r1, hour17)
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
	resumed := feed(t, r2, hour18)

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
	turns := reduceFixtures(t, hour17, hour18, hour21)

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
