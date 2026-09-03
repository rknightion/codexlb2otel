package enrich

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPostgres creates a pgx-backed enricher.
func NewPostgres(ctx context.Context, dsn string, opts Options) (*StoreEnricher, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("enrich postgres: create pool: %w", err)
	}
	return NewStoreEnricher(&pgxStore{pool: pool}, opts), nil
}

type pgxStore struct {
	pool *pgxpool.Pool
}

func (s *pgxStore) Lookup(ctx context.Context, responseID string) (Row, bool, error) {
	row, err := scanRow(s.pool.QueryRow(ctx, lookupSQL, responseID))
	if err != nil {
		if err == pgx.ErrNoRows {
			return Row{}, false, nil
		}
		return Row{}, false, err
	}
	return row, true, nil
}

func (s *pgxStore) Prefetch(ctx context.Context, afterID int64, limit int) ([]Row, error) {
	rows, err := s.pool.Query(ctx, prefetchSQL, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Row
	for rows.Next() {
		row, err := scanRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *pgxStore) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRow(rs rowScanner) (Row, error) {
	var row Row
	err := rs.Scan(
		&row.ID,
		&row.RequestID,
		&row.ArchiveRequestID,
		&row.CostUSD,
		&row.APIKeyID,
		&row.APIKeyName,
		&row.Status,
		&row.ErrorCode,
		&row.FailurePhase,
		&row.LatencyResponseCreatedMS,
		&row.LatencyFirstUpstreamEventMS,
	)
	return row, err
}

func scanRows(rows pgx.Rows) (Row, error) {
	return scanRow(rows)
}

const selectFields = `
	request_logs.id,
	request_logs.request_id,
	COALESCE(request_logs.archive_request_id, '') AS archive_request_id,
	request_logs.cost_usd,
	COALESCE(request_logs.api_key_id::text, '') AS api_key_id,
	COALESCE(api_keys.name, '') AS api_key_name,
	COALESCE(request_logs.status, '') AS status,
	COALESCE(request_logs.error_code, '') AS error_code,
	COALESCE(request_logs.failure_phase, '') AS failure_phase,
	COALESCE(request_logs.latency_response_created_ms, 0) AS latency_response_created_ms,
	COALESCE(request_logs.latency_first_upstream_event_ms, 0) AS latency_first_upstream_event_ms`

const lookupSQL = `
SELECT` + selectFields + `
FROM request_logs
LEFT JOIN api_keys ON api_keys.id = request_logs.api_key_id
WHERE request_logs.request_id = $1
ORDER BY request_logs.id DESC
LIMIT 1`

const prefetchSQL = `
SELECT` + selectFields + `
FROM request_logs
LEFT JOIN api_keys ON api_keys.id = request_logs.api_key_id
WHERE request_logs.id > $1
ORDER BY request_logs.id ASC
LIMIT $2`
