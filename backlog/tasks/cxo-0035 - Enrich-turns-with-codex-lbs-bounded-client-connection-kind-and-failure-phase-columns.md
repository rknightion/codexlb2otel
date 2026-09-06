---
id: CXO-0035
title: >-
  Enrich turns with codex-lb's bounded client, connection-kind and failure-phase
  columns
status: To Do
assignee: []
created_date: '2026-09-06 09:23'
labels: []
dependencies: []
priority: high
type: enhancement
ordinal: 34000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
request_logs now carries columns the exporter does not read that are bounded enums and directly answer operator questions: useragent_group (6 distinct values, e.g. the TUI, exec, desktop and scripted clients), connection_request_kind (normal or prewarm; prewarm rows were 27 percent of the newest 50,000 rows and carry archive_request_id, so they may currently be counted as ordinary turns), failure_phase (upstream or downstream), and upstream_proxy_route_mode (1 distinct value today). Distinct counts were measured with SELECT-only queries on 2026-09-06. Prewarm traffic in particular can distort latency and volume the same way probe traffic did before codexlb.family existed (CXO-0003), so the connection kind needs to be a metric dimension a query can exclude. Client group is the dimension that makes usage-by-client answerable.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 The enrichment SELECT and prefetch read useragent_group, connection_request_kind and failure_phase; NULL maps to absent; fake-store tests cover present and NULL for each
- [ ] #2 codexlb.connection_kind is a Bounded attribute (cap 4) present on every turn-level instrument that carries codexlb.family, and the instruments state it in their attr.Only comment
- [ ] #3 codexlb.client_group is a Bounded attribute (cap 8) on the Loki turn record and the turn span, and on the request and cost counters only; the exact attribute-set tests pin it
- [ ] #4 codexlb.failure_phase is Bounded (cap 4), Loki body and span only
- [ ] #5 Live series for job codexlb2otel stay under 21,300 after deployment, measured with count({job="codexlb2otel"}) and quoted
- [ ] #6 The dashboard has a client-group breakdown and every latency and volume panel that excludes probe traffic also excludes prewarm connections; just dashboard-check is green
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
<!-- DOD:END -->
