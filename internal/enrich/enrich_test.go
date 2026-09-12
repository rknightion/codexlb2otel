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
				LatencyQueueMS: intPtr(125), LatencyResponseCreateGateWaitMS: intPtr(0),
				LatencyBridgeQueueWaitMS: intPtr(2), UpstreamStatusCode: 503,
				UpstreamErrorCode: "upstream_overloaded", UpstreamTransport: "http",
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
	if first.ProxyQueueWaitMS == nil || *first.ProxyQueueWaitMS != 125 ||
		first.ProxyResponseCreateGateWaitMS == nil || *first.ProxyResponseCreateGateWaitMS != 0 ||
		first.ProxyBridgeQueueWaitMS == nil || *first.ProxyBridgeQueueWaitMS != 2 {
		t.Fatalf("proxy wait fields were not attached: %+v", first)
	}
	if first.UpstreamStatusCode != 503 || first.UpstreamErrorCode != "upstream_overloaded" ||
		first.UpstreamTransport != "http" {
		t.Fatalf("upstream fields were not attached: %+v", first)
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

func TestStoreEnricher_OptionalProxyAndUpstreamFields(t *testing.T) {
	zero := 0
	queue := 125
	bridge := 2
	status := 503

	tests := []struct {
		name          string
		row           Row
		wantQueue     *int
		wantGate      *int
		wantBridge    *int
		wantStatus    int
		wantErrorCode string
		wantTransport string
	}{
		{
			name: "present",
			row: Row{
				RequestID:                       "resp_present",
				LatencyQueueMS:                  &queue,
				LatencyResponseCreateGateWaitMS: &zero,
				LatencyBridgeQueueWaitMS:        &bridge,
				UpstreamStatusCode:              status,
				UpstreamErrorCode:               "upstream_overloaded",
				UpstreamTransport:               "http",
			},
			wantQueue:     &queue,
			wantGate:      &zero,
			wantBridge:    &bridge,
			wantStatus:    status,
			wantErrorCode: "upstream_overloaded",
			wantTransport: "http",
		},
		{
			name: "zero",
			row: Row{
				RequestID:                       "resp_zero",
				LatencyQueueMS:                  &zero,
				LatencyResponseCreateGateWaitMS: &zero,
				LatencyBridgeQueueWaitMS:        &zero,
			},
			wantQueue:  &zero,
			wantGate:   &zero,
			wantBridge: &zero,
		},
		{
			name: "null",
			row: Row{
				RequestID: "resp_null",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{lookups: map[string]Row{tt.row.RequestID: tt.row}}
			e := NewStoreEnricher(store, Options{LookupTimeout: time.Second, CacheEntries: 8})
			defer e.Close()

			gotTurn := &turn.Turn{ResponseID: tt.row.RequestID}
			if got := e.Enrich(context.Background(), gotTurn); !got.Found {
				t.Fatalf("Enrich() = %+v, want found", got)
			}
			assertOptionalInt(t, gotTurn.ProxyQueueWaitMS, tt.wantQueue)
			assertOptionalInt(t, gotTurn.ProxyResponseCreateGateWaitMS, tt.wantGate)
			assertOptionalInt(t, gotTurn.ProxyBridgeQueueWaitMS, tt.wantBridge)
			if gotTurn.UpstreamStatusCode != tt.wantStatus {
				t.Fatalf("UpstreamStatusCode = %d, want %d", gotTurn.UpstreamStatusCode, tt.wantStatus)
			}
			if gotTurn.UpstreamErrorCode != tt.wantErrorCode {
				t.Fatalf("UpstreamErrorCode = %q, want %q", gotTurn.UpstreamErrorCode, tt.wantErrorCode)
			}
			if gotTurn.UpstreamTransport != tt.wantTransport {
				t.Fatalf("UpstreamTransport = %q, want %q", gotTurn.UpstreamTransport, tt.wantTransport)
			}
		})
	}
}

func TestStoreEnricher_AttachesTurnEnrichmentAndAbsentValues(t *testing.T) {
	tests := []struct {
		name           string
		row            Row
		wantClient     string
		wantConnection string
		wantFailure    string
	}{
		{
			name: "present",
			row: Row{
				RequestID:      "resp_enrichment_present",
				ClientGroup:    "client-test",
				ConnectionKind: "normal",
				FailurePhase:   "downstream",
			},
			wantClient:     "client-test",
			wantConnection: "normal",
			wantFailure:    "downstream",
		},
		{
			name: "null and empty are absent",
			row: Row{
				RequestID:      "resp_enrichment_absent",
				ClientGroup:    "",
				ConnectionKind: "",
				FailurePhase:   "",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{lookups: map[string]Row{tt.row.RequestID: tt.row}}
			e := NewStoreEnricher(store, Options{LookupTimeout: time.Second, CacheEntries: 8})
			defer e.Close()

			gotTurn := &turn.Turn{ResponseID: tt.row.RequestID}
			if got := e.Enrich(context.Background(), gotTurn); !got.Found {
				t.Fatalf("Enrich() = %+v, want found", got)
			}
			if gotTurn.ClientGroup != tt.wantClient {
				t.Errorf("ClientGroup = %q, want %q", gotTurn.ClientGroup, tt.wantClient)
			}
			if gotTurn.ConnectionKind != tt.wantConnection {
				t.Errorf("ConnectionKind = %q, want %q", gotTurn.ConnectionKind, tt.wantConnection)
			}
			if gotTurn.FailurePhase != tt.wantFailure {
				t.Errorf("FailurePhase = %q, want %q", gotTurn.FailurePhase, tt.wantFailure)
			}
			if gotTurn.ProxyFailurePhase != tt.wantFailure {
				t.Errorf("ProxyFailurePhase = %q, want %q", gotTurn.ProxyFailurePhase, tt.wantFailure)
			}
		})
	}
}

func TestStoreEnricher_AttachesWidenedRequestLogFields(t *testing.T) {
	row := Row{
		RequestID:            "resp_widened",
		PlanType:             "prolite",
		ConversationID:       "conversation-1",
		SessionID:            "proxy-session-1",
		ServiceTier:          "priority",
		ActualServiceTier:    "default",
		RequestedServiceTier: "priority",
		LatencyMS:            1234.5,
		LatencyFirstTokenMS:  456.75,
		Transport:            "websocket",
		StickyKind:           "prompt_cache",
		StickyKeySource:      "thread_header",
		ProxyRouteMode:       "direct",
	}
	store := &fakeStore{lookups: map[string]Row{row.RequestID: row}}
	e := NewStoreEnricher(store, Options{LookupTimeout: time.Second, CacheEntries: 8})
	defer e.Close()

	got := &turn.Turn{ResponseID: row.RequestID, PlanType: "wire-plan"}
	if result := e.Enrich(context.Background(), got); !result.Found {
		t.Fatalf("Enrich() = %+v, want found", result)
	}
	if got.PlanType != row.PlanType || got.ConversationID != row.ConversationID || got.ProxySessionID != row.SessionID {
		t.Fatalf("plan/identity fields = %q/%q/%q, want %q/%q/%q",
			got.PlanType, got.ConversationID, got.ProxySessionID,
			row.PlanType, row.ConversationID, row.SessionID)
	}
	if got.ProxyServiceTier != row.ServiceTier || got.ProxyActualServiceTier != row.ActualServiceTier ||
		got.ProxyRequestedServiceTier != row.RequestedServiceTier {
		t.Fatalf("tier fields = %q/%q/%q, want %q/%q/%q",
			got.ProxyServiceTier, got.ProxyActualServiceTier, got.ProxyRequestedServiceTier,
			row.ServiceTier, row.ActualServiceTier, row.RequestedServiceTier)
	}
	if got.ProxyLatencyMS != row.LatencyMS || got.ProxyFirstTokenMS != row.LatencyFirstTokenMS {
		t.Fatalf("proxy latencies = %v/%v, want %v/%v",
			got.ProxyLatencyMS, got.ProxyFirstTokenMS, row.LatencyMS, row.LatencyFirstTokenMS)
	}
	if got.ServiceTierOutcome != "downgraded" {
		t.Fatalf("ServiceTierOutcome = %q, want downgraded", got.ServiceTierOutcome)
	}
	if got.StickyKind != row.StickyKind || got.StickyKeySource != row.StickyKeySource ||
		got.ProxyRouteMode != row.ProxyRouteMode {
		t.Fatalf("routing fields = %q/%q/%q, want %q/%q/%q",
			got.StickyKind, got.StickyKeySource, got.ProxyRouteMode,
			row.StickyKind, row.StickyKeySource, row.ProxyRouteMode)
	}
	if result := e.Enrich(context.Background(), &turn.Turn{ResponseID: row.RequestID}); !result.Found {
		t.Fatalf("cached Enrich() = %+v, want found", result)
	}
}

func TestServiceTierOutcome(t *testing.T) {
	for _, tc := range []struct {
		name, requested, actual, want string
	}{
		{name: "not requested", want: "not_requested"},
		{name: "granted", requested: "priority", actual: "priority", want: "granted"},
		{name: "downgraded", requested: "priority", actual: "default", want: "downgraded"},
		{name: "unknown actual", requested: "priority", want: "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := serviceTierOutcome(tc.requested, tc.actual); got != tc.want {
				t.Fatalf("serviceTierOutcome(%q, %q) = %q, want %q", tc.requested, tc.actual, got, tc.want)
			}
		})
	}
}

