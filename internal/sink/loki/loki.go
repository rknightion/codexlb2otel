// Package loki pushes reduced turns to Grafana Loki natively.
//
// There is deliberately no OTLP logs path anywhere in this service. Binding
// decision from issue #1: "we'll stick to loki as we need the ability to customise
// the labels and structured metadata which disappears in otel as it has an entirely
// different model for logs via attributes". This package is the entire logs story.
//
// It owns none of the attribute contract - internal/attr decides what becomes a
// Loki stream label and what becomes structured metadata, and this package calls
// into it rather than assembling either set itself. What this package owns is
// specific to Loki as a wire protocol and a store: splitting one Turn into several
// lines so an oversized one cannot take the rest down with it (issue #6), the
// per-line byte budget and its explicit truncation, batching, and the push/retry/
// classify loop against the HTTP push API.
package loki

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/rknightion/codexlb2otel/internal/attr"
	"github.com/rknightion/codexlb2otel/internal/config"
	"github.com/rknightion/codexlb2otel/internal/sink"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

// outLine is one Loki log line, built but not yet grouped into a push request.
type outLine struct {
	recordType string
	ts         time.Time
	labels     []attr.KV
	metadata   []attr.KV
	body       []byte
}

// rejecter is the one method buildLines needs from *Sink. Named separately so
// record.go's line-building code depends on a one-method interface instead of the
// whole Sink type.
type rejecter interface {
	addRejected(reason string, n int64)
}

// Sink pushes reduced turns to Loki. It batches lines in memory and pushes once a
// batch reaches its configured size or has sat open past its configured wait,
// whichever happens first; Flush is the backstop the tail loop calls before
// persisting a checkpoint, so nothing buffered is ever silently lost to a process
// that never called Emit again.
type Sink struct {
	cfg         config.Loki
	guard       *attr.Guard
	serviceName string
	token       string
	client      *http.Client
	enabled     map[string]bool // nil means every record type in attr.RecordTypes

	mu     sync.Mutex
	buf    []outLine
	opened time.Time

	rejMu    sync.Mutex
	rejected map[string]int64
}

var _ sink.Sink = (*Sink)(nil)
var _ sink.Reporter = (*Sink)(nil)

// New builds a Loki sink from its config section.
//
// guard is shared with every other sink in the process, not created here - see
// attr.Guard's own doc comment for why that sharing is the point: the cardinality
// cap is per field across the whole process, not per sink.
func New(cfg config.Loki, guard *attr.Guard, serviceName string) (*Sink, error) {
	if guard == nil {
		return nil, fmt.Errorf("loki: guard is nil")
	}
	if cfg.URL == "" {
		return nil, fmt.Errorf("loki: url is empty")
	}
	if cfg.MaxLineBytes <= 0 {
		return nil, fmt.Errorf("loki: max_line_bytes must be positive, got %d", cfg.MaxLineBytes)
	}
	token, err := cfg.Token.Resolve()
	if err != nil {
		return nil, fmt.Errorf("loki: resolving token: %w", err)
	}
	if err := attr.ValidateLabels(cfg.Labels); err != nil {
		return nil, fmt.Errorf("loki: %w", err)
	}

	var enabled map[string]bool
	if len(cfg.RecordTypes) > 0 {
		enabled = make(map[string]bool, len(cfg.RecordTypes))
		for _, rt := range cfg.RecordTypes {
			enabled[rt] = true
		}
	}

	return &Sink{
		cfg:         cfg,
		guard:       guard,
		serviceName: serviceName,
		token:       token,
		enabled:     enabled,
		client:      &http.Client{},
		rejected:    map[string]int64{},
	}, nil
}

// Name identifies this sink in logs and self-observability metrics.
func (s *Sink) Name() string { return "loki" }

// Emit buffers the batch's lines and pushes once the buffer reaches BatchSize lines
// or has been open at least BatchWait.
//
// There is no background timer goroutine driving BatchWait. The archive tail loop
// already calls Emit on every poll cycle whether or not it found new turns to
// reduce would be an odd thing to rely on, so instead: PollInterval is what actually
// drives how often Emit is invoked in production, and a buffer that is under
// BatchSize but past BatchWait gets flushed the next time Emit runs regardless of
// whether that call added anything new. That is enough to bound staleness without a
// second goroutine whose lifecycle Close would then have to manage.
func (s *Sink) Emit(ctx context.Context, turns []*turn.Turn) error {
	s.mu.Lock()
	for _, t := range turns {
		if t == nil {
			continue
		}
		lines := s.dropTooOld(buildLines(t, s.guard, s.serviceName, s.cfg.Labels, s.cfg.MaxLineBytes, s.enabled, s))
		if len(s.buf) == 0 && len(lines) > 0 {
			s.opened = time.Now()
		}
		s.buf = append(s.buf, lines...)
	}

	var toPush []outLine
	if len(s.buf) > 0 && (len(s.buf) >= s.cfg.BatchSize || time.Since(s.opened) >= s.cfg.BatchWait) {
		toPush = s.buf
		s.buf = nil
	}
	s.mu.Unlock()

	if len(toPush) == 0 {
		return nil
	}
	return s.push(ctx, toPush)
}

