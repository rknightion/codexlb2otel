// Package enrich joins archive turns to codex-lb's request_logs as optional
// enrichment.
//
// The archive remains the primary source of truth. A lookup failure is a cache miss,
// not an error returned to the caller, because Postgres being unavailable must reduce
// the emitted telemetry rather than stop the archive pipeline.
package enrich

import (
	"container/list"
	"context"
	"sync"
	"time"

	"github.com/rknightion/codexlb2otel/internal/turn"
)

// Enricher is the frozen archive-to-request_logs seam.
type Enricher interface {
	Enrich(ctx context.Context, t *turn.Turn) Result
	Stats() Stats
	Close()
}

// Disabled is the no-op enricher for configs that leave postgres.enabled false.
var Disabled Enricher = disabled{}

// Options controls lookup bounds, background prefetch and cache size.
type Options struct {
	LookupTimeout    time.Duration
	PrefetchInterval time.Duration
	CacheEntries     int
	PrefetchLimit    int
	now              func() time.Time
}

// Row is the request_logs subset this package is allowed to attach to an archive
// turn. It deliberately contains no request_kind or account id because those are
// wire-owned in codexlb2otel and must not be re-derived from Postgres.
type Row struct {
	ID                          int64
	RequestID                   string
	ArchiveRequestID            string
	CostUSD                     *float64
	APIKeyID                    string
	APIKeyName                  string
	Status                      string
	ErrorCode                   string
	FailurePhase                string
	LatencyResponseCreatedMS    float64
	LatencyFirstUpstreamEventMS float64
}

// Outcome is a bounded label value for codexlb.selfobs.enrich_lookups.
type Outcome string

const (
	OutcomeCacheHit Outcome = "cache_hit"
	OutcomeDBHit    Outcome = "db_hit"
	OutcomeMiss     Outcome = "miss"
	OutcomeError    Outcome = "error"
	OutcomeDisabled Outcome = "disabled"
)

// Result reports whether enrichment was attached plus the one bounded per-call
// outcome root wiring needs for codexlb.selfobs.enrich_lookups. Errors are counted
// in Stats and intentionally not returned here.
type Result struct {
	Found          bool
	Row            Row
	Outcome        Outcome
	LookupDuration time.Duration
}

// Stats is safe to expose directly as self-observability state.
type Stats struct {
	CacheHits    int64
	CacheMisses  int64
	LookupErrors int64
	Prefetches   int64
	PrefetchRows int64
	CacheEntries int
	LastError    string
}

// Store is the small database seam used by tests and by the pgx implementation.
type Store interface {
	Lookup(ctx context.Context, responseID string) (Row, bool, error)
	Prefetch(ctx context.Context, afterID int64, limit int) ([]Row, error)
	Close()
}

// StoreEnricher enriches turns using a Store plus an in-memory LRU.
type StoreEnricher struct {
	store Store
	opts  Options
	now   func() time.Time

	mu           sync.Mutex
	byResponse   map[string]*cacheEntry
	byArchive    map[string]*cacheEntry
	lru          *list.List
	lastID       int64
	cacheHits    int64
	cacheMisses  int64
	errors       int64
	prefetches   int64
	prefetchRows int64
	lastError    string

	done chan struct{}
	once sync.Once
	wg   sync.WaitGroup
}

type cacheEntry struct {
	row Row
	el  *list.Element
}

// NewStoreEnricher creates an Enricher around store. A zero PrefetchInterval leaves
// background prefetch disabled; callers and tests can still call PrefetchOnce.
func NewStoreEnricher(store Store, opts Options) *StoreEnricher {
	if opts.LookupTimeout <= 0 {
		opts.LookupTimeout = time.Second
	}
	if opts.CacheEntries <= 0 {
		opts.CacheEntries = 1024
	}
	if opts.PrefetchLimit <= 0 {
		opts.PrefetchLimit = 500
	}
	now := opts.now
	if now == nil {
		now = time.Now
	}

	e := &StoreEnricher{
		store:      store,
		opts:       opts,
		now:        now,
		byResponse: make(map[string]*cacheEntry),
		byArchive:  make(map[string]*cacheEntry),
		lru:        list.New(),
		done:       make(chan struct{}),
	}
	if opts.PrefetchInterval > 0 {
		e.wg.Add(1)
		go e.prefetchLoop()
	}
	return e
}

// Enrich attaches request_logs enrichment to t when a row is available.
func (e *StoreEnricher) Enrich(ctx context.Context, t *turn.Turn) Result {
	if e == nil || e.store == nil || t == nil {
		return Result{Outcome: OutcomeDisabled}
	}

	if row, ok := e.cacheLookup(t); ok {
		e.recordHit()
		attach(t, row)
		return Result{Found: true, Row: row, Outcome: OutcomeCacheHit}
	}
	e.recordMiss()

	if t.ResponseID == "" {
		return Result{Outcome: OutcomeMiss}
	}

	lookupCtx, cancel := context.WithTimeout(ctx, e.opts.LookupTimeout)
	defer cancel()
	start := e.now()
	row, ok, err := e.store.Lookup(lookupCtx, t.ResponseID)
	duration := e.now().Sub(start)
	if err != nil {
		e.recordError(err)
		return Result{Outcome: OutcomeError, LookupDuration: duration}
	}
	if !ok {
		return Result{Outcome: OutcomeMiss, LookupDuration: duration}
	}

	e.cacheStore(row)
	attach(t, row)
	return Result{Found: true, Row: row, Outcome: OutcomeDBHit, LookupDuration: duration}
}

