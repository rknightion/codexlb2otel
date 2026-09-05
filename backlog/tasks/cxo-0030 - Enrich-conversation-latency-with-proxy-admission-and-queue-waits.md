---
id: CXO-0030
title: Enrich conversation latency with proxy admission and queue waits
status: Done
assignee:
  - '@codex'
created_date: '2026-09-05 16:57'
updated_date: '2026-09-05 21:46'
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
- [x] #1 Matched turn logs and response spans expose the three named proxy wait measurements separately from upstream engine queue/critical-path measurements.
- [x] #2 Proxy wait distributions are available using existing bounded cohort dimensions, include measured zeros, and exclude missing values from observations; a coverage count or ratio makes sparse population visible.
- [x] #3 Documentation states units, measurement anchors and possible overlap; no composite end-to-end total is asserted by summing fields without demonstrated non-overlap.
- [x] #4 Optional Postgres behavior and the indexed response-ID / bounded cached archive-ID join contract remain intact; retries/replay do not multiply one row within the existing delivery semantics.
- [x] #5 An operator can inspect proxy wait alongside model wait for the same response through a documented query or existing latency view; live acceptance uses enabled enrichment and reports absent fields honestly.
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [x] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
<!-- DOD:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
Wave 2: commit root-owned seams first; frozen owned lanes implement with synthetic tests; integrate, just check, one corpus confidence gate and CodeRabbit; one push; watchtower-only deploy observation and m7kni proof; reconcile acceptance by evidence layer.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Clean main e8e97fd directly descends from c567894 and equals origin/main. CI 33988760737 and release-please 33988761017 succeeded. D8 holds; traces and agento11y remain disabled.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Wave 2 delivered at aca5e5de0bcd4ba6f5fd72dfaa6caef3a6c7fb71. NULL/zero/present enrichment and exact metric attribute/replay tests pass. Live response_create_gate histogram has 4 series; queue and bridge_queue absent coverage increases. Dashboard generation 14 matches committed spec and panel 67 rendered/visually checked. Two-minute post-deploy cost increase 1.6304492080183701 and db_hit increase 56.00386935824657. just check passed at that source SHA; CI 33993480890 success. Watchtower deployed that SHA healthy, restart count 0. Publish 33993481221 failed signing after successful manifest push; run-level publication completion remains open. The single D20 corpus gate was canceled (exit 143), so corpus confidence is not proven. Final tracker closeout is local under the one-push contract.
<!-- SECTION:FINAL_SUMMARY:END -->
