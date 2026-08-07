package loki

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rknightion/codexlb2otel/internal/attr"
	"github.com/rknightion/codexlb2otel/internal/config"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

func testCfg(url string) config.Loki {
	return config.Loki{
		Enabled:      true,
		URL:          url,
		User:         "123456",
		Token:        config.Secret("test-token"),
		Labels:       append([]string(nil), attr.DefaultLabels...),
		MaxLineBytes: 192 << 10,
		BatchSize:    1000,
		BatchWait:    time.Hour, // effectively "only push on Flush" for these tests
		Timeout:      5 * time.Second,
		MaxRetries:   2,
	}
}

func sampleTurn() *turn.Turn {
	now := time.Now().UTC()
	return &turn.Turn{
		RequestID:         "req-1",
		ResponseID:        "resp-1",
		ThreadID:          "thread-1",
		Model:             "gpt-5.6-terra",
		Status:            "completed",
		Family:            "websocket",
		FirstTS:           now.Add(-2 * time.Second),
		LastTS:            now,
		ServerCompletedAt: now,
		InputTokens:       100,
		OutputTokens:      50,
		Messages:          []turn.Message{{Phase: "final_answer", Chars: 5, Text: "hello"}},
	}
}

// TestSink_PermanentFourXX_CountedAndDroppedNotRetried is the acceptance-criteria
// case from issue #5: "a permanent 4xx is counted and dropped, not retried
// forever."
func TestSink_PermanentFourXX_CountedAndDroppedNotRetried(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`entry with timestamp ... ignored, reason: 'line_too_long' for stream: {...}`))
	}))
	defer srv.Close()

	s, err := New(testCfg(srv.URL+"/loki/api/v1/push"), attr.NewGuard(), "codexlb2otel")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Emit(context.Background(), []*turn.Turn{sampleTurn()}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("Flush should NOT return an error for a permanent 4xx (would wedge the checkpoint): %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("permanent 4xx must not be retried: server hit %d times, want 1", got)
	}
	rejs := s.Rejections()
	if len(rejs) != 1 || rejs[0].Reason != "line_too_long" || rejs[0].Count == 0 {
		t.Fatalf("Rejections() = %+v, want one line_too_long entry", rejs)
	}
}

// TestSink_RetryableFailure_RetriedThenSucceeds proves 429/5xx are retried with
// backoff rather than immediately dropped like a permanent 4xx.
func TestSink_RetryableFailure_RetriedThenSucceeds(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	cfg := testCfg(srv.URL + "/loki/api/v1/push")
	cfg.MaxRetries = 5
	s, err := New(cfg, attr.NewGuard(), "codexlb2otel")
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Emit(context.Background(), []*turn.Turn{sampleTurn()})
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("Flush should succeed once the transient failures stop: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Fatalf("expected 2 failures + 1 success = 3 requests, got %d", got)
	}
	if rejs := s.Rejections(); len(rejs) != 0 {
		t.Fatalf("a delivery that eventually succeeded must not be counted as rejected: %+v", rejs)
	}
}

// TestSink_RetriesExhausted_ReturnsErrorAndCountsIt is the other half of the
// retryable path: once MaxRetries is exhausted the caller must see an error (so it
// holds the checkpoint - "push failure does not advance the checkpoint"), and the
// failure must still be visible via Rejections.
func TestSink_RetriesExhausted_ReturnsErrorAndCountsIt(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	cfg := testCfg(srv.URL + "/loki/api/v1/push")
	cfg.MaxRetries = 2
	s, err := New(cfg, attr.NewGuard(), "codexlb2otel")
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Emit(context.Background(), []*turn.Turn{sampleTurn()})
	if err := s.Flush(context.Background()); err == nil {
		t.Fatal("Flush must return an error once retries on a 429 are exhausted")
	}
	if got, want := atomic.LoadInt32(&hits), int32(cfg.MaxRetries+1); got != want {
		t.Fatalf("expected %d requests (1 initial + %d retries), got %d", want, cfg.MaxRetries, got)
	}
	rejs := s.Rejections()
	if len(rejs) != 1 || rejs[0].Reason != "rate_limited" {
		t.Fatalf("Rejections() = %+v, want one rate_limited entry", rejs)
	}
}

// TestSink_TransportFailure_ReturnsErrorAfterRetries covers a network-level failure
// (no HTTP response at all) rather than an HTTP status - the sink must not confuse
// "could not reach Loki" with "Loki rejected this".
func TestSink_TransportFailure_ReturnsErrorAfterRetries(t *testing.T) {
	cfg := testCfg("http://127.0.0.1:1/loki/api/v1/push") // nothing listens on port 1
	cfg.MaxRetries = 1
	cfg.Timeout = 500 * time.Millisecond
	s, err := New(cfg, attr.NewGuard(), "codexlb2otel")
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Emit(context.Background(), []*turn.Turn{sampleTurn()})
	if err := s.Flush(context.Background()); err == nil {
		t.Fatal("Flush must return an error when Loki cannot be reached at all")
	}
	rejs := s.Rejections()
	if len(rejs) != 1 || rejs[0].Reason != "transport" {
		t.Fatalf("Rejections() = %+v, want one transport entry", rejs)
	}
}

