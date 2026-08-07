package agento11y

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/rknightion/codexlb2otel/internal/sink"
)

// push delivers one batch, retrying a retryable failure with backoff up to
// cfg.MaxRetries times - the same two-class retry loop as loki.Sink.push, PLUS a
// third check loki's wire format has no equivalent of: ExportGenerations can return
// 2xx for the request while refusing individual generations within it.
//
// A 200 is NOT proof of delivery here any more than it is for Loki. Loki's version of
// this trap is a 204 for a line silently discarded server-side (loki.Sink.dropTooOld's
// doc comment); sigil's OTLP-span decoder reports success as OTLP partial_success
// while dropping spans that fail its gate (see the issue's own research comment).
// ExportGenerations is at least explicit about it - ExportGenerationsResponse.results
// carries a per-generation accepted/error verdict - but explicit only helps if
// something reads it, so this function always parses the body on a 2xx and never
// treats the status code alone as success.
func (s *Sink) push(ctx context.Context, gens []wireGeneration) error {
	if len(gens) == 0 {
		return nil
	}
	body, err := json.Marshal(wireExportGenerationsRequest{Generations: gens})
	if err != nil {
		return fmt.Errorf("agento11y: marshaling export request: %w", err)
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
				s.addRejected(sink.ReasonTransport, int64(len(gens)))
				return fmt.Errorf("agento11y: push failed after %d attempt(s): %w", attempt+1, lastErr)
			}

		case status/100 == 2:
			return s.accountResults(gens, respBody)

		case status == http.StatusTooManyRequests || status >= 500:
			lastErr = fmt.Errorf("agento11y: push returned %d: %s", status, snippet(respBody))
			if attempt >= maxRetries {
				reason := sink.ReasonServerError
				if status == http.StatusTooManyRequests {
					reason = sink.ReasonRateLimited
				}
				s.addRejected(reason, int64(len(gens)))
				return fmt.Errorf("agento11y: push failed after %d attempt(s): %w", attempt+1, lastErr)
			}

		case status >= 400:
			reason := classify4xx(status)
			s.addRejected(reason, int64(len(gens)))
			// configFault mirrors loki's rule exactly (see its own doc comment there
			// and here): a malformed request or an auth failure indicts THIS
			// SERVICE's configuration, not the generations it happened to be
			// carrying, and will reject every future push identically. Returning an
			// error holds the checkpoint so the failure surfaces instead of running
			// forever while delivering nothing - the exact failure mode that made
			// Loki's dotted-label 400 a live incident on the first run.
			if configFault(reason) {
				return fmt.Errorf("agento11y: push rejected as %s (this will not fix itself): %d: %s",
					reason, status, snippet(respBody))
			}
			return nil

		default:
			// An unexpected but non-error status. Treat as delivered rather than
			// looping forever on a shape this sink has never seen sigil produce -
			// same default loki.Sink.push takes.
			return nil
		}

		select {
		case <-time.After(backoff(attempt)):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// accountResults reads the per-generation verdicts out of a 2xx response body. This
// is the check the issue's hard constraint exists for: without it, a 200 whose
// results all say accepted:false looks identical to genuine delivery, and the
// checkpoint would advance past data that never actually landed in sigil.
//
// A malformed response body on a 2xx is treated as a retryable failure, not success:
// this function returns an error in that case (the caller does not retry it directly,
// but neither does it advance the checkpoint on a guess) rather than assuming the
// batch that produced an unparseable 200 was accepted.
func (s *Sink) accountResults(gens []wireGeneration, respBody []byte) error {
	var resp wireExportGenerationsResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		s.addRejected(sink.ReasonServerError, int64(len(gens)))
		return fmt.Errorf("agento11y: push returned 2xx with an unparseable body, "+
			"treating as undelivered rather than assuming success: %w", err)
	}

	byID := make(map[string]wireExportGenerationResult, len(resp.Results))
	for _, r := range resp.Results {
		byID[r.GenerationID] = r
	}

	var rejected int64
	for _, g := range gens {
		r, ok := byID[g.ID]
		switch {
		case !ok:
			// Sent, but no verdict came back for it at all - the same "silently
			// missing" shape as every other rejection this sink counts rather than
			// assumes away.
			rejected++
		case !r.Accepted:
			rejected++
		}
	}
	s.addRejected(sink.ReasonRejected, rejected)
	return nil
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

// backoff is exponential with a 30s cap - identical to loki.backoff; duplicated
// rather than exported from that package, which this lane does not own and must not
// edit.
func backoff(attempt int) time.Duration {
	if attempt > 10 {
		attempt = 10
	}
	d := 250 * time.Millisecond * time.Duration(uint64(1)<<uint(attempt))
	if d <= 0 || d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}

// classify4xx maps an HTTP status to a sink.Reason, status-code only - unlike Loki,
// sigil's ingest error body has no documented substring vocabulary to match against
// (Loki's own classify4xx cites a specific docs page for its "line_too_long" etc.
// tokens; nothing equivalent has been read for sigil), so guessing at body text would
// be inventing a contract that was never verified. Status code alone is enough to
// answer the one question that matters: is this a config fault or a data fault.
func classify4xx(status int) string {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return sink.ReasonUnauthorized
	// An oversized body is a DATA fault, and must not fall through to the
	// bad_request default below, because bad_request holds the checkpoint. A batch
	// too large to accept is too large on every retry, so holding the checkpoint on
	// one would wedge the pipeline permanently on a batch that can never drain -
	// which is precisely the "ran forever, delivered nothing" failure that made the
	// Loki dotted-label 400 an incident, arrived at from the opposite direction.
	// This is a real risk rather than a theoretical one: a batch is 200 generations
	// and each may carry prompts up to 32 KB and tool output up to 4 KB.
	//
	// Counted and dropped, so the checkpoint advances and reportRejections says at
	// WARN exactly how much was lost. Splitting the batch and retrying would be
	// better still, and is the obvious follow-up.
	case http.StatusRequestEntityTooLarge, http.StatusRequestURITooLong:
		return sink.ReasonLineTooLong
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

// configFault reports whether a rejection reason indicts this service's own
// configuration rather than the data it was carrying - see loki.configFault's doc
// comment for the full reasoning, reproduced here rather than imported because
// internal/sink/loki is out of scope for this lane.
func configFault(reason string) bool {
	return reason == sink.ReasonBadRequest || reason == sink.ReasonUnauthorized
}
