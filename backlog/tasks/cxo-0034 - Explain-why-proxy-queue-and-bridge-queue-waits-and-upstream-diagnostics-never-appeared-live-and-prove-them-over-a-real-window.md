---
id: CXO-0034
title: >-
  Explain why proxy queue and bridge-queue waits and upstream diagnostics never
  appeared live, and prove them over a real window
status: Parked
assignee:
  - '@codex'
created_date: '2026-09-06 09:23'
updated_date: '2026-09-06 10:50'
labels: []
dependencies: []
priority: high
type: bug
ordinal: 33000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
Wave 2 (CXO-0030, CXO-0031) shipped the proxy wait histogram, the wait coverage counter and the upstream diagnostic fields, but in the 30-minute post-deploy window only response_create_gate waits were present; queue and bridge_queue coverage only ever incremented as absent, and no turn carried an upstream error code. A SELECT-only measurement of the newest 50,000 request_logs rows on 2026-09-06 shows latency_queue_ms populated on 6,932 rows, but only on rows whose connection_request_kind is NULL; every row with connection_request_kind set (normal or prewarm) has NULL queue and bridge waits while 84 percent carry a gate wait. upstream_error_code is set on 115 of those rows and upstream_status_code on 87, so upstream diagnostics exist at roughly 0.2 percent of turns and a 24-hour window should show them. Establish, from codex-lb's own source for the columns (app/db/models.py and where each latency column is written), which request paths populate each wait, whether the exporter's enrichment mapping in internal/enrich/enrich.go is reading the right column for the right path, and whether the coverage counter's absent result is truthful or hides a mapping defect.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 A documented statement in docs/signals.md of which codex-lb request path populates each of the three waits, citing the codex-lb source location, and whether absent is expected for websocket-era rows
- [x] #2 Either a fix with a failing-then-passing test for a mapping defect, or a test pinning that a row with connection_request_kind set and NULL queue wait is reported absent by design
- [x] #3 A live m7kni query over at least 24 hours that returns at least one turn record with upstream_error_code set, or the report states the count was zero for that window with the query quoted
- [ ] #4 The dashboard's Errors tab upstream-diagnostic panels default to a window in which the signal is expected to be visible, and docs/dashboards.md says why
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [x] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
<!-- DOD:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
Wave 3: execute the frozen lane ownership and decisions in codex/goal-2026-09-06-wave3.md. Root commits wave 0 before dispatch; L2/L7 depend on L1 and the recorded D15 verdict. Integrate, run the rewritten corpus gate once after wiring, review, push once, verify Watchtower and m7kni, then reconcile acceptance and resume boundaries.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Wave3 final source 3bc28996c78c3533ae30f370f01a247aa6dea3ac. One git push was rejected (fetch first); zero successful pushes. Remote advanced by Renovate at10:16:57Z. No second attempt under the one-push contract. L1 verdict (a): no wait mapping defect, structural NULL by request path. Synthetic NULL/zero tests and source-cited docs pass. Live 24h Loki aggregate at 2026-09-06T10:48:00Z counted 2 upstream-error turn records. Generated upstream panels use v2 queryOptions.timeFrom=24h; deployment of those panels remains unproven. Resume in a newly authorized publication run: fetch and reconcile remote main 72c29953f4d6a03528cbf0c2f319d30900cbaf01 with the preserved local commits, run the integrated gate on the reconciled tree, authorize a new push, verify CI/publish and Watchtower at that SHA, then update and read back the m7kni dashboard and prove the new dimensions live.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Source 3bc28996c78c3533ae30f370f01a247aa6dea3ac; no pushed SHA. L1 verdict (a): no wait mapping defect, structural NULL by request path. Synthetic NULL/zero tests and source-cited docs pass. Live 24h Loki aggregate at 2026-09-06T10:48:00Z counted 2 upstream-error turn records. Generated upstream panels use v2 queryOptions.timeFrom=24h; deployment of those panels remains unproven. Resume in a newly authorized publication run: fetch and reconcile remote main 72c29953f4d6a03528cbf0c2f319d30900cbaf01 with the preserved local commits, run the integrated gate on the reconciled tree, authorize a new push, verify CI/publish and Watchtower at that SHA, then update and read back the m7kni dashboard and prove the new dimensions live.
<!-- SECTION:FINAL_SUMMARY:END -->
