---
id: CXO-0009
title: Model Usage tab for the full-telemetry dashboard
status: Done
assignee: []
created_date: '2026-08-17 07:36'
updated_date: '2026-08-17 07:36'
labels:
  - enhancement
dependencies: []
priority: medium
ordinal: 9000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
Add a Model Usage tab covering model usage over time, grouped and ungrouped by reasoning effort, across a mix of panel types. Fix two silent pre-existing defects found while in there.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 Model Usage tab exists in generate.py with widths/heights in tabs_def
- [x] #2 Usage over time by model alone, and by model x reasoning effort
- [x] #3 A variety of viz types, not all timeseries
- [x] #4 Effort-carrying panels query only the two instruments that have the label
- [x] #5 generate.py exits 0 with coverage OK; JSON regenerated and committed
- [x] #6 Pushed to the stack and visually verified by rendering the panels
- [x] #7 README attribute-matrix note clarified: effort is absent from token instruments only
- [x] #8 Prometheus table panels set format table; schema-v2 transformation wrapper corrected
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 make check passes: gofmt -l . reports nothing, go vet ./... clean, go test ./... green
- [ ] #2 go build ./... succeeds
<!-- DOD:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Done in c1a48cd.

Twelve panels: response and turn rate by model; response rate by model x effort; model mix and effort mix as percent-stacked areas; donut, pie and bar-gauge whole-window splits; a model x effort x kind table; effort rate as bars; token rate by model.

The fact the tab is built on: codexlb.responses and codexlb.turns are the ONLY instruments carrying gen_ai.request.reasoning.level alongside gen_ai.request.model. Confirmed live against the metrics backend (series API, 3-day window): 731 and 616 series respectively, both label sets including the reasoning level. No token, duration or tool-call instrument has it. dashboards/README.md read as though effort were unavailable to PromQL generally; it now says which half is which, because routing a request-count panel through LogQL for that reason is a mistake waiting to happen.

Two silent pre-existing defects fixed while in there, both of which pass validation AND a live server-side dry-run, so only rendering the panel catches them:

1. A Prometheus query feeding a table panel must set format: table. Without it the instant result arrives as a wide time-series frame - the whole label set collapsed into one column header, with a series picker at the bottom - so the panel renders, has a number in it, and is not a table. Six panels were shipping like that. Now applied centrally in panel(), so a new table panel cannot reintroduce it.
2. The schema-v2 transformation wrapper is kind Transformation with the transformation id in group and the payload under spec.options. Writing the id as kind mirrors the classic schema, validates, pushes, and is then discarded - which is why the response outcome matrix showed raw label names and none of its excluded columns. Now built by organize().

Also pinned the folder annotation in the manifest metadata: without it a push is free to land the dashboard in General and silently detach it from its folder.

Verified by rendering panels, not by validating JSON.
<!-- SECTION:NOTES:END -->