// PrefetchOnce tails request_logs by monotonically increasing id and merges any rows
// into the cache, keyed by request_id and archive_request_id.
func (e *StoreEnricher) PrefetchOnce(ctx context.Context) error {
	if e == nil || e.store == nil {
		return nil
	}

	e.mu.Lock()
	afterID := e.lastID
	e.mu.Unlock()

	lookupCtx, cancel := context.WithTimeout(ctx, e.opts.LookupTimeout)
	defer cancel()
	rows, err := e.store.Prefetch(lookupCtx, afterID, e.opts.PrefetchLimit)
	if err != nil {
		e.recordError(err)
		return nil
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	e.prefetches++
	for _, row := range rows {
		if row.ID > e.lastID {
			e.lastID = row.ID
		}
		e.cacheStoreLocked(row)
		e.prefetchRows++
	}
	return nil
}

// Stats returns a consistent snapshot of cache and lookup state.
func (e *StoreEnricher) Stats() Stats {
	if e == nil {
		return Stats{}
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	return Stats{
		CacheHits:    e.cacheHits,
		CacheMisses:  e.cacheMisses,
		LookupErrors: e.errors,
		Prefetches:   e.prefetches,
		PrefetchRows: e.prefetchRows,
		CacheEntries: len(e.byResponse),
		LastError:    e.lastError,
	}
}

// Close stops background prefetch and closes the Store.
func (e *StoreEnricher) Close() {
	if e == nil {
		return
	}
	e.once.Do(func() {
		close(e.done)
		e.wg.Wait()
		if e.store != nil {
			e.store.Close()
		}
	})
}

func (e *StoreEnricher) prefetchLoop() {
	defer e.wg.Done()
	ticker := time.NewTicker(e.opts.PrefetchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			_ = e.PrefetchOnce(context.Background())
		case <-e.done:
			return
		}
	}
}

func (e *StoreEnricher) cacheLookup(t *turn.Turn) (Row, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if t.ResponseID != "" {
		if ent, ok := e.byResponse[t.ResponseID]; ok {
			e.lru.MoveToFront(ent.el)
			return ent.row, true
		}
	}
	if t.RequestID != "" {
		if ent, ok := e.byArchive[t.RequestID]; ok {
			e.lru.MoveToFront(ent.el)
			return ent.row, true
		}
	}
	return Row{}, false
}

func (e *StoreEnricher) cacheStore(row Row) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cacheStoreLocked(row)
}

func (e *StoreEnricher) cacheStoreLocked(row Row) {
	if row.RequestID == "" {
		return
	}

	if ent, ok := e.byResponse[row.RequestID]; ok {
		if ent.row.ArchiveRequestID != "" && ent.row.ArchiveRequestID != row.ArchiveRequestID {
			delete(e.byArchive, ent.row.ArchiveRequestID)
		}
		ent.row = row
		e.lru.MoveToFront(ent.el)
		if row.ArchiveRequestID != "" {
			e.byArchive[row.ArchiveRequestID] = ent
		}
		return
	}

	ent := &cacheEntry{row: row}
	ent.el = e.lru.PushFront(row.RequestID)
	e.byResponse[row.RequestID] = ent
	if row.ArchiveRequestID != "" {
		e.byArchive[row.ArchiveRequestID] = ent
	}

	for len(e.byResponse) > e.opts.CacheEntries {
		back := e.lru.Back()
		if back == nil {
			return
		}
		responseID, ok := back.Value.(string)
		if !ok {
			e.lru.Remove(back)
			continue
		}
		old := e.byResponse[responseID]
		e.lru.Remove(back)
		delete(e.byResponse, responseID)
		if old != nil && old.row.ArchiveRequestID != "" {
			delete(e.byArchive, old.row.ArchiveRequestID)
		}
	}
}

func (e *StoreEnricher) recordHit() {
	e.mu.Lock()
	e.cacheHits++
	e.mu.Unlock()
}

func (e *StoreEnricher) recordMiss() {
	e.mu.Lock()
	e.cacheMisses++
	e.mu.Unlock()
}

func (e *StoreEnricher) recordError(err error) {
	e.mu.Lock()
	e.errors++
	e.lastError = err.Error()
	e.mu.Unlock()
}

func attach(t *turn.Turn, row Row) {
	t.CostUSD = row.CostUSD
	t.APIKeyID = row.APIKeyID
	t.APIKeyName = row.APIKeyName
	t.ProxyStatus = row.Status
	t.ProxyErrorCode = row.ErrorCode
	t.ProxyFailurePhase = row.FailurePhase
	t.ProxyResponseCreatedMS = row.LatencyResponseCreatedMS
	t.ProxyFirstUpstreamEventMS = row.LatencyFirstUpstreamEventMS
}

type disabled struct{}

func (disabled) Enrich(context.Context, *turn.Turn) Result {
	return Result{Outcome: OutcomeDisabled}
}
func (disabled) Stats() Stats { return Stats{} }
func (disabled) Close()       {}
