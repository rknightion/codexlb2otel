package loki

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rknightion/codexlb2otel/internal/attr"
	"github.com/rknightion/codexlb2otel/internal/sink"
)

// pushRequest is the Loki JSON push payload. Verified against current Loki docs
// (docs/sources/reference/loki-http-api.md via context7, 2026-08-07) rather than
// assumed from memory: streams carry a flat label map and a values array of
// [unix-nanos-as-string, line, structured-metadata-object] tuples, POSTed as
// application/json to <url>. Success is 204 with an empty body.
type pushRequest struct {
	Streams []pushStream `json:"streams"`
}

type pushStream struct {
	Stream map[string]string `json:"stream"`
	Values [][]any           `json:"values"`
}

// buildPushRequest groups lines into streams by their exact label set - a Loki
// stream IS a label set, and pushing the same label set twice in one request as two
// separate stream entries is both wasteful and not what the wire format expects.
func buildPushRequest(lines []outLine) pushRequest {
	streams := map[string]*pushStream{}
	order := make([]string, 0, len(lines))

	for _, l := range lines {
		key := streamKey(l.labels)
		ps, ok := streams[key]
		if !ok {
			ps = &pushStream{Stream: kvMap(l.labels)}
			streams[key] = ps
			order = append(order, key)
		}
		ps.Values = append(ps.Values, []any{
			strconv.FormatInt(l.ts.UnixNano(), 10),
			string(l.body),
			kvMap(l.metadata),
		})
	}

	out := pushRequest{Streams: make([]pushStream, 0, len(order))}
	for _, k := range order {
		out.Streams = append(out.Streams, *streams[k])
	}
	return out
}

// streamKey is a canonical string for a label set. attr.Guard.Labels already
// returns its result sorted by key, so simple concatenation is a stable key without
// re-sorting here.
func streamKey(labels []attr.KV) string {
	var b strings.Builder
	for _, kv := range labels {
		b.WriteString(kv.Key)
		b.WriteByte('=')
		b.WriteString(kv.Value)
		b.WriteByte(0)
	}
	return b.String()
}

// kvMap renders an attribute set for the wire, translating the dotted OTel key into
// the underscored name Loki's label grammar requires. This is the only place a key
// crosses into Loki, so attr.LokiKey is applied here and nowhere else - see its
// comment for the 400 that made this necessary.
func kvMap(kvs []attr.KV) map[string]string {
	m := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		m[attr.LokiKey(kv.Key)] = kv.Value
	}
	return m
}

