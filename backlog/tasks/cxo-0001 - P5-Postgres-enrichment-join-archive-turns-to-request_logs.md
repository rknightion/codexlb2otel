---
id: CXO-0001
title: 'P5 - Postgres enrichment: join archive turns to request_logs'
status: In Progress
assignee:
  - '@codex'
created_date: '2026-08-14 16:58'
updated_date: '2026-09-03 18:48'
labels:
  - enhancement
  - from-gh-issue
dependencies: []
priority: high
type: enhancement
ordinal: 1000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
Migrated from the former GitHub issue tracker.\n
Phase P5 of the original build issue (#1). Postgres `request_logs` knows things the wire capture
cannot: `cost_usd`, the resolved `api_key_id`, and codex-lb's own view of the request. **Enrichment
source only.**

**Do not re-derive `request_logs`.** It already carries cost, latency, tokens, service tier and error
codes, and already drives the `codex-lb-overview` dashboard. Duplicating it was explicitly out of
scope in #1.

Scope: join archive turns to `request_logs` and attach `cost_usd` and `api_key_id`; LRU-cached;
degrade gracefully - Postgres unreachable must reduce the telemetry, never stop it, because the
archive pipeline is the primary source.

**Open question to resolve before building anything:** what actually joins them. The archive has
`request_id` in two namespaces (`ws_<hex32>` and bare UUID - both valid, do not "fix" this), plus
`response_id` and the server `turn_id`. Confirm against a live `request_logs` row which of these is
present there, and measure the join hit rate on the corpus first.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 Join key identified against live data, with the measured hit rate recorded in this task's notes
- [ ] #2 Postgres unavailable = enrichment absent, pipeline healthy; asserted by test
- [ ] #3 Cache hit rate exposed as a metric
- [ ] #4 No field re-derived that request_logs already publishes
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 make check passes: gofmt -l . reports nothing, go vet ./... clean, go test ./... green
- [ ] #2 go build ./... succeeds
<!-- DOD:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
Wave 1: implement the frozen pgx enrichment seam, unit-tested cache and failure behavior, then wire it at root and verify live on camden.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
RESOLVED 2026-09-03 by read-only discovery against the live DB (report: codex/assessment-2026-09-03-postgres-discovery.md, gitignored; the SQL outputs are beside it). The archive request_id is stored VERBATIM in request_logs.archive_request_id in both namespaces (ws_<hex32> 424,450 rows, bare UUID 4,302 rows, never NULL). Measured over two archive hours: 774/775 archive ids match exactly one row; every DB row in the window is in the archive. The wire's resp_ response id (gen_ai.response.id) is request_logs.request_id: 425/425 matched. archive_request_id has NO index (seq scan on a 1 GB table); request_id IS indexed. requested_at is written at completion, p50 9 ms after the last archive frame. FROZEN DESIGN for the build: (1) point lookup by response id against request_logs.request_id (indexed); (2) a prefetch that tails request_logs by id (PK) every poll so almost every lookup is a cache hit; (3) archive_request_id is matched only from prefetched rows, never as a point query. Fields to attach: cost_usd (float, computed by codex-lb from its pricing table; 98.8% non-null, sum USD 32,386 to date) and api_key_id (2 keys) plus the key's name from api_keys; also codex-lb's own status/error_code/failure_phase and the two proxy-side timings latency_response_created_ms and latency_first_upstream_event_ms, on the Loki turn body and the span only. DB request_kind is only normal|prewarm (compaction/memory are coerced to normal), so never take request_kind from the DB. Account id in the DB carries a _<8hex> suffix; the wire's bare UUID stays the label. Retention is disabled on request_logs (0), so replay from any checkpoint can still join. codex-lb-overview already plots the DB directly; nothing on it is re-derived here.
<!-- SECTION:NOTES:END -->
