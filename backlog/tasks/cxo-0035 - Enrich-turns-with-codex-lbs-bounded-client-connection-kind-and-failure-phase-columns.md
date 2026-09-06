---
id: CXO-0035
title: >-
  Enrich turns with codex-lb's bounded client, connection-kind and failure-phase
  columns
status: Done
assignee:
  - '@codex'
created_date: '2026-09-06 09:23'
updated_date: '2026-09-06 11:28'
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
- [x] #1 The enrichment SELECT and prefetch read useragent_group, connection_request_kind and failure_phase; NULL maps to absent; fake-store tests cover present and NULL for each
- [x] #2 codexlb.connection_kind is a Bounded attribute (cap 4) present on every turn-level instrument that carries codexlb.family, and the instruments state it in their attr.Only comment
- [x] #3 codexlb.client_group is a Bounded attribute (cap 8) on the Loki turn record and the turn span, and on the request and cost counters only; the exact attribute-set tests pin it
- [x] #4 codexlb.failure_phase is Bounded (cap 4), Loki body and span only
- [x] #5 Live series for job codexlb2otel stay under 21,300 after deployment, measured with count({job="codexlb2otel"}) and quoted
- [x] #6 The dashboard has a client-group breakdown and every latency and volume panel that excludes probe traffic also excludes prewarm connections; just dashboard-check is green
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
Wave3 final source 3bc28996c78c3533ae30f370f01a247aa6dea3ac. One git push was rejected (fetch first); zero successful pushes. Remote advanced by Renovate at10:16:57Z. No second attempt under the one-push contract. Enrichment SELECT/scanner and signal scopes implemented; exact-set and NULL/zero tests pass. Generated 150-panel dashboard passes coverage and prewarm lint. No post-wave deployment occurred: existing runtime has 2232 series but no connection/client labels on the 581 turn aggregate, so this is not AC5 or live rollout proof. Dashboard remains generation14. Resume in a newly authorized publication run: fetch and reconcile remote main 72c29953f4d6a03528cbf0c2f319d30900cbaf01 with the preserved local commits, run the integrated gate on the reconciled tree, authorize a new push, verify CI/publish and Watchtower at that SHA, then update and read back the m7kni dashboard and prove the new dimensions live.

Publication resumed 2026-09-06: pushed cce4673, CI 34029098196 and publish 34029098538 green, camden deployed 11:10 UTC healthy. Live at 11:36 UTC: count({job="codexlb2otel"}) = 982 (budget 21,300); codexlb_turns_total carries codexlb_connection_kind=normal and codexlb_client_group=codex-tui (61 turns in 25 min). connection_kind=prewarm was NOT observed on any instrument within 25 minutes of the deploy; the 27 percent prewarm-connection share in request_logs evidently does not reach the reducer as request_kind=turn on this traffic, consistent with L1's caveat. Dashboard generation 15 spec-equal; just dashboard-check 150 panels; prewarm-exclusion lint green. Snapshots of panels 28, 29 and 33 rendered; panels 28/29 show the pre-existing instant-barchart category defect now tracked as CXO-0039.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Source 3bc28996c78c3533ae30f370f01a247aa6dea3ac; no pushed SHA. Enrichment SELECT/scanner and signal scopes implemented; exact-set and NULL/zero tests pass. Generated 150-panel dashboard passes coverage and prewarm lint. No post-wave deployment occurred: existing runtime has 2232 series but no connection/client labels on the 581 turn aggregate, so this is not AC5 or live rollout proof. Dashboard remains generation14. Resume in a newly authorized publication run: fetch and reconcile remote main 72c29953f4d6a03528cbf0c2f319d30900cbaf01 with the preserved local commits, run the integrated gate on the reconciled tree, authorize a new push, verify CI/publish and Watchtower at that SHA, then update and read back the m7kni dashboard and prove the new dimensions live.

Pushed SHA cce4673 (CI 34029098196, publish 34029098538, camden 11:10 UTC). Live series 982 of 21,300 budget; connection_kind=normal and client_group=codex-tui present live; prewarm not observed in the first 25 minutes. Dashboard generation 15 spec-equal with client-group breakdown and prewarm exclusions; dashboard-check green.
<!-- SECTION:FINAL_SUMMARY:END -->
