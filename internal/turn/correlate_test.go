package turn

import (
	"fmt"
	"testing"
	"time"
)

func TestCallIndexMatchesAcrossResponsesAndConsumesEntry(t *testing.T) {
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	var calls callIndex
	calls.advance(base)
	calls.record("thread-a", "call-1", callRef{
		ResponseID: "response-a", TurnID: "turn-a", ToolName: "tool-a", CapturedAt: base,
	})

	got, match := calls.lookup("thread-a", "call-1")
	if match != "exact" {
		t.Fatalf("match = %q, want exact", match)
	}
	if got.ResponseID != "response-a" || got.TurnID != "turn-a" || got.ToolName != "tool-a" {
		t.Fatalf("lookup = %#v, want recorded origin", got)
	}
	if _, match := calls.lookup("thread-a", "call-1"); match != "none" {
		t.Errorf("duplicate output match = %q, want none", match)
	}
}

func TestCallIndexDoesNotCrossThreadsAndReportsAmbiguity(t *testing.T) {
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	var calls callIndex
	calls.record("thread-a", "shared", callRef{ResponseID: "response-a", CapturedAt: base})
	calls.record("thread-b", "shared", callRef{ResponseID: "response-b", CapturedAt: base})

	got, match := calls.lookup("thread-b", "shared")
	if match != "exact" || got.ResponseID != "response-b" {
		t.Fatalf("thread-b lookup = (%#v, %q), want response-b exact", got, match)
	}
	if got, match := calls.lookup("thread-a", "shared"); match != "exact" || got.ResponseID != "response-a" {
		t.Fatalf("thread-a lookup = (%#v, %q), want response-a exact", got, match)
	}

	calls.record("thread-c", "duplicate", callRef{ResponseID: "response-c1", CapturedAt: base})
	calls.record("thread-c", "duplicate", callRef{ResponseID: "response-c2", CapturedAt: base})
	if got, match := calls.lookup("thread-c", "duplicate"); match != "ambiguous" || got != (callRef{}) {
		t.Errorf("ambiguous lookup = (%#v, %q), want empty ambiguous", got, match)
	}
	if _, match := calls.lookup("thread-c", "missing"); match != "none" {
		t.Errorf("missing lookup match = %q, want none", match)
	}
}

func TestCallIndexEvictsByArchiveAgeAndPerThreadLimit(t *testing.T) {
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	var calls callIndex
	calls.record("thread-age", "old", callRef{CapturedAt: base})
	calls.advance(base.Add(24*time.Hour + time.Second))
	if _, match := calls.lookup("thread-age", "old"); match != "none" {
		t.Fatalf("expired lookup match = %q, want none", match)
	}
	var global callIndex
	global.record("thread-a", "old", callRef{CapturedAt: base})
	global.advance(base.Add(24*time.Hour + time.Second))
	global.record("thread-b", "new", callRef{CapturedAt: base.Add(24*time.Hour + time.Second)})
	if got := global.entries("thread-a"); got != 0 {
		t.Fatalf("inactive-thread entries after insert = %d, want 0", got)
	}

	for i := 0; i < 10_000; i++ {
		id := fmt.Sprintf("call-%05d", i)
		calls.record("thread-bound", id, callRef{CapturedAt: base.Add(time.Duration(i) * time.Second)})
	}
	if got := calls.entries("thread-bound"); got != 512 {
		t.Fatalf("retained entries = %d, want 512", got)
	}
	if _, match := calls.lookup("thread-bound", "call-00000"); match != "none" {
		t.Errorf("evicted oldest lookup match = %q, want none", match)
	}
	if _, match := calls.lookup("thread-bound", "call-09999"); match != "exact" {
		t.Errorf("newest lookup match = %q, want exact", match)
	}
}
