package attr

import (
	"fmt"
	"regexp"
	"sort"
	"sync"
	"testing"

	"github.com/rknightion/codexlb2otel/internal/archive"
	"github.com/rknightion/codexlb2otel/internal/fixture"
	"github.com/rknightion/codexlb2otel/internal/frame"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

var (
	corpusOnce  sync.Once
	corpusCache []*turn.Turn
	corpusErr   error
)

// corpusTurns reduces the WHOLE corpus, once per process.
//
// It read the two cheapest archives until 2026-08-07, and that was the weaker half of
// the same mistake issue #20 turned out to be: a cap check is only evidence for the
// traffic it actually saw, and two archives of fourteen is a sample, not the corpus.
// request_kind="memory" - the value whose absence from the contract caused #20's silent
// 4.3x token over-count - is exactly the shape of thing a cheap sample misses, because
// a rare enum value is rare in every file including the small ones. The acceptance
// criterion on #3 says "reduce the corpus"; this now does.
//
// Memoized because both tests here want the same reduction and doing it twice would
// double the most expensive thing in the package for no additional coverage. The
// failure is memoized too rather than being raised with t.Fatal from inside the Once:
// Fatal is a Goexit, which would still mark the Once done and leave the second test
// reporting "the corpus reduced to no turns" instead of the real cause.
func corpusTurns(t *testing.T) []*turn.Turn {
	t.Helper()

	// fixture.All is called OUTSIDE the memo deliberately. It is what SKIPS the test
	// when no corpus is present (CLB_NO_CORPUS, which is exactly how CI runs), and a
	// skip is a runtime.Goexit - inside the Once it would mark the Once done while
	// leaving the cache empty, so the first test would skip correctly and every later
	// one would run against zero turns and fail with a misleading "the corpus reduced
	// to no turns". That is precisely how this repo's first CI run failed. Same Goexit
	// hazard the error path below already guards against, arriving through the skip
	// path instead. Listing the directory is cheap; only the reduction is memoized.
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

// idShaped matches values that would be a cardinality incident as a metric attribute:
// a UUID, one of the archive's prefixed ids, or a long bare hex run.
var idShaped = regexp.MustCompile(
	`^([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-|resp_|ws_|ctc_|msg_|rs_|fc_|turn_)|^[0-9a-fA-F]{24,}$`)

// The contract claims these fields are bounded. This is where that claim meets the
// real capture rather than a comment - and where an id arriving in a field the
// contract calls an enum is caught before it becomes a series per request.
func TestCorpus_BoundedFieldsStayWithinTheirCaps(t *testing.T) {
	turns := corpusTurns(t)
	if len(turns) == 0 {
		t.Fatal("the corpus reduced to no turns; this test asserts nothing")
	}

	values := map[string]map[string]bool{}
	for _, tn := range turns {
		for _, f := range Fields() {
			// Of == nil is a caller-supplied field (tool name, token type): not derivable
			// from a Turn, so there is nothing to sample here. Guard.With caps those.
			if f.Class != Bounded || f.Of == nil {
				continue
			}
			v := f.Of(tn)
			if v == "" {
				continue
			}
			if values[f.Key] == nil {
				values[f.Key] = map[string]bool{}
			}
			values[f.Key][v] = true
		}
	}

	for _, f := range Fields() {
		if f.Class != Bounded || f.Of == nil {
			continue
		}
		seen := values[f.Key]
		if len(seen) > f.Cap {
			t.Errorf("%s took %d distinct values across %d turns, cap is %d - either the cap "+
				"is wrong or the field is not the enum this contract claims",
				f.Key, len(seen), len(turns), f.Cap)
		}
		for v := range seen {
			if f.IDLike {
				continue // see Field.IDLike; the cap above is what keeps this safe
			}
			if idShaped.MatchString(v) {
				t.Errorf("%s carried an id-shaped value %q; as a metric attribute that is a "+
					"series per request", f.Key, v)
			}
		}
	}

	// Not an assertion, but the number that matters when reading a failure above.
	for _, f := range Fields() {
		if f.Class == Bounded && f.Of != nil && len(values[f.Key]) > 0 {
			t.Logf("%-38s %2d/%d distinct  %v", f.Key, len(values[f.Key]), f.Cap, sorted(values[f.Key]))
		}
	}
}

// deliberateIdentityOverlap is the allowlist of (metric attribute -> identity field)
// pairs that carry the SAME value on purpose, keyed by the metric attribute.
//
// The leak test below is a value-equality check, not a provenance check - it cannot
// tell "this bounded field accidentally got handed an id" from "these two keys are two
// routings of one deliberately-shared value". Exactly one pair is the latter, so it is
// named here rather than the test being weakened for everything else. Anything not on
// this list that matches by value is still a hard failure.
var deliberateIdentityOverlap = map[string]string{
	// gen_ai.agent.version IS codexlb.instructions_hash (issue #32) - see that
	// constant's doc comment for why the system prompt's identity is the agent's
	// version. The pairing is the point: a version in agent-observability's UI is
	// meant to grep straight back to the Loki lines carrying the same hash.
	//
	// It is safe where a real leak would not be for the reason the leak test exists to
	// catch: this value does not vary per request. It is sha256 of a system prompt
	// codex ships, so it changes when codex ships a new prompt and not otherwise, and
	// the field is capped at 32 with IDLike set precisely because its shape would
	// otherwise trip the sibling id-shape heuristic in
	// TestCorpus_BoundedFieldsStayBounded.
	GenAIAgentVersion: InstructionsHash,
}

// The leak test, run against real records rather than a constructed turn: no value
// that this package routes to a metric attribute may equal an identity value from the
// same turn, unless the pair is on deliberateIdentityOverlap. That is the mistake no
// amount of careful naming prevents on its own.
func TestCorpus_NoIdentityValueReachesAMetricAttribute(t *testing.T) {
	g := NewGuard()
	turns := corpusTurns(t)
	if len(turns) == 0 {
		t.Fatal("the corpus reduced to no turns; this test asserts nothing")
	}

	checked := 0
	for _, tn := range turns {
		ids := map[string]string{}
		for _, f := range Fields() {
			if f.Class == Bounded || f.Of == nil {
				continue
			}
			if v := f.Of(tn); v != "" {
				ids[v] = f.Key
			}
		}
		if len(ids) == 0 {
			continue
		}
		checked++
		for _, kv := range g.MetricAttrs(tn) {
			from, isID := ids[kv.Value]
			if !isID {
				continue
			}
			// Allowed only for the exact pair declared, not for any identity value
			// that happens to collide with an allowlisted attribute.
			if deliberateIdentityOverlap[kv.Key] == from {
				continue
			}
			t.Fatalf("metric attribute %s carries %s's value %q", kv.Key, from, kv.Value)
		}
	}
	if checked == 0 {
		t.Fatal("no turn carried any identity field; the leak check ran against nothing")
	}
	t.Logf("checked %d turns carrying identity fields", checked)
}

func sorted(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
