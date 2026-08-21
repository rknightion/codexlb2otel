---
id: CXO-0010
title: Surface fast-mode effectiveness in full telemetry dashboard
status: Done
assignee: []
created_date: '2026-08-21 19:24'
updated_date: '2026-08-21 19:44'
labels: []
dependencies: []
references:
  - dashboards/v2/generate.py
type: feature
ordinal: 10000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
Make the Full Telemetry dashboard show whether Codex requests that ask for priority/fast processing are acknowledged by the response and whether their observed latency differs from normal/default traffic, both for the selected window and in aggregate. Preserve requested and served tier as separate facts; do not infer compliance from latency alone.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 Dashboard shows requested fast/priority volume and requested-versus-served outcomes with meaningful counts or rates
- [x] #2 Dashboard compares fast/priority and normal/default latency with sample size context and an aggregate effect measure that is not distorted by mixing models or request kinds
- [x] #3 Dashboard descriptions distinguish requested tier, reported served tier, and observed performance without claiming that response metadata proves actual queue treatment
- [x] #4 Generated dashboard is validated, visually inspected against Grafana Cloud data, and repository gates pass
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [x] #1 make check passes: gofmt -l . reports nothing, go vet ./... clean, go test ./... green
- [x] #2 go build ./... succeeds
<!-- DOD:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Verify the requested/served service-tier labels and latency histograms in current Grafana data, including whether normal traffic is represented as default, auto, or an absent requested label.

2. Add decision-oriented fast-mode volume, acknowledgement, latency, sample-size, and effect panels to the generated v2 dashboard while keeping model and request-kind comparisons honest.

3. Regenerate and validate the dashboard, push it to the existing m7kni dashboard context, render and inspect a snapshot, then run repository gates and review the final diff.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Live Grafana verification on m7kni: over the rendered 30-day window, fast share was 4.1% and reported priority rate was 0.0%. These are kept separate because response metadata does not prove queue treatment.

Validated generated schema with gcx resources validate; dry-run and live push both succeeded. Rendered the full dashboard plus panels 48, 49, 53, and 55. The first effect render exposed missing cohort labels; switched the effect panels to labeled bar gauges and re-rendered them successfully.

Live PromQL verified cohort masking and effect calculations. The 30-day TTFT comparison returned only cohorts with real fast and normal samples; no zero-sample cohort was shown as 100% faster.

Verification: make check passed including corpus tests; go build ./... passed; generator coverage reported 57/57 metrics, 9/9 Loki record types, 8/8 span names, 117 panels across 12 tabs. CodeRabbit completed on the final staged diff with no Critical or Warning findings; one minor typography suggestion was dismissed because the multiplication sign is user-facing mathematical prose.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Added and deployed a dedicated Fast Mode tab to the Full Telemetry dashboard. It separates requested priority from reported served tier, shows fast volume/share and metadata agreement, exposes exact TTFT and turn sample counts, and compares fast versus normal within model/request-kind cohorts. Verified queries against live m7kni data, validated and pushed the dashboard, visually iterated rendered panels, passed the full repository gate and build, and completed pre-push review.
<!-- SECTION:FINAL_SUMMARY:END -->
