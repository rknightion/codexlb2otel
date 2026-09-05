package turn

import (
	"encoding/json"
	"testing"
	"time"
)

func TestStateCallIndexRoundTripDoesNotAliasSnapshot(t *testing.T) {
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	r1 := New()
	r1.calls.record("thread-a", "call-a", callRef{ResponseID: "response-a", CapturedAt: base})
	s := r1.Snapshot()

	s.Calls.record("thread-a", "call-snapshot", callRef{CapturedAt: base})
	if _, match := r1.calls.lookup("thread-a", "call-snapshot"); match != "none" {
		t.Fatalf("snapshot mutation changed reducer: match = %q", match)
	}
	checkpoint, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var restored State
	if err := json.Unmarshal(checkpoint, &restored); err != nil {
		t.Fatal(err)
	}

	r2 := New()
	r2.RestoreAt(restored, base)
	if got, match := r2.calls.lookup("thread-a", "call-a"); match != "exact" || got.ResponseID != "response-a" {
		t.Fatalf("restored lookup = (%#v, %q), want response-a exact", got, match)
	}
}

func TestRestoreVersionFourKeepsBaselinesAndDropsCallIndex(t *testing.T) {
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	r := New()
	r.calls.record("thread-a", "call-a", callRef{CapturedAt: base})
	s := State{
		Version: 4,
		Prev:    map[string]cumulative{"thread-a\x00default": {engineCalls: 7}},
		Seq:     map[string]int{"thread-a": 3},
		Calls:   r.calls.snapshot(),
	}

	r2 := New()
	r2.RestoreAt(s, base)
	if got := r2.prev["thread-a\x00default"].engineCalls; got != 7 {
		t.Errorf("v4 baseline = %d, want 7", got)
	}
	if got := r2.seq["thread-a"]; got != 3 {
		t.Errorf("v4 sequence = %d, want 3", got)
	}
	if _, match := r2.calls.lookup("thread-a", "call-a"); match != "none" {
		t.Errorf("v4 call-index lookup match = %q, want none", match)
	}
}
