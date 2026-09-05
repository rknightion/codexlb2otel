---
id: CXO-0026
title: >-
  Make priority/fast-mode value answerable at session level, not just per
  response
status: Done
assignee:
  - '@codex'
created_date: '2026-09-04 17:38'
updated_date: '2026-09-05 17:22'
labels: []
dependencies: []
references:
  - internal/sink/otlpmetric/record.go
  - internal/sink/otlptrace
  - dashboards/v2/generate.py
priority: medium
type: feature
ordinal: 20000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
Priority mode's per-response win is measurable today, but its value across a whole wave session is not. Closing that needs the requested tier on four more signals plus one missing label on the rate-limit gauges.

## What was measured (2026-09-04)

Window 2026-08-27 to 2026-08-31, the only period where both tiers ran interleaved on the same days. 28,877 priority turns vs 34,781 default, request_kind=turn.

Per turn, priority wins clearly:

| | default | priority | gain |
|---|---|---|---|
| TTFT (avg) | 1023 ms | 520 ms | 1.97x |
| engine queue wait (avg) | 3312 ms | 1177 ms | 2.81x |
| time-between-tokens | 19.3 ms | 13.5 ms | 1.43x |
| model round trip excl. client tools | 7967 ms | 5846 ms | 1.36x |
| client tool pause | 6482 ms | 5731 ms | 1.13x |
| engine calls / cache hit ratio | 0.98 / 0.93 | 0.99 / 0.93 | flat |

The win holds into the tail on model round-trip seconds, per model:

```text
gpt-5.6-sol    p50 4.70 -> 3.26   p90 13.19 -> 9.52   p99 43.21 -> 29.89
gpt-5.6-terra  p50 5.84 -> 3.93   p90 20.42 -> 14.42  p99 68.57 -> 44.73
gpt-5.6-luna   p50 5.40 -> 3.93   p90 20.29 -> 12.15  p99 65.24 -> 43.50
```

TBT matters more than TTFT. At ~330 output tokens per response the decode-rate saving is ~1.9s, roughly 4x the 0.5s TTFT saving.

At session level it mostly washes out. 319 sessions of 40+ turns reconstructed offline from Loki turn records (177 default, 142 priority):

| | default | priority |
|---|---|---|
| median turns/session | 78 | 100 |
| median model time per turn | 9.29 s | 6.45 s (1.44x) |
| median wall clock per turn | 16.10 s | 14.66 s (1.10x) |
| median out-tokens per wall-second | 25.07 | 25.78 (+3%) |
| mean out-tokens per wall-second | 24.47 | 28.78 (+18%) |
| median turns per wall-minute | 3.73 | 4.09 (+10%) |
| median subagent share of turns | 100% | 100% |
| unaccounted residual (not model, not tool pause) | 6.8% | 7.5% |

A 1.44x per-turn model win compresses to roughly 3-10% on median session throughput. Mechanism: long sessions are entirely fan-out, so wall clock is set by the slowest lane per wave, not the mean, and the tool-pause half of the cycle does not move.

Confounds, not controlled: cohorts are not randomised, model mix differs (terra 14.9% -> 31.1%, sol 68.3% -> 52.2%), and priority sessions ran larger jobs.

## Current signal coverage

Carries `codexlb_service_tier_requested` (deliberate, record.go:255):

- `gen_ai_client_time_to_first_token_seconds`
- `gen_ai_client_operation_duration_seconds`
- `codexlb_turn_duration_seconds`

Narrowed out everywhere else, so unaskable in PromQL: the engine wall / queue / TBT breakdown, `codexlb_harness_unblocked_seconds`, `codexlb_responsesapi_excl_client_tools_seconds`, tokens, tool calls, errors, transport events, and every rate-limit and credits gauge.

Loki is the only complete session-level source today. `{service_name="codexlb2otel", codexlb_record_type="turn"}` carries thread_id, parent_thread_id, session_id, logical_turn_seq, all four timestamps, both tier fields, is_subagent, every `*_delta` critical-path field, tokens and queue wait. It needs an offline pull: LogQL caps at 5000 entries per query, so the 4-day window above took 48 chunked calls.

Tempo has the session shape but not the tier. Root spans are named `turn`, one observed span covered 2.2 hours, `gen_ai.conversation.id` is present, and there are zero `codexlb.*` span attributes.

Separately: the rate-limit gauges carry neither tier nor a window label, so "does priority burn quota faster" cannot be answered at all. Account `<redacted account>` was at 98% used when this was written.

## Scope

Four changes, smallest first. Each adds at most one extra label value per deployment since only one tier is requested at a time, so the cardinality cost is ~nothing outside a changeover.

1. `codexlb.service_tier_requested` on the turn span. This is the one that changes the workflow: session-level becomes a TraceQL query instead of a 48-call chunked pull.
2. Same attribute on `codexlb_harness_unblocked_seconds` and `codexlb_responsesapi_excl_client_tools_seconds`. Those two split waiting-on-the-model from waiting-on-the-client, which is the ratio that decides whether priority can help at all.
3. Same attribute on `codexlb_tokens_total` and `codexlb_errors_total`, for the cost and reliability side.
4. A window label on the rate-limit gauges (`codexlb_rate_limit_used_percent`, `codexlb_rate_limit_model_used_percent`, `codexlb_rate_limit_reset_after_seconds`) so 5h and weekly quotas are separable, and the tier attribute alongside it if the wire carries enough to attribute burn.

The existing Fast Mode tab (CXO-0010) covers requested-vs-served tier and TTFT cohorts. It is not being rebuilt here; it gains panels once the signals above exist.

