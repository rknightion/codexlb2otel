---
id: CXO-0034
title: >-
  Explain why proxy queue and bridge-queue waits and upstream diagnostics never
  appeared live, and prove them over a real window
status: To Do
assignee: []
created_date: '2026-09-06 09:23'
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
- [ ] #1 A documented statement in docs/signals.md of which codex-lb request path populates each of the three waits, citing the codex-lb source location, and whether absent is expected for websocket-era rows
- [ ] #2 Either a fix with a failing-then-passing test for a mapping defect, or a test pinning that a row with connection_request_kind set and NULL queue wait is reported absent by design
- [ ] #3 A live m7kni query over at least 24 hours that returns at least one turn record with upstream_error_code set, or the report states the count was zero for that window with the query quoted
- [ ] #4 The dashboard's Errors tab upstream-diagnostic panels default to a window in which the signal is expected to be visible, and docs/dashboards.md says why
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
<!-- DOD:END -->
