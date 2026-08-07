package attr

import (
	"regexp"
	"sort"
	"testing"

	"github.com/rknightion/codexlb2otel/internal/archive"
	"github.com/rknightion/codexlb2otel/internal/fixture"
	"github.com/rknightion/codexlb2otel/internal/frame"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

// corpusTurns reduces the n cheapest archives. Mirrors internal/turn's own helper;
// duplicated rather than exported because a test helper is not API.
func corpusTurns(t *testing.T, n int) []*turn.Turn {
	t.Helper()
	r := turn.New()
	var out []*turn.Turn
	for _, path := range fixture.Any(t, n) {
		res, err := archive.DecodeMembers(fixture.Load(t, path))
		if err != nil {
			t.Fatalf("%s: %v", fixture.Name(path), err)
		}
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
	}
	return append(out, r.Flush()...)
}

// idShaped matches values that would be a cardinality incident as a metric attribute:
// a UUID, one of the archive's prefixed ids, or a long bare hex run.
var idShaped = regexp.MustCompile(
	`^([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-|resp_|ws_|ctc_|msg_|rs_|fc_|turn_)|^[0-9a-fA-F]{24,}$`)

// The contract claims these fields are bounded. This is where that claim meets the
// real capture rather than a comment - and where an id arriving in a field the
// contract calls an enum is caught before it becomes a series per request.
func TestCorpus_BoundedFieldsStayWithinTheirCaps(t *testing.T) {
	turns := corpusTurns(t, 2)
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

// The leak test, run against real records rather than a constructed turn: no value
// that this package routes to a metric attribute may equal an identity value from the
// same turn. That is the mistake no amount of careful naming prevents on its own.
func TestCorpus_NoIdentityValueReachesAMetricAttribute(t *testing.T) {
	g := NewGuard()
	turns := corpusTurns(t, 2)
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
			if from, isID := ids[kv.Value]; isID {
				t.Fatalf("metric attribute %s carries %s's value %q", kv.Key, from, kv.Value)
			}
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