// Flush pushes whatever is buffered, blocking until it is delivered or permanently
// dropped.
//
// Unlike a permanent 4xx inside push (deliberately swallowed so the checkpoint is
// free to advance past a line that will never be accepted), an error returned here
// must reach the caller: this is what the tail loop calls immediately before
// persisting a checkpoint, and it is the one signal standing between a delivery
// that actually failed and a checkpoint that claims it did not.
func (s *Sink) Flush(ctx context.Context) error {
	s.mu.Lock()
	toPush := s.buf
	s.buf = nil
	s.mu.Unlock()
	return s.push(ctx, toPush)
}

// Close releases the HTTP client's idle connections. It deliberately does not
// flush - see the Sink interface's doc comment on why a Close that flushed would
// hide a failed flush behind a successful-looking exit.
func (s *Sink) Close(context.Context) error {
	s.client.CloseIdleConnections()
	return nil
}

// Rejections reports deliveries refused, by reason, since process start.
//
// It folds together two sources that are the same outcome from an operator's point
// of view even though they are detected differently: reasons Loki itself reported
// (parsed from a 4xx response body) and lines this sink declined to ship at all
// because they could not be made to fit MaxLineBytes even truncated. Both mean a
// line that never reached Loki.
func (s *Sink) Rejections() []sink.Rejection {
	s.rejMu.Lock()
	defer s.rejMu.Unlock()
	out := make([]sink.Rejection, 0, len(s.rejected))
	for reason, n := range s.rejected {
		out = append(out, sink.Rejection{Reason: reason, Count: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Reason < out[j].Reason })
	return out
}

// Pending reports how many lines are buffered and not yet pushed.
func (s *Sink) Pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.buf)
}

func (s *Sink) addRejected(reason string, n int64) {
	if n <= 0 {
		return
	}
	s.rejMu.Lock()
	s.rejected[reason] += n
	s.rejMu.Unlock()
}

// dropTooOld removes lines Loki will accept and then discard.
//
// Grafana Cloud enforces a maximum sample age, and it does NOT reject an older line
// the way its own docs describe. Measured 2026-08-07 against logs-prod-035 by pushing
// one line per age and querying each back: 3h old returns 204 and is queryable; 4h,
// 5h, 8h, 15h and 26h all return 204 and were queryable neither 6 seconds nor 45
// minutes later. There is no error, no rejection body and no counter.
//
// CAREFUL WITH THE CONCLUSION: the documented tenant limit is 7 DAYS, and old data is
// described as ingestable but not immediately queryable. So this is not proof the
// lines are discarded - only that they do not become visible on any timescale a
// delivery check can wait for. Either way the operational consequence is the same and
// is all this code acts on: a push that returns 204 is not evidence of delivery.
//
// That is the worst failure this sink can have, and it is invisible by construction:
// the first live run replayed a 15-hour-old archive, logged nothing, wrote a clean
// checkpoint, and delivered not one line. Dropping locally converts silent loss into
// counted loss - the same reasoning as every other rejection reason here.
//
// This does not affect the service's actual job. Tailing live traffic emits records
// seconds old, and the default window is sized for the only realistic gap - Grafana
// Cloud being unreachable for an hour or two. Backfilling an old corpus is explicitly
// NOT a goal, and failing it loudly beats appearing to succeed.
func (s *Sink) dropTooOld(lines []outLine) []outLine {
	if s.cfg.MaxLineAge <= 0 {
		return lines
	}
	cutoff := time.Now().Add(-s.cfg.MaxLineAge)
	kept := lines[:0]
	var dropped int64
	for _, l := range lines {
		if l.ts.Before(cutoff) {
			dropped++
			continue
		}
		kept = append(kept, l)
	}
	if dropped > 0 {
		s.addRejected(sink.ReasonTooOld, dropped)
	}
	return kept
}
