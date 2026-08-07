package agento11y

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/rknightion/codexlb2otel/internal/attr"
	"github.com/rknightion/codexlb2otel/internal/config"
	"github.com/rknightion/codexlb2otel/internal/sink"
)

// newTestSink points a Sink at an httptest.Server - never a real endpoint, per the
// issue's own requirement (a prior test in this repo "passed" only because a wrong
// token meant it never actually reached Grafana; hermetic here means literally
// impossible for that to recur).
func newTestSink(t *testing.T, handler http.HandlerFunc) (*Sink, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	s, err := New(config.AgentO11y{
		Enabled:    true,
		URL:        srv.URL,
		User:       "654321",
		Token:      config.Secret("test-token-not-real"),
		BatchSize:  10,
		BatchWait:  time.Second,
		Timeout:    5 * time.Second,
		MaxRetries: 0,
	}, attr.NewGuard())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, srv
}

// TestPush_PerGenerationRejectionIsCounted is the hard-constraint test: a 200 whose
// results[] says accepted:false for one generation out of two must be counted as one
// rejection, not folded into "the push succeeded" the way a status-code-only check
// would read it.
func TestPush_PerGenerationRejectionIsCounted(t *testing.T) {
	s, _ := newTestSink(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"results":[
			{"generation_id":"resp_1","accepted":true},
			{"generation_id":"resp_2","accepted":false,"error":"effective_version does not match ^sha256:[0-9a-f]{64}$"}
		]}`)
	})

	err := s.push(context.Background(), []wireGeneration{{ID: "resp_1"}, {ID: "resp_2"}})
	if err != nil {
		// A per-generation rejection is a DATA fault: it must not hold the checkpoint,
		// because retrying an identical payload will fail identically forever.
		t.Fatalf("push returned an error for a per-generation data rejection, want nil: %v", err)
	}

	rej := s.Rejections()
	if len(rej) != 1 || rej[0].Reason != sink.ReasonRejected || rej[0].Count != 1 {
		t.Fatalf("Rejections() = %+v, want exactly one %s:1", rej, sink.ReasonRejected)
	}
}

// TestPush_MissingResultIsCountedAsRejected covers the sibling silent-loss shape: a
// generation sent but never mentioned in results[] at all. Assuming success for a
// generation the server said nothing about would be the same mistake Loki's own 204-
// for-a-discarded-line trap warns against.
func TestPush_MissingResultIsCountedAsRejected(t *testing.T) {
	s, _ := newTestSink(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"results":[{"generation_id":"resp_1","accepted":true}]}`)
	})

	err := s.push(context.Background(), []wireGeneration{{ID: "resp_1"}, {ID: "resp_2"}})
	if err != nil {
		t.Fatalf("push returned an error, want nil: %v", err)
	}
	rej := s.Rejections()
	if len(rej) != 1 || rej[0].Reason != sink.ReasonRejected || rej[0].Count != 1 {
		t.Fatalf("Rejections() = %+v, want exactly one %s:1 for the unmentioned generation", rej, sink.ReasonRejected)
	}
}

