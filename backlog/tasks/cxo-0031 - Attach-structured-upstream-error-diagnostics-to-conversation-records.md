---
id: CXO-0031
title: Attach structured upstream error diagnostics to conversation records
status: To Do
assignee: []
created_date: '2026-09-05 16:57'
updated_date: '2026-09-05 16:59'
labels: []
dependencies:
  - CXO-0001
references:
  - internal/enrich/postgres.go
  - internal/enrich/enrich.go
  - internal/turn/turn.go
  - ../codex-lb/app/db/models.py
  - dashboards/v2/generate.py
priority: medium
type: enhancement
ordinal: 30000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
Users can see the proxy error code on an enriched conversation but cannot inspect the upstream HTTP status and upstream error code preserved in request_logs. These distinguish authentication, capacity, quota and continuity failures while reviewing the affected conversation, without switching to an unrelated raw database dashboard. internal/enrich/postgres.go currently selects status, error_code and failure_phase only.

Read-only SQL on 2026-09-05 over request_logs.id <= 472100 (472,098 rows) found 75 non-null upstream_status_code values: 40 HTTP 404, 30 HTTP 401, four HTTP 503 and one HTTP 429. All 75 rows had an archive_request_id; none had a resp_-prefixed request_id. On these rows the upstream and proxy error codes agreed, but the HTTP status is additional evidence. Separately, 75 other rows had differing codes: 60 proxy stream_incomplete and 15 proxy codex_previous_response_stale, all retaining upstream previous_response_not_found and no upstream HTTP status. Across the full snapshot upstream_error_code was present in 469 rows, including 394 previous_response_not_found values. The existing bounded cached archive-ID join therefore matters for early-error enrichment. Recent IDs 462101..472100 had no such diagnostics, so historical population is not current universal coverage.

Scope is populated structured fields: upstream_status_code, upstream_error_code and upstream_transport. Do not bulk-export failure_detail, error_message, credentials, endpoint identifiers, client IPs or other freeform database content. failure_exception_type, bridge_stage and fallback fields were empty in the inspected snapshot and are not evidence-backed additions. Existing CXO-0001 owns enabling enrichment; CXO-0021 already owns the general status-disagreement view.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 Matched conversation logs and response spans retain upstream HTTP status, upstream error code and upstream transport distinctly from the existing proxy and archive status/error fields.
- [ ] #2 The existing bounded archive-ID cache path can correlate early errors without response IDs; unavailable or ambiguous matches remain explicitly missing and never trigger unindexed per-turn scans.
- [ ] #3 Users can locate affected conversations by these structured diagnostics through a documented Loki query or existing error view, including a proxy-versus-upstream-code mismatch.
- [ ] #4 Absent fields remain absent; arbitrary error strings and identifiers do not become unrestricted metric labels, and freeform private database bodies are excluded.
- [ ] #5 Focused validation covers differing upstream/proxy codes, archive-only joins and missing fields; live delivery evidence is distinguished from historical SQL population.
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
<!-- DOD:END -->
