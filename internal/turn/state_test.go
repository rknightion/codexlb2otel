package turn

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/rknightion/codexlb2otel/internal/frame"
)

func TestReducer_StateSnapshotPersistsSeriesTimestamps(t *testing.T) {
	r := New()
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	completeTimedTurn(t, r, "thread-state", frame.KindTurn, "req-1", 1, 10, base)

	key := "thread-state\x00" + frame.KindTurn
	got := r.Snapshot()
	if got.PrevSeen[key] != base.Add(time.Millisecond) {
		t.Fatalf("PrevSeen[%q] = %s, want the newest archive event timestamp for the series",
			key, got.PrevSeen[key])
	}
	if got.SeqSeen["thread-state"] != base.Add(time.Millisecond) {
		t.Fatalf("SeqSeen[thread-state] = %s, want newest archive event timestamp for the thread",
			got.SeqSeen["thread-state"])
	}
}

func TestReducer_SequenceFreshnessLivesOnReducer(t *testing.T) {
	r := New()
	base := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	completeTimedTurn(t, r, "seq-freshness", frame.KindTurn, "req-seq", 1, 10, base)

	if got, want := r.seqSeen["seq-freshness"], base.Add(time.Millisecond); got != want {
		t.Fatalf("reducer sequence freshness = %s, want newest sequence event %s", got, want)
	}
}

func TestReducer_RestoreOldStateTimestampsDefaultToLoadTime(t *testing.T) {
	loadedAt := time.Date(2026, 8, 10, 12, 30, 0, 0, time.UTC)
	old := State{
		Version: 3,
		Prev:    map[string]cumulative{"old-thread\x00" + frame.KindTurn: {engineCalls: 7}},
		Seq:     map[string]int{"old-thread": 4},
	}

	r := New()
	r.RestoreAt(old, loadedAt)
	got := r.Snapshot()

	if got.PrevSeen["old-thread\x00"+frame.KindTurn] != loadedAt {
		t.Fatalf("upgraded PrevSeen timestamp = %s, want load time %s",
			got.PrevSeen["old-thread\x00"+frame.KindTurn], loadedAt)
	}
	if got.SeqSeen["old-thread"] != loadedAt {
		t.Fatalf("upgraded SeqSeen timestamp = %s, want load time %s",
			got.SeqSeen["old-thread"], loadedAt)
	}
}

func TestReducer_EvictStateRemovesDeadSeriesAndThreadsOnly(t *testing.T) {
	r := New()
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	completeTimedTurn(t, r, "dead", frame.KindTurn, "req-dead", 1, 10, base)
	completeTimedTurn(t, r, "live", frame.KindTurn, "req-live", 1, 10, base.Add(time.Hour))
	completeTimedTurn(t, r, "open", frame.KindTurn, "req-open-base", 1, 10, base)

	openKey := "open\x00" + frame.KindTurn
	r.open["req-open-live"] = &Turn{
		RequestID:   "req-open-live",
		ThreadID:    "open",
		RequestKind: frame.KindTurn,
		FirstTS:     base.Add(2 * time.Hour),
		LastTS:      base.Add(2 * time.Hour),
		ItemCounts:  map[string]int{},
	}

	seriesRemoved, threadsRemoved := r.EvictState(base.Add(30 * time.Minute))
	if seriesRemoved != 1 || threadsRemoved != 1 {
		t.Fatalf("removed series=%d threads=%d, want 1 and 1", seriesRemoved, threadsRemoved)
	}
	if _, ok := r.prev["dead\x00"+frame.KindTurn]; ok {
		t.Fatal("dead series baseline survived eviction")
	}
	if _, ok := r.seq["dead"]; ok {
		t.Fatal("dead thread sequence survived eviction")
	}
	if _, ok := r.prev["live\x00"+frame.KindTurn]; !ok {
		t.Fatal("live series baseline was evicted")
	}
	if _, ok := r.seq["live"]; !ok {
		t.Fatal("live thread sequence was evicted")
	}
	if _, ok := r.prev[openKey]; !ok {
		t.Fatal("open response's series baseline was evicted mid-turn")
	}
	if _, ok := r.seq["open"]; !ok {
		t.Fatal("open response's thread sequence was evicted mid-turn")
	}
}

func TestReducer_EvictedReturningSeriesIsBaselineReset(t *testing.T) {
	r := New()
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	completeTimedTurn(t, r, "returning", frame.KindTurn, "req-base", 1, 10, base)

	r.EvictState(base.Add(5 * time.Second))
	got := completeTimedTurn(t, r, "returning", frame.KindTurn, "req-return", 100, 500, base.Add(9*time.Second))

	if !got.BaselineReset {
		t.Fatal("returning series after state eviction was not flagged BaselineReset")
	}
	if got.EnginePromptTokensDelta != 500 {
		t.Fatalf("prompt delta = %d, want full current reading 500; old baseline was silently diffed",
			got.EnginePromptTokensDelta)
	}
}

func completeTimedTurn(t *testing.T, r *Reducer, thread, kind, req string, calls, prompt int, ts time.Time) *Turn {
	t.Helper()
	meta, err := json.Marshal(map[string]any{
		"thread_id": thread, "request_kind": kind,
	})
	if err != nil {
		t.Fatal(err)
	}
	var out *Turn
	events := []string{
		`{"type":"response.create","model":"gpt-5.6-sol","client_metadata":{"thread_id":"` +
			thread + `","x-codex-turn-metadata":` + strconvQuote(string(meta)) + `}}`,
		`{"type":"responsesapi.websocket_timing","timing_metrics":{"timing_scope":"logical_turn",` +
			`"num_engine_calls":` + strconvItoa(calls) + `,"engine_total_prompt_tokens_total":` + strconvItoa(prompt) + `}}`,
		`{"type":"response.completed","response":{"status":"completed","model":"gpt-5.6-sol"}}`,
	}
	for i, text := range events {
		done, err := r.Add(&frame.Record{
			RequestID: req,
			Headers:   frame.Headers{"thread-id": thread, "originator": "codex-tui"},
			Timestamp: ts.Add(time.Duration(i) * time.Millisecond),
			Payload:   frame.Payload{Text: text},
		})
		if err != nil {
			t.Fatal(err)
		}
		if done != nil {
			out = done
		}
	}
	if out == nil {
		t.Fatalf("%s: no completed turn", req)
	}
	return out
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func strconvItoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
