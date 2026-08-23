---
id: CXO-0001
title: 'P5 - Postgres enrichment: join archive turns to request_logs'
status: To Do
assignee: []
created_date: '2026-08-14 16:58'
updated_date: '2026-08-23 12:10'
labels:
  - enhancement
  - from-gh-issue
dependencies: []
priority: low
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
- [ ] #1 Join key identified against live data, with the measured hit rate recorded in this task's notes
- [ ] #2 Postgres unavailable = enrichment absent, pipeline healthy; asserted by test
- [ ] #3 Cache hit rate exposed as a metric
- [ ] #4 No field re-derived that request_logs already publishes
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 make check passes: gofmt -l . reports nothing, go vet ./... clean, go test ./... green
- [ ] #2 go build ./... succeeds
<!-- DOD:END -->