// push delivers one batch, retrying a retryable failure with backoff up to
// cfg.MaxRetries times before giving up.
//
// The two failure classes are handled deliberately differently, per the binding
// acceptance criteria on issues #5 and #6:
//
//   - A permanent 4xx (line_too_long, out_of_order, a bad request shape, an auth
//     failure) will never succeed by resending the same bytes. It is classified,
//     counted, and the push returns nil - the batch is treated as delivered from the
//     caller's point of view, specifically so the checkpoint is free to advance past
//     data that is going to be rejected identically forever. Retrying it would wedge
//     the checkpoint on an unfixable line.
//   - A 429 or 5xx (or a transport-level failure - DNS, connection refused, a
//     timeout) might well succeed on the next attempt, so it is retried. Only once
//     retries are exhausted is it counted and turned into an error, which is what
//     tells the caller to hold the checkpoint and try these turns again later.
func (s *Sink) push(ctx context.Context, lines []outLine) error {
	if len(lines) == 0 {
		return nil
	}
	body, err := json.Marshal(buildPushRequest(lines))
	if err != nil {
		return fmt.Errorf("loki: marshaling push request: %w", err)
	}

	maxRetries := s.cfg.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}

	var lastErr error
	for attempt := 0; ; attempt++ {
		status, respBody, doErr := s.doOnce(ctx, body)
		switch {
		case doErr != nil:
			lastErr = doErr
			if attempt >= maxRetries {
				s.addRejected(sink.ReasonTransport, int64(len(lines)))
				return fmt.Errorf("loki: push failed after %d attempt(s): %w", attempt+1, lastErr)
			}

		case status/100 == 2:
			return nil

		case status == http.StatusTooManyRequests || status >= 500:
			lastErr = fmt.Errorf("loki: push returned %d: %s", status, snippet(respBody))
			if attempt >= maxRetries {
				reason := sink.ReasonServerError
				if status == http.StatusTooManyRequests {
					reason = sink.ReasonRateLimited
				}
				s.addRejected(reason, int64(len(lines)))
				return fmt.Errorf("loki: push failed after %d attempt(s): %w", attempt+1, lastErr)
			}

		case status >= 400:
			reason := classify4xx(status, respBody)
			s.addRejected(reason, int64(len(lines)))
			// Not every permanent 4xx is the same kind of permanent.
			//
			// line_too_long, out_of_order and stream_limit are verdicts on the DATA:
			// resending is futile, the checkpoint should advance past it, and dropping
			// is the decision issues #5 and #6 settled.
			//
			// A malformed request or an auth failure is a verdict on the SERVICE. It
			// will reject every future push identically, so dropping silently means
			// running forever and delivering nothing - which is exactly what happened
			// on the first live run, when dotted label names 400'd the whole batch and
			// the service still wrote a clean checkpoint. Those must surface as errors
			// so the checkpoint holds and somebody sees it.
			if configFault(reason) {
				return fmt.Errorf("loki: push rejected as %s (this will not fix itself): %d: %s",
					reason, status, snippet(respBody))
			}
			return nil

		default:
			// An unexpected but non-error status (e.g. a 1xx or 3xx the client
			// didn't already resolve). Treat as delivered rather than looping
			// forever on a shape this sink has never seen Loki produce.
			return nil
		}

		select {
		case <-time.After(backoff(attempt)):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *Sink) doOnce(ctx context.Context, body []byte) (status int, respBody []byte, err error) {
	reqCtx := ctx
	if s.cfg.Timeout > 0 {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(ctx, s.cfg.Timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, s.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(s.cfg.User, s.token)

	resp, err := s.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, b, nil
}

// backoff is exponential with a 30s cap. attempt is clamped before shifting so a
// generously configured MaxRetries cannot overflow the shift.
func backoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if attempt > 10 {
		attempt = 10
	}
	d := 250 * time.Millisecond * time.Duration(1<<attempt)
	if d <= 0 || d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}

// reasonMatchers classifies a permanent-4xx response body by substring match.
//
// Loki's own docs (operations/request-validation-rate-limits.md) state that "the
// offending stream will be returned in the body of the HTTP response" but do not
// pin an exact wire format for that text - it is prose describing operator-facing
// behavior, not a documented response schema. Matching is therefore deliberately
// loose: both the underscored reason token Loki uses in its own
// loki_discarded_samples_total metric (e.g. "line_too_long") and the more
// conversational phrasing operators report seeing in error bodies (e.g. "line too
// long") are matched, so this does not depend on a client library or curl demo
// actually being generated to see one.
var reasonMatchers = []struct {
	reason  string
	substrs []string
}{
	{sink.ReasonLineTooLong, []string{"line_too_long", "line too long", "entry too long", "max_line_size", "max line size"}},
	{sink.ReasonOutOfOrder, []string{"out_of_order", "out of order", "too_far_behind", "too far behind"}},
	{sink.ReasonStreamLimit, []string{"stream_limit", "streams_limit", "max_streams_per_user", "max_global_streams", "streams limit", "too many streams"}},
	{sink.ReasonRateLimited, []string{"rate_limited", "rate limit", "per_stream_rate_limit", "too many requests", "ingestion rate limit"}},
	{sink.ReasonUnauthorized, []string{"unauthorized", "invalid credentials", "authentication"}},
}

func classify4xx(status int, body []byte) string {
	lower := strings.ToLower(string(body))
	for _, m := range reasonMatchers {
		for _, s := range m.substrs {
			if strings.Contains(lower, s) {
				return m.reason
			}
		}
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return sink.ReasonUnauthorized
	default:
		return sink.ReasonBadRequest
	}
}

func snippet(b []byte) string {
	const max = 500
	if len(b) > max {
		return string(b[:max]) + "…"
	}
	return string(b)
}

// configFault reports whether a rejection reason indicts the service's own
// configuration rather than the data it happened to be carrying.
//
// The distinction decides whether a permanent 4xx is survivable. A data verdict is
// dropped so the checkpoint can move past bytes that will always be rejected; a
// configuration verdict is returned as an error, because every subsequent push will
// fail the same way and silently advancing would deliver nothing for as long as
// nobody looked.
func configFault(reason string) bool {
	return reason == sink.ReasonBadRequest || reason == sink.ReasonUnauthorized
}
