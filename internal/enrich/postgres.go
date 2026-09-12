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
	var (
		clientGroup                     *string
		connectionKind                  *string
		failurePhase                    *string
		planType                        *string
		conversationID                  *string
		sessionID                       *string
		serviceTier                     *string
		actualServiceTier               *string
		requestedServiceTier            *string
		latencyQueueMS                  *int
		latencyResponseCreateGateWaitMS *int
		latencyBridgeQueueWaitMS        *int
		upstreamStatusCode              *int
		upstreamErrorCode               *string
		upstreamTransport               *string
		transport                       *string
		stickyKind                      *string
		stickyKeySource                 *string
		proxyRouteMode                  *string
	)
	err := rs.Scan(
		&row.ID,
		&row.RequestID,
		&row.ArchiveRequestID,
		&row.CostUSD,
		&row.APIKeyID,
		&row.APIKeyName,
		&row.Status,
		&row.ErrorCode,
		&planType,
		&conversationID,
		&sessionID,
		&serviceTier,
		&actualServiceTier,
		&requestedServiceTier,
		&clientGroup,
		&connectionKind,
		&failurePhase,
		&row.LatencyMS,
		&row.LatencyFirstTokenMS,
		&row.LatencyResponseCreatedMS,
		&row.LatencyFirstUpstreamEventMS,
		&latencyQueueMS,
		&latencyResponseCreateGateWaitMS,
		&latencyBridgeQueueWaitMS,
		&upstreamStatusCode,
		&upstreamErrorCode,
		&upstreamTransport,
		&transport,
		&stickyKind,
		&stickyKeySource,
		&proxyRouteMode,
	)
	if err != nil {
		return row, err
	}
	if clientGroup != nil {
		row.ClientGroup = *clientGroup
	}
	if connectionKind != nil {
		row.ConnectionKind = *connectionKind
	}
	if failurePhase != nil {
		row.FailurePhase = *failurePhase
	}
	if planType != nil {
		row.PlanType = *planType
	}
	if conversationID != nil {
		row.ConversationID = *conversationID
	}
	if sessionID != nil {
		row.SessionID = *sessionID
	}
	if serviceTier != nil {
		row.ServiceTier = *serviceTier
	}
	if actualServiceTier != nil {
		row.ActualServiceTier = *actualServiceTier
	}
	if requestedServiceTier != nil {
		row.RequestedServiceTier = *requestedServiceTier
	}
	row.LatencyQueueMS = latencyQueueMS
	row.LatencyResponseCreateGateWaitMS = latencyResponseCreateGateWaitMS
	row.LatencyBridgeQueueWaitMS = latencyBridgeQueueWaitMS
	if upstreamStatusCode != nil {
		row.UpstreamStatusCode = *upstreamStatusCode
	}
	if upstreamErrorCode != nil {
		row.UpstreamErrorCode = *upstreamErrorCode
	}
	if upstreamTransport != nil {
		row.UpstreamTransport = *upstreamTransport
	}
	if transport != nil {
		row.Transport = *transport
	}
	if stickyKind != nil {
		row.StickyKind = *stickyKind
	}
	if stickyKeySource != nil {
		row.StickyKeySource = *stickyKeySource
	}
	if proxyRouteMode != nil {
		row.ProxyRouteMode = *proxyRouteMode
	}
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
	request_logs.plan_type,
	request_logs.conversation_id,
	request_logs.session_id,
	request_logs.service_tier,
	request_logs.actual_service_tier,
	request_logs.requested_service_tier,
	request_logs.useragent_group,
	request_logs.connection_request_kind,
	request_logs.failure_phase,
	COALESCE(request_logs.latency_ms, 0) AS latency_ms,
	COALESCE(request_logs.latency_first_token_ms, 0) AS latency_first_token_ms,
	COALESCE(request_logs.latency_response_created_ms, 0) AS latency_response_created_ms,
	COALESCE(request_logs.latency_first_upstream_event_ms, 0) AS latency_first_upstream_event_ms,
	request_logs.latency_queue_ms,
	request_logs.latency_response_create_gate_wait_ms,
	request_logs.latency_bridge_queue_wait_ms,
	request_logs.upstream_status_code,
	request_logs.upstream_error_code,
	request_logs.upstream_transport,
	request_logs.transport,
	request_logs.sticky_kind,
	request_logs.sticky_key_source,
	request_logs.upstream_proxy_route_mode`

// request_id is the response namespace and remains the point-query key. The
// measured 24-hour sample had different request_id and archive_request_id values
// on 6,139 of 6,231 rows; archive_request_id is a prefetch-only cache alias.
const lookupSQL = `
SELECT` + selectFields + `
FROM request_logs
LEFT JOIN api_keys ON api_keys.id = request_logs.api_key_id
WHERE request_logs.request_id = $1
	AND request_logs.deleted_at IS NULL
ORDER BY request_logs.id DESC
LIMIT 1`

// Several request_logs columns are structurally absent on the websocket-only
// deployment: bridge_stage, failure_exception_type, model_source_id, model_source_kind
// and source are never populated, while prewarm_status, session_previous_gap_ms and
// latency_bridge_queue_wait_ms are assigned only by the HTTP-bridge submit path. The
// selected latency_queue_ms and upstream_status_code remain nullable historical
// diagnostics; do not re-add the absent columns as a supposed fix.
const prefetchSQL = `
SELECT` + selectFields + `
FROM request_logs
LEFT JOIN api_keys ON api_keys.id = request_logs.api_key_id
WHERE request_logs.id > $1
	AND request_logs.deleted_at IS NULL
ORDER BY request_logs.id ASC
LIMIT $2`
