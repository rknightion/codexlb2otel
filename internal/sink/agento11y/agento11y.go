package agento11y

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

// Sink pushes reduced turns to Grafana agent-observability (sigil) as Generations,
// batching in memory the same way internal/sink/loki does: a batch is pushed once it
// reaches BatchSize or has sat open past BatchWait, whichever comes first, and Flush
// is the backstop cmd/codexlb2otel calls before persisting a checkpoint (see
// sink.Sink.Emit's own doc comment - a sink MAY buffer, but it is only the caller
// pairing every Emit with an immediate Flush that makes Emit's nil actually mean
// "delivered").
type Sink struct {
	cfg    config.AgentO11y
	guard  *attr.Guard
	token  string
	client *http.Client

	mu     sync.Mutex
	buf    []wireGeneration
	opened time.Time

	rejMu    sync.Mutex
	rejected map[string]int64
}

var _ sink.Sink = (*Sink)(nil)
var _ sink.Reporter = (*Sink)(nil)

// New builds an agento11y sink from its config section.
//
// guard is shared with every other sink in the process - see loki.New's identical
// parameter and attr.Guard's own doc comment for why: the cardinality cap is per
// field across the whole process, not per sink.
func New(cfg config.AgentO11y, guard *attr.Guard) (*Sink, error) {
	if guard == nil {
		return nil, fmt.Errorf("agento11y: guard is nil")
	}
	if cfg.URL == "" {
		return nil, fmt.Errorf("agento11y: url is empty")
	}
	if cfg.BatchSize <= 0 {
		return nil, fmt.Errorf("agento11y: batch_size must be positive, got %d", cfg.BatchSize)
	}
	token, err := cfg.Token.Resolve()
	if err != nil {
		return nil, fmt.Errorf("agento11y: resolving token: %w", err)
	}

	return &Sink{
		cfg:      cfg,
		guard:    guard,
		token:    token,
		client:   &http.Client{},
		rejected: map[string]int64{},
	}, nil
}

// Name identifies this sink in logs and self-observability metrics.
func (s *Sink) Name() string { return "agento11y" }

// Emit builds one Generation per turn and buffers it, pushing once the buffer reaches
// BatchSize or has been open at least BatchWait. See loki.Sink.Emit's doc comment -
// same shape, same reasoning: PollInterval is what actually drives how often this
// runs in production, not a background timer.
func (s *Sink) Emit(ctx context.Context, turns []*turn.Turn) error {
	s.mu.Lock()
	for _, t := range turns {
		if t == nil {
			continue
		}
		g := buildGeneration(t, s.guard)
		if len(s.buf) == 0 {
			s.opened = time.Now()
		}
		s.buf = append(s.buf, g)
	}

	var toPush []wireGeneration
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
// dropped. See loki.Sink.Flush's doc comment for why an error here MUST reach the
// caller unlike a permanent-4xx inside push.
func (s *Sink) Flush(ctx context.Context) error {
	s.mu.Lock()
	toPush := s.buf
	s.buf = nil
	s.mu.Unlock()
	return s.push(ctx, toPush)
}

// Close releases the HTTP client's idle connections. Deliberately does not flush -
// see sink.Sink's doc comment on Close.
func (s *Sink) Close(context.Context) error {
	s.client.CloseIdleConnections()
	return nil
}

// Rejections reports generations refused, by reason, since process start - both
// classes push() can produce: an HTTP-level config fault held the whole batch
// (reported once the caller decides to give up on it) and per-generation
// accepted:false verdicts from a 2xx response (sink.ReasonRejected).
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

// Pending reports how many generations are buffered and not yet pushed.
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
