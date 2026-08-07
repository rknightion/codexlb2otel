package agento11y

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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
		User:       "1217581",
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
