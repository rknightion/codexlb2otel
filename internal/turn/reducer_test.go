package turn

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/rknightion/codexlb2otel/internal/archive"
	"github.com/rknightion/codexlb2otel/internal/frame"
)

// reduceFixtures runs the full pipeline over the captured archive hours, in order.
func reduceFixtures(t *testing.T, names ...string) []*Turn {
	t.Helper()
	r := New()
	var out []*Turn
	for _, name := range names {
		p := filepath.Join("..", "..", "testdata", "live", name)
		data, err := os.ReadFile(p)
		if err != nil {
			t.Skipf("live fixture absent (%v); pull one from camden to run this", err)
		}
		res, err := archive.DecodeMembers(data)
		if err != nil {
			t.Fatalf("%s: decode: %v", name, err)
		}
		err = frame.Lines(res.Data, func(rec *frame.Record) error {
			done, err := r.Add(rec)
			if err != nil {
				return err
			}
			if done != nil {
				out = append(out, done)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("%s: lines: %v", name, err)
		}
	}
	// The reference extractor emitted every accumulated request_id at end of input,
	// including responses that never completed. Flush to match that.
	out = append(out, r.Flush()...)
	sortByStart(out)
	return out
}

const (
	hour18 = "2026-08-06T18.jsonl.gz"
	hour19 = "2026-08-06T19.jsonl.gz"
	hour20 = "2026-08-06T20.jsonl.gz"
)

// Pinned from the Python reference extractor over the same three hours. These are
// the numbers the whole design was validated against.
func TestReducer_MatchesReferenceExtractor(t *testing.T) {
	turns := reduceFixtures(t, hour18, hour19, hour20)

	if got := len(turns); got != 1245 {
		t.Errorf("responses = %d, want 1245", got)
	}

	logical := map[string]bool{}
	threads := map[string]bool{}
	var input, cached, output, reasoning int
	var promptDelta, sampledDelta int
	models := map[string]int{}
	var subagents int

	for _, x := range turns {
		logical[x.LogicalTurnID] = true
		threads[x.ThreadID] = true
		input += x.InputTokens
		cached += x.CachedTokens
		output += x.OutputTokens
		reasoning += x.ReasoningTokens
		promptDelta += x.EnginePromptTokensDelta
		sampledDelta += x.SampledTokensDelta
		models[x.Model]++
		if x.IsSubagent {
			subagents++
		}
	}

	checks := []struct {
		name      string
		got, want int
	}{
		{"logical turns", len(logical), 87},
		{"threads", len(threads), 25},
		{"subagent responses", subagents, 872},
		{"input tokens", input, 136788705},
		{"cached tokens", cached, 130826240},
		{"output tokens", output, 486062},
		{"reasoning tokens", reasoning, 168736},
		{"sampled-token deltas", sampledDelta, 486062},
		// Deliberately ABOVE the reference extractor's 638/598. That extractor read
		// the model only from response.completed, so it left the 9 responses that
		// never completed unattributed. This reducer also reads response.create and
		// response.created, so those 9 (5 sol, 4 terra) are attributed. Every
		// response therefore carries a model and none is left blank.
		{"gpt-5.6-sol responses", models["gpt-5.6-sol"], 643},
		{"gpt-5.6-terra responses", models["gpt-5.6-terra"], 602},
		{"unattributed responses", models[""], 0},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}

	// The delta conversion is the correctness core. Engine prompt tokens summed as
	// deltas must track the per-response usage figure; summed raw they would be
	// ~5.7x higher. The reference run measured 0.9996.
	ratio := float64(promptDelta) / float64(input)
	if ratio < 0.995 || ratio > 1.005 {
		t.Errorf("engine-prompt-delta / input-tokens = %.4f, want ~0.9996; "+
			"a large value means the cumulative metrics are being summed raw", ratio)
	}
}

// A negative delta means a logical-turn boundary was missed, which silently corrupts
// every downstream counter. Zero were observed across the reference run.
func TestReducer_NoNegativeDeltas(t *testing.T) {
	for _, x := range reduceFixtures(t, hour18, hour19, hour20) {
		if x.EngineCallsDelta < 0 || x.SampledTokensDelta < 0 ||
			x.EnginePromptTokensDelta < 0 || x.EngineCachedTokensDelta < 0 ||
			x.TurnTimeSecondsDelta < 0 || x.ClientToolPauseMsDelta < 0 {
			t.Fatalf("negative delta in %s at %s: calls=%d sampled=%d prompt=%d cached=%d time=%.3f pause=%.3f",
				x.LogicalTurnID, x.FirstTS.Format("15:04:05"),
				x.EngineCallsDelta, x.SampledTokensDelta, x.EnginePromptTokensDelta,
				x.EngineCachedTokensDelta, x.TurnTimeSecondsDelta, x.ClientToolPauseMsDelta)
		}
	}
}

// Guards the cardinality rules that keep the metric pipeline safe.
func TestReducer_CardinalityAssumptions(t *testing.T) {
	turns := reduceFixtures(t, hour18, hour19, hour20)

	models := map[string]bool{}
	efforts := map[string]bool{}
	engines := map[string]bool{}
	for _, x := range turns {
		models[x.Model] = true
		if x.Effort != "" {
			efforts[x.Effort] = true
		}
		if x.EngineIDs != "" {
			engines[x.EngineIDs] = true
		}
	}

	// Safe as metric attributes only while they stay small.
	if len(models) > 10 {
		t.Errorf("%d distinct models; too many to keep as a metric attribute", len(models))
	}
	if len(efforts) > 6 {
		t.Errorf("%d distinct reasoning efforts; unexpected", len(efforts))
	}
	// Documents why engine_ids is a log field: it rotates, and can be a joined list.
	if len(engines) < 2 {
		t.Skip("too few engine ids in fixture to assert rotation")
	}
	t.Logf("engine_ids observed: %d distinct values (log field only, never a metric label)", len(engines))
}

// A restart must not turn the next cumulative reading into a giant fake delta.
func TestReducer_StateSurvivesRestart(t *testing.T) {
	full := reduceFixtures(t, hour18, hour19)

	// Reduce hour 18, snapshot, restore into a fresh Reducer, then continue.
	r1 := New()
	feed(t, r1, hour18)
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
	resumed := feed(t, r2, hour19)

	// Compare against the same responses from the uninterrupted run, keyed by
	// request_id rather than position: a turn spanning the hour boundary is handled
	// differently by the two runs and would shift every index after it.
	want := map[string]*Turn{}
	for _, x := range full {
		want[x.RequestID] = x
	}

	var compared int
	for _, got := range resumed {
		exp, ok := want[got.RequestID]
		if !ok || exp.Status == StatusIncomplete || got.Status == StatusIncomplete {
			continue // spans the restart boundary; neither run can complete it
		}
		compared++
		if got.EnginePromptTokensDelta != exp.EnginePromptTokensDelta {
			t.Errorf("%s: prompt delta after restart = %d, want %d",
				got.RequestID, got.EnginePromptTokensDelta, exp.EnginePromptTokensDelta)
		}
		if got.SampledTokensDelta != exp.SampledTokensDelta {
			t.Errorf("%s: sampled delta after restart = %d, want %d",
				got.RequestID, got.SampledTokensDelta, exp.SampledTokensDelta)
		}
	}
	if compared < 100 {
		t.Fatalf("only compared %d turns; the fixture or keying is wrong", compared)
	}
	t.Logf("verified %d turns identical across a restart", compared)
}

func feed(t *testing.T, r *Reducer, name string) []*Turn {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "live", name))
	if err != nil {
		t.Skipf("live fixture absent (%v)", err)
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
	out = append(out, r.Flush()...)
	sortByStart(out)
	return out
}
