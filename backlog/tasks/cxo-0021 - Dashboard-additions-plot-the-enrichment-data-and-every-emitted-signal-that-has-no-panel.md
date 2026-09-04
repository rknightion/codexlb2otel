---
id: CXO-0021
title: >-
  Dashboard additions: plot the enrichment data and every emitted signal that
  has no panel
status: Parked
assignee:
  - '@codex'
created_date: '2026-09-03 14:11'
updated_date: '2026-09-04 22:30'
labels:
  - dashboards
dependencies:
  - CXO-0001
  - CXO-0003
  - CXO-0020
priority: high
type: feature
ordinal: 21000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
The 2026-09-03 sweep (codex/assessment-2026-09-03-dashboard-coverage.md) proved every metric and record type has a panel but large parts of what the exporter emits are never plotted, and the Postgres enrichment (CXO-0001) plus the family/effort/thread_source attributes (CXO-0003) add data that must be visualised the day it ships. All work is in dashboards/v2/generate.py (the JSON is generated, never hand-edited) and the result is pushed to the m7kni stack with gcx dashboards update. Every new panel excludes codexlb_family="probe" by default once CXO-0003 lands; add a $family variable.

A. Enrichment (new data, Postgres join):
- Overview stat row: spend in range (USD) and spend rate, from codexlb_cost_usd_total.
- Tokens & Cost tab: cost by model, cost by reasoning effort, cost by thread_source (user vs subagent), cost by family (what the probes cost), cost by API key name, cost per turn (cost / turns), cost per 1M tokens by model; cost split by request_kind so compaction and memory (invisible to codex-lb) are visible.
- Turns & Responses: codex-lb proxy status vs wire status disagreement (Loki turn body: proxy_status/proxy_error_code vs codexlb_status).
- Pipeline Health: enrichment lookups by result (cache_hit/db_hit/miss/error), lookup latency, cache entries, and a hit-rate stat.
B. Attributes carried but never used (from the sweep):
- codexlb.rate_limit.secondary_used_percent (no panel at all).
- codexlb.close_code split on transport events (v2 groups by frame_type only).
- codexlb.plan_type as a legend dimension on the rate-limit gauges.
- gen_ai.agent.version (instructions hash) churn: distinct versions per model over time, on codexlb_responses_total.
- gen_ai.response.model != gen_ai.request.model divergence on the duration histograms (safety-buffering re-runs).
- codexlb.family and reasoning level and thread_source on the token counters once CXO-0003 lands, replacing the LogQL unwrap workaround.
C. Ported from the legacy classic dashboards, which have no v2 equivalent:
- agent-message topology (author -> recipient) and parent/child thread pairs from 04-agent-tree.json (Loki | json on agent_message and turn bodies).
- a Lookup tab reproducing 08-response-thread-lookup.json: one $id variable matched against codexlb_request_id, gen_ai_response_id and codexlb_thread_id across every record type.
D. Traces tab: an invoke_agent spans panel, and execute_tool spans by gen_ai.tool.name. These are moot while otlp.traces.enabled is false on camden; build them anyway so enabling traces needs no dashboard change.
Deliberately NOT plotted, so nobody adds them later by mistake: gen_ai.provider.name (constant openai), gen_ai.operation.name (generateText|streamText carries no question anyone has asked), and the raw request_logs columns the codex-lb-overview dashboard already plots from Postgres directly.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 Every panel in sections A to D exists in generate.py, and generate.py verify() fails if any metric, record type or span name that the exporter emits has no panel
- [ ] #2 Regenerated JSON pushed to m7kni; gcx dashboards snapshot of the Tokens & Cost and Pipeline Health tabs shows live data in the new cost and enrichment panels within one hour of the enrichment deploy
- [x] #3 A $family variable exists and every cost, token and latency panel excludes probe by default
- [x] #4 docs/dashboards.md and dashboards/README.md describe the new tabs and the deliberate omissions
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [x] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
<!-- DOD:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
Wave 1 dashboard lane after enrichment and metrics wiring: add all specified panels, family filtering and deliberate omissions, then root publishes and verifies live.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Dashboard implementation landed at 7a54c59 and final selector fixes at 334a4db. It covers 63 of 63 metrics, 9 of 9 records, and 10 of 10 spans; family defaults exclude probe; documentation names the tabs and omissions; final just check passed. Generation 12 is live and normalized-spec identical. Panel 132 rendered the enrichment-disabled state and panel 136 rendered two breaking drift findings. Cost is absent and enrichment reports only disabled because no read-only Postgres DSN exists, so AC 2 is not proven. Resume after CXO-0001 has genuine db_hit and cost data, then capture both live panels within one hour.

Final 30 minute live sample still had only enrichment disabled=1,183 and no codexlb_cost_usd_total series. The cost and genuine enrichment half of AC 2 remains parked, not skipped or passed.

Final v0.4.0 deployment recheck: the live dashboard exactly matches the committed spec and the enrichment-disabled state renders, but Postgres has no DSN, enrichment emits disabled only, and codexlb_cost_usd_total returns no series. AC 2 remains parked until genuine db_hit and cost data arrive within the required observation window; dashboard presence alone is not a pass.
<!-- SECTION:NOTES:END -->
