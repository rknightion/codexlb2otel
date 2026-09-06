package turn

import (
	"encoding/json"
	"fmt"
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

func TestStateCallIndexCheckpointIsBoundedAndPreservesOccurrences(t *testing.T) {
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	var calls callIndex
	for i := 0; i < 10_000; i++ {
		calls.record(fmt.Sprintf("thread-%05d", i), "call", callRef{CapturedAt: base.Add(time.Duration(i) * time.Millisecond)})
	}
	if got := calls.record("thread-repeat", "call-repeat", callRef{CapturedAt: base.Add(11 * time.Second)}); got != 1 {
		t.Fatalf("first occurrence = %d, want 1", got)
	}
	if _, match := calls.lookup("thread-repeat", "call-repeat"); match != "exact" {
		t.Fatalf("consumed occurrence match = %q, want exact", match)
	}

	checkpoint, err := json.Marshal(State{Version: stateVersion, Calls: calls.snapshot()})
	if err != nil {
		t.Fatal(err)
	}
	const checkpointByteBound = 4 << 20
	if got := len(checkpoint); got > checkpointByteBound {
		t.Fatalf("checkpoint size = %d bytes, want at most %d", got, checkpointByteBound)
	}

	var restored State
	if err := json.Unmarshal(checkpoint, &restored); err != nil {
		t.Fatal(err)
	}
	r := New()
	r.RestoreAt(restored, base)
	if got := r.calls.record("thread-repeat", "call-repeat", callRef{CapturedAt: base.Add(time.Second)}); got != 2 {
		t.Fatalf("restored occurrence = %d, want 2", got)
	}
}

func TestRestoreVersionFiveKeepsBaselinesAndDropsCallIndex(t *testing.T) {
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	r := New()
	r.calls.record("thread-a", "call-a", callRef{CapturedAt: base})
	s := State{
		Version: 5,
		Prev:    map[string]cumulative{"thread-a\x00default": {engineCalls: 7}},
		Seq:     map[string]int{"thread-a": 3},
		Calls:   r.calls.snapshot(),
	}

	r2 := New()
	r2.RestoreAt(s, base)
	if got := r2.prev["thread-a\x00default"].engineCalls; got != 7 {
		t.Errorf("v5 baseline = %d, want 7", got)
	}
	if got := r2.seq["thread-a"]; got != 3 {
		t.Errorf("v5 sequence = %d, want 3", got)
	}
	if _, match := r2.calls.lookup("thread-a", "call-a"); match != "none" {
		t.Errorf("v5 call-index lookup match = %q, want none", match)
	}
}

func TestRestoreVersionSixBoundsOversizedCallIndex(t *testing.T) {
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	threads := make(map[string][]callIndexEntry, callIndexThreadLimit+1)
	for i := 0; i <= callIndexThreadLimit; i++ {
		threads[fmt.Sprintf("thread-%04d", i)] = []callIndexEntry{{CallID: "call", Ref: callRef{CapturedAt: base.Add(time.Duration(i) * time.Second)}}}
	}
	r := New()
	r.RestoreAt(State{Version: stateVersion, Calls: callIndex{clock: base.Add(callIndexThreadLimit * time.Second), threads: threads}}, base)
	if got := len(r.calls.threads); got != callIndexThreadLimit {
		t.Fatalf("restored resident threads = %d, want %d", got, callIndexThreadLimit)
	}
	if _, ok := r.calls.threads["thread-0000"]; ok {
		t.Fatal("oldest restored thread survived bound enforcement")
	}
}