// TestPush_ConfigFaultHoldsCheckpoint_DataFaultDoesNot pins the split the issue calls
// out by name: a permanent failure on the request itself (here, 401 - the whole batch
// was rejected by the SERVICE, not because of what it was carrying) must return an
// error so the caller holds the checkpoint, because every subsequent push will fail
// identically until the credential is fixed. A per-generation data rejection (see
// TestPush_PerGenerationRejectionIsCounted above) must not.
func TestPush_ConfigFaultHoldsCheckpoint_DataFaultDoesNot(t *testing.T) {
	t.Run("401 is a config fault: push errors, checkpoint held", func(t *testing.T) {
		s, _ := newTestSink(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, "invalid credentials")
		})

		err := s.push(context.Background(), []wireGeneration{{ID: "resp_1"}})
		if err == nil {
			t.Fatal("push returned nil for a 401, want an error holding the checkpoint")
		}

		rej := s.Rejections()
		if len(rej) != 1 || rej[0].Reason != sink.ReasonUnauthorized || rej[0].Count != 1 {
			t.Fatalf("Rejections() = %+v, want exactly one %s:1", rej, sink.ReasonUnauthorized)
		}
	})

	t.Run("200 with accepted:false is a data fault: push does not error", func(t *testing.T) {
		s, _ := newTestSink(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"results":[{"generation_id":"resp_1","accepted":false,"error":"bad trace_id"}]}`)
		})

		err := s.push(context.Background(), []wireGeneration{{ID: "resp_1"}})
		if err != nil {
			t.Fatalf("push returned an error for a data fault, want nil: %v", err)
		}
		rej := s.Rejections()
		if len(rej) != 1 || rej[0].Reason != sink.ReasonRejected || rej[0].Count != 1 {
			t.Fatalf("Rejections() = %+v, want exactly one %s:1", rej, sink.ReasonRejected)
		}
	})

	// A 413 is the trap in the middle: permanent like a 401, but caused by the DATA,
	// so treating it as a config fault would hold the checkpoint on a batch that can
	// never drain and the pipeline would wedge on it forever. A batch is 200
	// generations carrying prompts up to 32 KB each, so this is reachable.
	t.Run("413 is a data fault: too big to ever accept, so the checkpoint must advance", func(t *testing.T) {
		s, _ := newTestSink(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			fmt.Fprint(w, "payload too large")
		})

		if err := s.push(context.Background(), []wireGeneration{{ID: "resp_1"}}); err != nil {
			t.Fatalf("push returned an error for a 413, which would wedge the pipeline "+
				"on a batch that can never succeed: %v", err)
		}
		rej := s.Rejections()
		if len(rej) != 1 || rej[0].Reason != sink.ReasonLineTooLong || rej[0].Count != 1 {
			t.Fatalf("Rejections() = %+v, want exactly one %s:1", rej, sink.ReasonLineTooLong)
		}
	})
}

// TestPush_SplitOnTooLarge_DeliversIndividually is item 3's headline case: a batch
// that 413s must not be dropped wholesale, it must be halved and retried until the
// halves are small enough to be accepted. The handler here accepts up to maxAccepted
// generations per request and 413s anything larger, so an 8-generation push must
// halve at least twice (8 -> 4 -> 2) before any sub-batch clears - proving push()
// actually retries the smaller halves rather than happening to succeed on the first
// try, and that every generation is eventually delivered rather than some being
// silently dropped along the way.
func TestPush_SplitOnTooLarge_DeliversIndividually(t *testing.T) {
	const maxAccepted = 3

	var mu sync.Mutex
	delivered := map[string]bool{}

	s, _ := newTestSink(t, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("handler: reading request body: %v", err)
		}
		var req wireExportGenerationsRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("handler: decoding request body: %v", err)
		}

		if len(req.Generations) > maxAccepted {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			fmt.Fprint(w, "payload too large")
			return
		}

		mu.Lock()
		results := make([]wireExportGenerationResult, 0, len(req.Generations))
		for _, g := range req.Generations {
			delivered[g.ID] = true
			results = append(results, wireExportGenerationResult{GenerationID: g.ID, Accepted: true})
		}
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(wireExportGenerationsResponse{Results: results}); err != nil {
			t.Fatalf("handler: encoding response: %v", err)
		}
	})

	var gens []wireGeneration
	for i := 0; i < 8; i++ {
		gens = append(gens, wireGeneration{ID: fmt.Sprintf("resp_%d", i)})
	}

	if err := s.push(context.Background(), gens); err != nil {
		t.Fatalf("push returned an error, want nil: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(delivered) != len(gens) {
		t.Fatalf("delivered %d of %d generations: %v", len(delivered), len(gens), delivered)
	}
	for _, g := range gens {
		if !delivered[g.ID] {
			t.Errorf("generation %s was never delivered", g.ID)
		}
	}

	if rej := s.Rejections(); len(rej) != 0 {
		t.Fatalf("Rejections() = %+v, want none - every generation was eventually delivered", rej)
	}
}

// TestPush_SplitOnTooLarge_SingleOversizedGenerationCountsOnce covers the floor the
// split must not erode: a generation that is too large even alone is genuinely
// undeliverable, and halving must count it exactly once at the leaf - not once per
// halving step it passed through on the way down. The handler 413s every request
// regardless of size, so all 4 generations bottom out as singletons; if the split
// double-counted at an intermediate level (e.g. also counting the length-2 and
// length-4 attempts before recursing) the total would be well above 4.
func TestPush_SplitOnTooLarge_SingleOversizedGenerationCountsOnce(t *testing.T) {
	s, _ := newTestSink(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		fmt.Fprint(w, "payload too large")
	})

	gens := []wireGeneration{{ID: "resp_1"}, {ID: "resp_2"}, {ID: "resp_3"}, {ID: "resp_4"}}
	if err := s.push(context.Background(), gens); err != nil {
		t.Fatalf("push returned an error for a 413 that never clears, want nil: %v", err)
	}

	rej := s.Rejections()
	if len(rej) != 1 || rej[0].Reason != sink.ReasonLineTooLong || rej[0].Count != int64(len(gens)) {
		t.Fatalf("Rejections() = %+v, want exactly one %s:%d (one per undeliverable singleton, no double-counting)",
			rej, sink.ReasonLineTooLong, len(gens))
	}
}

// TestPush_ConfigFaultInsideSplitPropagates pins the requirement that splitting must
// never swallow a config fault: a batch that 413s while larger than 1 generation gets
// halved down, but if a half then hits a genuine config fault (modeled here as a 401
// once a request is down to a single generation), that error must still propagate all
// the way out of the original push() call and hold the checkpoint - exactly as it
// would have without any splitting involved.
func TestPush_ConfigFaultInsideSplitPropagates(t *testing.T) {
	s, _ := newTestSink(t, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("handler: reading request body: %v", err)
		}
		var req wireExportGenerationsRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("handler: decoding request body: %v", err)
		}

		if len(req.Generations) > 1 {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			fmt.Fprint(w, "payload too large")
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, "invalid credentials")
	})

	gens := []wireGeneration{{ID: "resp_1"}, {ID: "resp_2"}, {ID: "resp_3"}, {ID: "resp_4"}}
	if err := s.push(context.Background(), gens); err == nil {
		t.Fatal("push returned nil for a config fault reached via splitting, want an error holding the checkpoint")
	}
}
