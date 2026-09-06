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

func TestCallIndexBoundsResidentThreadsAndEvictsOldestNewestEntry(t *testing.T) {
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	var calls callIndex
	for i := 0; i < callIndexThreadLimit; i++ {
		thread := fmt.Sprintf("thread-%04d", i)
		calls.record(thread, "call", callRef{CapturedAt: base.Add(time.Duration(i) * time.Second)})
	}
	calls.record("thread-overflow", "call", callRef{CapturedAt: base.Add(callIndexThreadLimit * time.Second)})

	if got := len(calls.threads); got != callIndexThreadLimit {
		t.Fatalf("resident threads = %d, want %d", got, callIndexThreadLimit)
	}
	if _, ok := calls.threads["thread-0000"]; ok {
		t.Fatal("thread with oldest newest entry survived eviction")
	}
	if _, ok := calls.threads["thread-overflow"]; !ok {
		t.Fatal("newly recorded thread was not retained")
	}
}

func TestCallIndexEvictsLexicallySmallestThreadOnNewestEntryTie(t *testing.T) {
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	var calls callIndex
	for i := 0; i < callIndexThreadLimit; i++ {
		calls.record(fmt.Sprintf("thread-%04d", i), "call", callRef{CapturedAt: base})
	}
	calls.record("thread-overflow", "call", callRef{CapturedAt: base})

	if _, ok := calls.threads["thread-0000"]; ok {
		t.Fatal("lexically smallest tied thread survived eviction")
	}
}

func TestCallIndexKeepsNewerResidentsWhenIncomingThreadIsOldest(t *testing.T) {
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	t.Run("older incoming entry", func(t *testing.T) {
		var calls callIndex
		for i := 0; i < callIndexThreadLimit; i++ {
			calls.record(fmt.Sprintf("thread-%04d", i), "call", callRef{CapturedAt: base.Add(time.Duration(i) * time.Second)})
		}
		calls.record("incoming-oldest", "call", callRef{CapturedAt: base.Add(-time.Second)})

		if _, ok := calls.threads["incoming-oldest"]; ok {
			t.Fatal("older incoming thread displaced a newer resident")
		}
		if _, ok := calls.threads["thread-0000"]; !ok {
			t.Fatal("newer resident was evicted for older incoming thread")
		}
	})
	t.Run("lexically smallest tied incoming entry", func(t *testing.T) {
		var calls callIndex
		for i := 0; i < callIndexThreadLimit; i++ {
			calls.record(fmt.Sprintf("thread-%04d", i), "call", callRef{CapturedAt: base})
		}
		calls.record("a-incoming", "call", callRef{CapturedAt: base})

		if _, ok := calls.threads["a-incoming"]; ok {
			t.Fatal("lexically smallest tied incoming thread displaced a resident")
		}
		if _, ok := calls.threads["thread-0000"]; !ok {
			t.Fatal("tied resident was evicted instead of lexically smallest incoming thread")
		}
	})
}

func TestCallIndexExpiryDropsEmptyThreads(t *testing.T) {
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	var calls callIndex
	calls.record("expired", "call", callRef{CapturedAt: base})
	calls.record("live", "call", callRef{CapturedAt: base.Add(callIndexMaxAge + time.Second)})

	if _, ok := calls.threads["expired"]; ok {
		t.Fatal("thread whose entries all expired remained resident")
	}
}

func TestCallIndexOccurrenceSurvivesConsumedCall(t *testing.T) {
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	var calls callIndex
	if got := calls.record("thread", "call", callRef{CapturedAt: base}); got != 1 {
		t.Fatalf("first occurrence = %d, want 1", got)
	}
	if _, match := calls.lookup("thread", "call"); match != "exact" {
		t.Fatalf("lookup match = %q, want exact", match)
	}
	if got := calls.record("thread", "call", callRef{CapturedAt: base.Add(time.Second)}); got != 2 {
		t.Fatalf("occurrence after consumed call = %d, want 2", got)
	}
}

func TestCallIndexOccurrenceResetsWhenBoundedHistoryIsRemoved(t *testing.T) {
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	t.Run("expiry", func(t *testing.T) {
		var calls callIndex
		calls.record("thread", "call", callRef{CapturedAt: base})
		calls.advance(base.Add(callIndexMaxAge + time.Second))
		calls.pruneAll()
		if got := calls.record("thread", "call", callRef{CapturedAt: base.Add(callIndexMaxAge + 2*time.Second)}); got != 1 {
			t.Fatalf("occurrence after expiry = %d, want reset to 1", got)
		}
	})
	t.Run("per-thread cap", func(t *testing.T) {
		var calls callIndex
		calls.record("thread", "call", callRef{CapturedAt: base})
		for i := 0; i < callIndexLimit; i++ {
			calls.record("thread", fmt.Sprintf("other-%03d", i), callRef{CapturedAt: base.Add(time.Duration(i+1) * time.Second)})
		}
		if got := calls.record("thread", "call", callRef{CapturedAt: base.Add((callIndexLimit + 1) * time.Second)}); got != 1 {
			t.Fatalf("occurrence after per-thread cap = %d, want reset to 1", got)
		}
	})
	t.Run("thread eviction", func(t *testing.T) {
		var calls callIndex
		calls.record("thread", "call", callRef{CapturedAt: base})
		for i := 0; i < callIndexThreadLimit-1; i++ {
			calls.record(fmt.Sprintf("other-%04d", i), "call", callRef{CapturedAt: base.Add(time.Duration(i+1) * time.Second)})
		}
		calls.record("overflow", "call", callRef{CapturedAt: base.Add(callIndexThreadLimit * time.Second)})
		if got := calls.record("thread", "call", callRef{CapturedAt: base.Add((callIndexThreadLimit + 1) * time.Second)}); got != 1 {
			t.Fatalf("occurrence after thread eviction = %d, want reset to 1", got)
		}
	})
}

// Global expiry is amortized; the active thread still expires on every record.
func TestCallIndexExpirySweepCadence(t *testing.T) {
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	var calls callIndex
	calls.record("inactive", "call", callRef{CapturedAt: base})
	calls.record("active", "call", callRef{CapturedAt: base})
	boundary := base.Add(callIndexMaxAge)
	calls.record("clock", "call", callRef{CapturedAt: boundary})
	if got := calls.record("active", "call", callRef{CapturedAt: boundary.Add(time.Second)}); got != 1 {
		t.Fatalf("expired active occurrence = %d, want 1", got)
	}
	if _, ok := calls.threads["inactive"]; !ok {
		t.Fatal("inactive thread swept before the next minute boundary")
	}
	calls.record("clock", "next", callRef{CapturedAt: boundary.Add(time.Minute)})
	if _, ok := calls.threads["inactive"]; ok {
		t.Fatal("expired inactive thread survived next sweep")
	}
}