func TestStoreEnricher_ConnectionRowsKeepNullableWaitsAbsent(t *testing.T) {
	zero := 0
	for _, connectionKind := range []string{"normal", "prewarm"} {
		t.Run(connectionKind, func(t *testing.T) {
			row := Row{
				ID:                              11,
				RequestID:                       "resp_" + connectionKind,
				ConnectionKind:                  connectionKind,
				LatencyResponseCreateGateWaitMS: &zero,
				LatencyQueueMS:                  nil,
				LatencyBridgeQueueWaitMS:        nil,
			}
			store := &fakeStore{lookups: map[string]Row{row.RequestID: row}}
			e := NewStoreEnricher(store, Options{LookupTimeout: time.Second, CacheEntries: 8})
			defer e.Close()

			gotTurn := &turn.Turn{ResponseID: row.RequestID}
			if got := e.Enrich(context.Background(), gotTurn); !got.Found {
				t.Fatalf("Enrich() = %+v, want found", got)
			}
			if gotTurn.ConnectionKind != connectionKind {
				t.Fatalf("ConnectionKind = %q, want %q", gotTurn.ConnectionKind, connectionKind)
			}
			if gotTurn.ProxyQueueWaitMS != nil || gotTurn.ProxyBridgeQueueWaitMS != nil {
				t.Fatalf("connection waits = queue %v, bridge %v; want absent", gotTurn.ProxyQueueWaitMS, gotTurn.ProxyBridgeQueueWaitMS)
			}
			if gotTurn.ProxyResponseCreateGateWaitMS == nil || *gotTurn.ProxyResponseCreateGateWaitMS != 0 {
				t.Fatalf("gate wait = %v, want present zero", gotTurn.ProxyResponseCreateGateWaitMS)
			}
		})
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
			ErrorCode: "rate_limit_exceeded", ClientGroup: "client-prefetch",
			ConnectionKind: "prewarm", FailurePhase: "upstream",
			LatencyQueueMS: intPtr(18), LatencyResponseCreateGateWaitMS: intPtr(0),
			LatencyBridgeQueueWaitMS: intPtr(1), UpstreamStatusCode: 429,
			UpstreamErrorCode: "upstream_rate_limited", UpstreamTransport: "http",
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
	if byArchiveID.ClientGroup != "client-prefetch" || byArchiveID.ConnectionKind != "prewarm" ||
		byArchiveID.FailurePhase != "upstream" {
		t.Fatalf("turn enrichment fields were not attached: %+v", byArchiveID)
	}
	if byArchiveID.ProxyQueueWaitMS == nil || *byArchiveID.ProxyQueueWaitMS != 18 ||
		byArchiveID.ProxyResponseCreateGateWaitMS == nil || *byArchiveID.ProxyResponseCreateGateWaitMS != 0 ||
		byArchiveID.ProxyBridgeQueueWaitMS == nil || *byArchiveID.ProxyBridgeQueueWaitMS != 1 {
		t.Fatalf("archive proxy wait fields were not attached: %+v", byArchiveID)
	}
	if byArchiveID.UpstreamStatusCode != 429 || byArchiveID.UpstreamErrorCode != "upstream_rate_limited" ||
		byArchiveID.UpstreamTransport != "http" {
		t.Fatalf("archive upstream fields were not attached: %+v", byArchiveID)
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
