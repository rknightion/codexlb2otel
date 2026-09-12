---
id: CXO-0045
title: 'Panels, naming checks and documentation for every metric this wave adds'
status: In Progress
assignee:
  - '@codex'
created_date: '2026-09-12 10:09'
updated_date: '2026-09-12 11:03'
labels:
  - dashboards
  - docs
dependencies:
  - CXO-0041
  - CXO-0042
  - CXO-0043
  - CXO-0044
priority: medium
type: feature
ordinal: 44000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
The dashboard generator is a hard gate, not a nicety. just check runs dashboard-check, which regenerates dashboards/v2/.metrics_from_code.txt from every Metric constant in internal/attr/names.go and compares it byte for byte, then runs dashboards/v2/generate.py and compares the generated dashboard JSON. generate.py itself fails with "N metric(s) have no panel" when a declared metric has no panel, and it enforces the same for Loki record types and span names.

So every new Metric constant breaks the repository gate until a panel exists for it. That makes generate.py a single append-only registry every metric-adding change must touch, and dashboards/v2/.metrics_from_code.txt and dashboards/v2/codexlb2otel-full.json generated artifacts that must be regenerated exactly once, at the end, rather than by each change in turn.

This task owns that pass for the whole wave. It is scheduled last and depends on every metric-adding task, taking their frozen metric names as its input.

Documentation that is stale as of 2026-09-12 and belongs here:
- docs/operations.md describes the enrichment row and the proxy-wait columns without saying that two of the three wait kinds are structurally absent on a websocket-only deployment.
- The Camden enrichment check section predates the account and quota gauges entirely.
- AGENTS.md records the read-only role as holding SELECT on three tables; it now holds six.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 Every metric constant added by this wave has a panel, and generate.py reports no uncovered metric
- [ ] #2 dashboards/v2/.metrics_from_code.txt and dashboards/v2/codexlb2otel-full.json are regenerated once and just dashboard-check passes
- [ ] #3 dashboards/scripts/check_names.py passes, including its own unit test
- [ ] #4 A panel shows quota headroom for every account including those serving no traffic, and a panel shows key eligibility
- [ ] #5 docs/operations.md states which proxy-wait kinds are structurally absent on a websocket-only deployment and why
- [ ] #6 AGENTS.md records the six tables the read-only role now selects from
- [ ] #7 just docs-links passes
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
<!-- DOD:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Consume the frozen metric registry after wave 0.
2. Add one panel per new metric and update operations and role documentation.
3. Regenerate artifacts once and validate naming, dashboard coverage and links.
<!-- SECTION:PLAN:END -->
