package enrich

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/rknightion/codexlb2otel/internal/turn"
)

func TestStoreEnricher_PointLookupThenCacheHit(t *testing.T) {
	cost := 1.25
	store := &fakeStore{
		lookups: map[string]Row{
			"resp_1": {
				ID: 7, RequestID: "resp_1", ArchiveRequestID: "ws_1", CostUSD: &cost,
				APIKeyID: "key-1", APIKeyName: "primary", Status: "success",
				LatencyResponseCreatedMS: 12.5, LatencyFirstUpstreamEventMS: 8.25,
			},
		},
	}
	e := NewStoreEnricher(store, Options{
		LookupTimeout: time.Second,
		CacheEntries:  8,
		now:           steppedClock(time.Unix(1, 0), 25*time.Millisecond),
	})
	defer e.Close()

	first := &turn.Turn{RequestID: "ws_1", ResponseID: "resp_1", RequestKind: "compaction", AccountID: "acct-wire"}
	if got := e.Enrich(context.Background(), first); !got.Found ||
		got.Outcome != OutcomeDBHit || got.LookupDuration != 25*time.Millisecond {
		t.Fatal("first Enrich() was not found")
	}
	if first.CostUSD == nil || *first.CostUSD != cost {
		t.Fatalf("CostUSD = %v, want %.2f", first.CostUSD, cost)
	}
	if first.APIKeyID != "key-1" || first.APIKeyName != "primary" {
		t.Fatalf("api key fields = %q/%q, want key-1/primary", first.APIKeyID, first.APIKeyName)
	}
	if first.ProxyStatus != "success" || first.ProxyResponseCreatedMS != 12.5 ||
		first.ProxyFirstUpstreamEventMS != 8.25 {
		t.Fatalf("proxy fields were not attached: %+v", first)
	}
	if first.RequestKind != "compaction" || first.AccountID != "acct-wire" {
		t.Fatalf("wire-owned fields were overwritten: request_kind=%q account_id=%q",
			first.RequestKind, first.AccountID)
	}

	second := &turn.Turn{RequestID: "ws_1", ResponseID: "resp_1"}
	if got := e.Enrich(context.Background(), second); !got.Found ||
		got.Outcome != OutcomeCacheHit || got.LookupDuration != 0 {
		t.Fatalf("second Enrich() = %+v, want cache hit with no lookup duration", got)
	}
	if store.lookupCount != 1 {
		t.Fatalf("store lookups = %d, want 1", store.lookupCount)
	}

	archiveOnly := &turn.Turn{RequestID: "ws_1"}
	if got := e.Enrich(context.Background(), archiveOnly); got.Found || got.Outcome != OutcomeMiss {
		t.Fatalf("archive id seeded by point lookup: Enrich() = %+v, want miss", got)
	}
	if store.lookupCount != 1 {
		t.Fatalf("archive-only lookup queried store: lookups = %d, want 1", store.lookupCount)
	}
	stats := e.Stats()
	if stats.CacheHits != 1 || stats.CacheMisses != 2 || stats.LookupErrors != 0 {
		t.Fatalf("Stats() = %+v, want one hit, two misses, zero errors", stats)
	}
}

func TestStoreEnricher_StoreErrorIsAbsentEnrichment(t *testing.T) {
	store := &fakeStore{lookupErr: errors.New("postgres is down")}
	e := NewStoreEnricher(store, Options{
		LookupTimeout: time.Second,
		CacheEntries:  8,
		now:           steppedClock(time.Unix(2, 0), 30*time.Millisecond),
	})
	defer e.Close()

	tn := &turn.Turn{RequestID: "ws_1", ResponseID: "resp_1"}
	got := e.Enrich(context.Background(), tn)
	if got.Found || got.Outcome != OutcomeError || got.LookupDuration != 30*time.Millisecond {
		t.Fatalf("Enrich() = %+v, want error outcome with lookup duration", got)
	}
	if tn.CostUSD != nil || tn.APIKeyID != "" || tn.ProxyStatus != "" {
		t.Fatalf("turn was mutated on lookup error: %+v", tn)
	}

	stats := e.Stats()
	if stats.CacheMisses != 1 || stats.LookupErrors != 1 || stats.LastError == "" {
		t.Fatalf("Stats() = %+v, want miss plus counted error", stats)
	}
}

func TestStoreEnricher_DBMissOutcomeAndDuration(t *testing.T) {
	store := &fakeStore{lookups: map[string]Row{}}
	e := NewStoreEnricher(store, Options{
		LookupTimeout: time.Second,
		CacheEntries:  8,
		now:           steppedClock(time.Unix(3, 0), 40*time.Millisecond),
	})
	defer e.Close()

	tn := &turn.Turn{RequestID: "ws_missing", ResponseID: "resp_missing"}
	got := e.Enrich(context.Background(), tn)
	if got.Found || got.Outcome != OutcomeMiss || got.LookupDuration != 40*time.Millisecond {
		t.Fatalf("Enrich() = %+v, want db miss outcome with lookup duration", got)
	}
	if store.lookupCount != 1 {
		t.Fatalf("store lookups = %d, want 1", store.lookupCount)
	}
}

