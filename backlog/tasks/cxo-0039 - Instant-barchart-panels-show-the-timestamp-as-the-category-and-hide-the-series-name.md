---
id: CXO-0039
title: >-
  Instant barchart panels show the timestamp as the category and hide the series
  name
status: Done
assignee: []
created_date: '2026-09-06 11:13'
updated_date: '2026-09-06 12:36'
labels:
  - needs-triage
dependencies: []
priority: low
type: bug
ordinal: 38000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
Every horizontal barchart built from an instant PromQL query with legendFormat (Requests by client group, Cost by client group, Turns by originator and thread source, Cost split by request kind, Cost per 1M tokens by model) renders a single category labelled with the year (2026) and one bar per series; where the legend is hidden the series name is not shown anywhere, so the panel reads as one unlabelled bar. Seen on m7kni generation 15 snapshots of panels 28, 29, 34 and 51 on 2026-09-06. The barchart panel expects one row per category with a string field; an instant multi-series frame needs a transformation (series to rows, or reduce with the label as the string field) or a table-format query. The fix lives in dashboards/v2/generate.py: one shared helper for instant barcharts that adds the transformation, applied to every such panel, then just dashboard, just dashboard-check and a gcx dashboards snapshot of at least two of the panels as evidence.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 Each affected barchart renders one labelled bar per label value in a gcx dashboards snapshot, with no year on the category axis
- [x] #2 The generator applies the fix through one helper so no instant barchart can be added without it; just dashboard-check is green
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
<!-- DOD:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Fixed in dashboards/v2/generate.py: _instant_barchart() appends a reduce (seriesToRows, lastNotNull) plus an organize rename for every barchart with an instant Prometheus query, sets colorByField and hides the redundant legend; instant_barchart_findings() runs inside verify() and a stripped panel produced one finding in the negative check. Six barcharts affected (28, 29, 34, 51, 53, 87). Deployed to m7kni as dashboard generation 17; snapshots of panel 28 (codex-tui) and panel 51 over 3h (memory, compaction, prewarm, turn) show named categories and no year on the axis. just check green; CodeRabbit complete with 0 findings.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Commit bfcf800: central series-to-rows reduce for instant barcharts with a verify() lint; m7kni generation 17 renders named categories (snapshots of panels 28 and 51).
<!-- SECTION:FINAL_SUMMARY:END -->
