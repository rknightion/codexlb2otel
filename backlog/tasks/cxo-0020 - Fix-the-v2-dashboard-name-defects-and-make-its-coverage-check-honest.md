---
id: CXO-0020
title: Fix the v2 dashboard name defects and make its coverage check honest
status: In Progress
assignee:
  - '@codex'
created_date: '2026-09-03 13:51'
updated_date: '2026-09-03 18:48'
labels:
  - dashboards
dependencies: []
priority: high
type: bug
ordinal: 20000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
Found by a 2026-09-03 emitted-vs-visualised sweep of dashboards/v2/codexlb2otel-full.json against the Go source (assessment saved at codex/assessment-2026-09-03-dashboard-coverage.md, gitignored; regenerate with the scripts described there if missing). Three panels are silently wrong or missing, and the generator cannot detect any of them.

Defects:
1. Tools & Agents / "Tool calls per operation" (3 queries, p50/p95/p99) queries codexlb_tool_calls_per_operation_bucket. That name is not on the wire: the instrument is gen_ai.client.tool_calls_per_operation (internal/attr/names.go MetricToolCallsPerOperation), mangled gen_ai_client_tool_calls_per_operation_bucket. The panel is permanently empty.
2. Latency & Critical Path / "gen_ai client operation duration (by status)" groups sum by (le, status). The real label is codexlb_status (attr.Status mangled). PromQL folds every series into one unlabeled group, the legend renders {{status}} as empty, and the panel never splits fast errors from slow ones, which is its stated purpose.
3. codexlb.rate_limit.secondary_used_percent (MetricRateLimitUsed2) has no panel at all and is absent from generate.py ALL_METRICS/PROM_NAME and from .metrics_from_code.txt.

Why the generator did not catch them: verify() compares two hand-typed dicts in the same file against a hand-regenerated sidecar. The sidecar regen command in the generate.py comment uses [A-Za-z]+ for the constant name, so MetricRateLimitUsed2 (the only Metric* constant with a digit) is invisible even to a fresh regen. Nothing runs the regen (no just recipe, no CI step), and the committed sidecar is dated 2026-08-08 with the pre-rename tool_calls name. Coverage is "the query string contains the declared name", so a wrong name declared in both places passes. SPAN_NAMES omits invoke_agent (the second toolOperationName value) and critical_path.client_tool_pause / critical_path.other. dashboards/scripts/check_names.py globs dashboards/*.json non-recursively, so it never inspects dashboards/v2/ at all; it does catch the identical stale name in the legacy 05-tool-usage.json (exit 1 today).

Not in scope: adding panels for signals that have none (that is the dashboard-additions task). Not in scope: the legacy 0*.json dashboards beyond leaving check_names.py green or documenting why it is red.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 The three panels above query the real wire names and render series on the live m7kni stack (evidence: gcx dashboards snapshot or a query against grafanacloud-prom returning data for each)
- [ ] #2 The metric sidecar is produced by a just recipe (group gen) whose extraction matches every Metric* constant including ones with digits, and just check fails when the committed sidecar is stale
- [ ] #3 generate.py verify() fails when a panel query references a metric wire name or a label that the source does not emit, not only when a declared name has no panel; check_names.py (or its replacement) covers dashboards/v2/
- [ ] #4 SPAN_NAMES enumerates every span name the otlptrace sink emits, including invoke_agent and every critical_path.* phase, and verify() fails when one has no panel
- [ ] #5 Regenerated dashboards/v2/codexlb2otel-full.json is pushed to m7kni with gcx dashboards update and the live spec matches the committed file
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
<!-- DOD:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
Wave 1 dashboard lane after wiring: repair real wire names and labels, make sidecar and coverage validation honest, regenerate, then root publishes and reads back.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Correction 2026-09-03 (live-verified on Mimir, not derived): the instrument declares unit 'count', and the translator appends the unit as a suffix, so the real wire name is gen_ai_client_tool_calls_per_operation_count_bucket / _count_sum / _count_count (256 bucket series live), NOT gen_ai_client_tool_calls_per_operation_bucket as the assessment file states. Fix the panel to the _count_ form. Live baseline for reference: ~7,100 active series total for job=codexlb2otel, dominated by gen_ai_client_token_usage_bucket (1,140).
<!-- SECTION:NOTES:END -->