Do not use served tier as evidence of anything. Per `internal/turn/turn.go`, every request since the 2026-08-08 cutover asked for priority and no response has ever reported it back.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 The turn span carries `codexlb.service_tier_requested`; synthetic span evidence covers priority and absent-label normal turns, and any session query classifies pure-priority, pure-normal, and mixed sessions into disjoint cohorts.
- [x] #2 `codexlb_harness_unblocked_seconds` preserves `codexlb_critical_path_coverage` and `codexlb_family` while adding `codexlb_service_tier_requested`; `codexlb_responsesapi_excl_client_tools_seconds` preserves `codexlb_family` while adding the same tier label.
- [x] #3 `codexlb_tokens_total` preserves provider, operation, request model, response model, account, request kind, family, reasoning effort, thread source, API key, and `gen_ai_token_type` while adding `codexlb_service_tier_requested`; `codexlb_errors_total` preserves provider, operation, request model, account, error type, error code, and status while adding the tier label.
- [x] #4 Account and per-model rate-limit gauges carry `codexlb_rate_limit_window_minutes`; primary and secondary windows are emitted distinctly, and requested-tier attribution is recorded as a hard negative unless the wire identifies which request consumed the shared quota.
- [x] #5 The attribute registry maps dotted span and metric key `codexlb.service_tier_requested` to Prometheus label `codexlb_service_tier_requested`, registers the bounded window label, updates exact-set and cardinality guards, and records that neither custom key has a GenAI semantic-convention equivalent.
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [x] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
- [x] #2 Focused tests cover priority, absent normal, both quota windows, and exact attribute preservation without widening unrelated instruments.
<!-- DOD:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Pin the existing turn-span mapping with a synthetic priority-versus-absent regression. 2. Add requested tier only to the two named wait histograms, token counter, and error counter while exact-set tests preserve every existing attribute. 3. Register a bounded rate-limit window-minutes key, retain both primary and secondary per-model windows, and keep requested tier off shared quota snapshots as a measured hard negative. 4. Determine whether current Tempo can aggregate all thread traces for one session; implement the dashboard only with an authoritative cross-trace source, otherwise park that criterion on the missing session aggregate and traces-disabled deployment. 5. Run focused races, cardinality validation, generated-dashboard checks, just check, and review before terminal task state.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Implemented requested-tier propagation on the turn span and the four scoped metric families while preserving their existing selectors. The operation-duration selector already claimed requested tier in its source comment and task baseline but had omitted it in code; restored that pre-existing declared contract and pinned it in the focused test.

Added bounded caller-supplied codexlb.rate_limit.window_minutes, emitted it on account and per-model gauges, retained primary and secondary per-model windows plus both reset intervals, and kept codexlb.service_tier_requested off every shared quota snapshot because the wire does not attribute account-wide quota consumption to the current request.

AC 5 is not implementable honestly from the current trace model: trace IDs are derived from ThreadID, while one session spans a parent thread plus subagent thread traces. Current Tempo panels aggregate spans or individual traces and no authoritative cross-trace distinct-turn session aggregate is available. The frozen deployment also has traces disabled. Do not substitute deployment-level throughput for session throughput.

Verification: focused race tests passed; just check passed in 14.8s, including fmt, vet, golangci-lint, build, corpus-free race tests, dashboard checks, and the intentionally absent-corpus probe path. Per the revised run contract and user direction, the potentially multi-hour just test-corpus gate was not run for this non-decoder change.

CodeRabbit pass 1 completed with one major finding: the dashboard name checker guarded probe exclusion on counters but not latency histograms. Fixed it by covering every family-bearing latency histogram, adding a regression test for an unfiltered turn-duration query, and adding explicit non-probe selectors to the legacy latency dashboard. The post-fix just check passed in 10.3s.

CodeRabbit pass 2 completed with three major findings, all verified and fixed: per-term probe-selector validation with a mixed-expression regression; archive retention documented as independent OR rules; and Postgres documentation aligned with lazy-pool behavior for a syntactically valid but unreachable DSN. Two-pass review ceiling reached; no third pass run. Final just check passed in 5.7s. AC 5 remains unproven and is the sole park.

Final v0.4.0 deployment recheck: rate-limit window labeling is live with distinct 300-minute and 10080-minute per-model windows. Requested-tier labels were absent from the immediate response and turn-duration series, so no live priority cohort is claimed. AC 5 remains parked because sessions span multiple thread-derived traces, traces are disabled, and no authoritative cross-trace distinct-turn session aggregate exists.

2026-09-05: AC 5 (session-level throughput panel) removed on Rob's decision. It required an authoritative cross-trace distinct-turn session aggregate; trace IDs derive from ThreadID and a fan-out session spans a parent thread plus subagent threads, so neither Tempo nor the traces-disabled deployment can provide it. Recorded here so it is not re-added as a defect.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Delivered requested-tier span and bounded metric attribution, distinct account and per-model quota windows, exact-set and absent-normal regressions, dashboard legend and probe-filter validation, and corrected related operator documentation. Focused race tests and final just check passed; CodeRabbit completed two passes and every major finding was fixed. Parked only the session-throughput panel because a fan-out session spans multiple thread-derived traces and neither current Tempo nor the traces-disabled deployment provides an authoritative cross-trace distinct-turn aggregate.

Delivered requested-tier span and bounded metric attribution, distinct account and per-model quota windows (300 and 10080 minutes live on m7kni), exact-set regressions, dashboard legend and probe-filter validation, and corrected operator docs. Verified with focused race tests, just check, and two CodeRabbit passes with every major finding fixed. The session-throughput panel criterion was dropped on 2026-09-05 because no authoritative cross-trace session aggregate exists.
<!-- SECTION:FINAL_SUMMARY:END -->