// TestSink_WireFormat pins the push payload shape against what issue #5 specifies
// and what context7's current Loki docs confirm: streams carry a flat label map and
// [ts_ns_string, line, metadata_object] value tuples, POSTed as application/json
// with HTTP basic auth.
func TestSink_WireFormat(t *testing.T) {
	var gotAuthUser, gotAuthPass string
	var gotAuthOK bool
	var gotContentType string
	var gotBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthUser, gotAuthPass, gotAuthOK = r.BasicAuth()
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = readAll(r)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	s, err := New(testCfg(srv.URL+"/loki/api/v1/push"), attr.NewGuard(), "codexlb2otel")
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Emit(context.Background(), []*turn.Turn{sampleTurn()})
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	if !gotAuthOK || gotAuthUser != "123456" || gotAuthPass != "test-token" {
		t.Fatalf("basic auth = (%q,%q,%v), want (123456,test-token,true)", gotAuthUser, gotAuthPass, gotAuthOK)
	}
	if gotContentType != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", gotContentType)
	}

	var payload struct {
		Streams []struct {
			Stream map[string]string `json:"stream"`
			Values [][]json.RawMessage
		} `json:"streams"`
	}
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("push body is not valid JSON: %v\nbody: %s", err, gotBody)
	}
	if len(payload.Streams) == 0 {
		t.Fatal("push body has no streams")
	}
	st := payload.Streams[0]
	if st.Stream["service_name"] != "codexlb2otel" {
		t.Fatalf("stream labels = %+v, want service_name=codexlb2otel", st.Stream)
	}
	if len(st.Values) == 0 {
		t.Fatal("stream has no values")
	}
	v := st.Values[0]
	if len(v) != 3 {
		t.Fatalf("value tuple has %d elements, want 3 (ts, line, metadata)", len(v))
	}
	var ts string
	if err := json.Unmarshal(v[0], &ts); err != nil || len(ts) < 18 {
		t.Fatalf("value[0] = %s, want a nanosecond-epoch string", v[0])
	}
	var line string
	if err := json.Unmarshal(v[1], &line); err != nil {
		t.Fatalf("value[1] is not a JSON string: %s", v[1])
	}
	var meta map[string]string
	if err := json.Unmarshal(v[2], &meta); err != nil {
		t.Fatalf("value[2] is not a JSON object: %s", v[2])
	}
	if meta["codexlb_request_id"] != "req-1" {
		t.Fatalf("structured metadata = %+v, want codexlb_request_id=req-1", meta)
	}
}

// Loki's label grammar is Prometheus's, so a dot inside braces is a parse error and
// the whole push 400s. That cost a run that consumed an archive, wrote a clean
// checkpoint and delivered nothing - the keys are OTel-dotted and nothing translated
// them. Asserted on both label and metadata keys, since both are label-grammar names.
func TestPushRequest_NoKeyCarriesADotIntoLoki(t *testing.T) {
	lines := buildLines(sampleTurn(), attr.NewGuard(), "codexlb2otel",
		attr.DefaultLabels, 192<<10, nil, &fakeRejecter{counts: map[string]int64{}})
	if len(lines) == 0 {
		t.Fatal("no lines built; this test asserts nothing")
	}

	req := buildPushRequest(lines)
	if len(req.Streams) == 0 {
		t.Fatal("no streams built")
	}
	for _, st := range req.Streams {
		for k := range st.Stream {
			if strings.Contains(k, ".") {
				t.Errorf("stream label %q contains a dot; Loki 400s the whole push", k)
			}
		}
		for _, v := range st.Values {
			md, ok := v[2].(map[string]string)
			if !ok {
				t.Fatalf("value[2] is %T, want map[string]string", v[2])
			}
			for k := range md { //nolint:gocritic // key-shape assertion
				if strings.Contains(k, ".") {
					t.Errorf("structured metadata key %q contains a dot", k)
				}
			}
		}
	}
}

func readAll(r *http.Request) ([]byte, error) {
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := r.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	return buf, nil
}

func TestClassify4xx(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"line too long token", 400, `reason: 'line_too_long' for stream`, "line_too_long"},
		{"line too long prose", 400, `rejected: line too long for this tenant`, "line_too_long"},
		{"out of order", 400, `entry for stream out of order`, "out_of_order"},
		{"stream limit", 400, `max_streams_per_user exceeded`, "stream_limit"},
		{"rate limited body on 400", 400, `per_stream_rate_limit exceeded`, "rate_limited"},
		{"unauthorized body", 401, `invalid credentials`, "unauthorized"},
		{"unauthorized by status only", 401, ``, "unauthorized"},
		{"forbidden by status only", 403, `no message`, "unauthorized"},
		{"unrecognised 400", 400, `something else entirely`, "bad_request"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classify4xx(tc.status, []byte(tc.body))
			if got != tc.want {
				t.Fatalf("classify4xx(%d, %q) = %q, want %q", tc.status, tc.body, got, tc.want)
			}
		})
	}
}

func TestBackoff_BoundedAndIncreasing(t *testing.T) {
	prev := time.Duration(0)
	for attempt := 0; attempt < 8; attempt++ {
		d := backoff(attempt)
		if d <= 0 {
			t.Fatalf("backoff(%d) = %v, must be positive", attempt, d)
		}
		if d > 30*time.Second {
			t.Fatalf("backoff(%d) = %v, exceeds the 30s cap", attempt, d)
		}
		if d < prev {
			t.Fatalf("backoff(%d) = %v is less than backoff(%d) = %v; should not decrease", attempt, d, attempt-1, prev)
		}
		prev = d
	}
	// Must not overflow or panic for a generously configured MaxRetries.
	if d := backoff(1000); d != 30*time.Second {
		t.Fatalf("backoff(1000) = %v, want the 30s cap", d)
	}
}
