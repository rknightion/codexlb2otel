---
id: CXO-0030
title: Enrich conversation latency with proxy admission and queue waits
status: To Do
assignee: []
created_date: '2026-09-05 16:57'
labels: []
dependencies:
  - CXO-0001
references:
  - internal/enrich/postgres.go
  - internal/enrich/enrich.go
  - internal/turn/turn.go
  - internal/sink/otlpmetric/record.go
  - ../codex-lb/app/db/models.py
priority: medium
type: enhancement
ordinal: 29000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
Archive engine timings describe upstream model/harness work but do not explain time spent waiting inside codex-lb before an attempt. request_logs.latency_queue_ms explicitly measures account selection, admission and failed failover attempts, outside the successful-attempt latency/TTFT anchor (codex-lb/app/db/models.py:491-500). The current exporter selects only response-created and first-upstream-event proxy timings (internal/enrich/postgres.go:92-105).

Read-only SQL on 2026-09-05, bounded by request_logs.id <= 472100, inspected 472,098 rows spanning 2026-08-03 to 2026-09-05. latency_queue_ms was present and positive in 7,606 rows, maximum 405 ms; 7,497 had a resp_-prefixed request_id and all 7,606 had archive_request_id. Bridge queue wait was present in 3,354 rows and positive in 14; response-create gate wait was positive in 14; both maxima were 2 ms. In IDs 462101..472100, queue wait was present in only 40/10,000 rows (p50 25 ms, p95 44.3 ms, max 63 ms), gate wait was 9,742 measured zeros and bridge wait was absent. These observations support a separate proxy-wait signal, not a claim that queueing is currently a major bottleneck.

This extends optional per-conversation enrichment; it must not duplicate the standalone raw request_logs dashboard. Activation and live enrichment evidence remain owned by CXO-0001.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 Matched turn logs and response spans expose the three named proxy wait measurements separately from upstream engine queue/critical-path measurements.
- [ ] #2 Proxy wait distributions are available using existing bounded cohort dimensions, include measured zeros, and exclude missing values from observations; a coverage count or ratio makes sparse population visible.
- [ ] #3 Documentation states units, measurement anchors and possible overlap; no composite end-to-end total is asserted by summing fields without demonstrated non-overlap.
- [ ] #4 Optional Postgres behavior and the indexed response-ID / bounded cached archive-ID join contract remain intact; retries/replay do not multiply one row within the existing delivery semantics.
- [ ] #5 An operator can inspect proxy wait alongside model wait for the same response through a documented query or existing latency view; live acceptance uses enabled enrichment and reports absent fields honestly.
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
<!-- DOD:END -->