func TestStoreEnricher_PrefetchMatchesArchiveRequestIDWithoutPointQueryingIt(t *testing.T) {
	cost := 0.75
	store := &fakeStore{
		prefetch: []Row{{
			ID: 11, RequestID: "resp_2", ArchiveRequestID: "ws_2", CostUSD: &cost,
			APIKeyID: "key-2", APIKeyName: "secondary", Status: "rate_limited",
			ErrorCode: "rate_limit_exceeded", FailurePhase: "upstream",
		}},
	}
	e := NewStoreEnricher(store, Options{LookupTimeout: time.Second, CacheEntries: 8})
	defer e.Close()

	if err := e.PrefetchOnce(context.Background()); err != nil {
		t.Fatalf("PrefetchOnce() error = %v", err)
	}

	byArchiveID := &turn.Turn{RequestID: "ws_2"}
	if got := e.Enrich(context.Background(), byArchiveID); !got.Found ||
		got.Outcome != OutcomeCacheHit {
		t.Fatalf("prefetched archive_request_id Enrich() = %+v, want cache hit", got)
	}
	if byArchiveID.ProxyErrorCode != "rate_limit_exceeded" ||
		byArchiveID.ProxyFailurePhase != "upstream" {
		t.Fatalf("proxy error fields were not attached: %+v", byArchiveID)
	}
	if store.lookupCount != 0 {
		t.Fatalf("archive_request_id caused %d point lookups; want 0", store.lookupCount)
	}

	unknownArchiveID := &turn.Turn{RequestID: "ws_unknown"}
	if got := e.Enrich(context.Background(), unknownArchiveID); got.Found ||
		got.Outcome != OutcomeMiss {
		t.Fatalf("unknown archive_request_id Enrich() = %+v, want miss", got)
	}
	if store.lookupCount != 0 {
		t.Fatalf("unknown archive_request_id caused %d point lookups; want 0", store.lookupCount)
	}

	stats := e.Stats()
	if stats.Prefetches != 1 || stats.PrefetchRows != 1 || stats.CacheHits != 1 {
		t.Fatalf("Stats() = %+v, want prefetch row and cache hit", stats)
	}
}

func TestStoreEnricher_ConcurrentPrefetchAndEnrich(t *testing.T) {
	cost := 0.15
	store := &fakeStore{
		lookups: map[string]Row{
			"resp_3": {ID: 3, RequestID: "resp_3", ArchiveRequestID: "ws_3", CostUSD: &cost},
		},
		prefetch: []Row{
			{ID: 1, RequestID: "resp_1", ArchiveRequestID: "ws_1", CostUSD: &cost},
			{ID: 2, RequestID: "resp_2", ArchiveRequestID: "ws_2", CostUSD: &cost},
		},
	}
	e := NewStoreEnricher(store, Options{LookupTimeout: time.Second, CacheEntries: 2})
	defer e.Close()

	until := time.Now().Add(100 * time.Millisecond)

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(until) {
				_ = e.Enrich(context.Background(), &turn.Turn{RequestID: "ws_1"})
				_ = e.Enrich(context.Background(), &turn.Turn{RequestID: "ws_3", ResponseID: "resp_3"})
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for time.Now().Before(until) {
			if err := e.PrefetchOnce(context.Background()); err != nil {
				t.Errorf("PrefetchOnce() error = %v", err)
				return
			}
		}
	}()
	wg.Wait()

	if stats := e.Stats(); stats.LookupErrors != 0 {
		t.Fatalf("Stats() = %+v, want no lookup errors", stats)
	}
}

func TestDisabled(t *testing.T) {
	tn := &turn.Turn{RequestID: "ws_1", ResponseID: "resp_1"}
	if got := Disabled.Enrich(context.Background(), tn); got.Found ||
		got.Outcome != OutcomeDisabled {
		t.Fatalf("Disabled.Enrich() = %+v, want disabled outcome", got)
	}
	if stats := Disabled.Stats(); stats != (Stats{}) {
		t.Fatalf("Disabled.Stats() = %+v, want zero value", stats)
	}
	Disabled.Close()
}

type fakeStore struct {
	mu sync.Mutex

	lookups     map[string]Row
	prefetch    []Row
	lookupErr   error
	prefetchErr error

	lookupCount   int
	prefetchCount int
}

func (s *fakeStore) Lookup(ctx context.Context, responseID string) (Row, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.lookupCount++
	if err := ctx.Err(); err != nil {
		return Row{}, false, err
	}
	if s.lookupErr != nil {
		return Row{}, false, s.lookupErr
	}
	row, ok := s.lookups[responseID]
	return row, ok, nil
}

func (s *fakeStore) Prefetch(ctx context.Context, afterID int64, limit int) ([]Row, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.prefetchCount++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.prefetchErr != nil {
		return nil, s.prefetchErr
	}
	var rows []Row
	for _, row := range s.prefetch {
		if row.ID > afterID {
			rows = append(rows, row)
			if len(rows) == limit {
				break
			}
		}
	}
	return rows, nil
}

func (s *fakeStore) Close() {}

func steppedClock(start time.Time, step time.Duration) func() time.Time {
	var mu sync.Mutex
	now := start.Add(-step)
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(step)
		return now
	}
}
