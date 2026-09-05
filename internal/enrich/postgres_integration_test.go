package enrich

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/rknightion/codexlb2otel/internal/turn"
)

func TestPostgresIntegration_GatedByDSN(t *testing.T) {
	dsn := os.Getenv("CLB_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("CLB_TEST_PG_DSN is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	e, err := NewPostgres(ctx, dsn, Options{
		LookupTimeout:    time.Second,
		PrefetchInterval: 0,
		CacheEntries:     16,
	})
	if err != nil {
		t.Fatalf("NewPostgres() error = %v", err)
	}
	defer e.Close()

	if err := e.PrefetchOnce(context.Background()); err != nil {
		t.Fatalf("PrefetchOnce() error = %v", err)
	}
	if stats := e.Stats(); stats.LookupErrors != 0 {
		t.Fatalf("PrefetchOnce() lookup errors = %d, want 0; last error: %s", stats.LookupErrors, stats.LastError)
	}

	got := e.Enrich(context.Background(), &turn.Turn{ResponseID: "resp_codexlb2otel_missing_test"})
	if got.Found {
		t.Fatalf("unexpected enrichment for synthetic missing response id: %+v", got)
	}
	if got.Outcome != OutcomeMiss {
		t.Fatalf("Enrich() outcome = %q, want %q", got.Outcome, OutcomeMiss)
	}
}
