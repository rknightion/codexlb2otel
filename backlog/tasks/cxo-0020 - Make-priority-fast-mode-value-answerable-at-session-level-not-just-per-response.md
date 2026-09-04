---
id: CXO-0020
title: >-
  Make priority/fast-mode value answerable at session level, not just per
  response
status: To Do
assignee: []
created_date: '2026-09-04 17:38'
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

```
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

Separately: the rate-limit gauges carry neither tier nor a window label, so "does priority burn quota faster" cannot be answered at all. Account `bd537096-499f-46f4-b1d8-20b1994b9ac5` was at 98% used when this was written.

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
- [ ] #1 codexlb.service_tier_requested is present on the turn span and a TraceQL query returns priority and non-priority sessions separately
- [ ] #2 codexlb_harness_unblocked_seconds and codexlb_responsesapi_excl_client_tools_seconds carry the requested tier, so model-wait versus client-wait can be compared per tier in PromQL
- [ ] #3 codexlb_tokens_total and codexlb_errors_total carry the requested tier
- [ ] #4 Rate-limit gauges distinguish the 5h and weekly windows via a label, and the task records whether per-tier quota attribution is possible from the wire or is a hard negative
- [ ] #5 Fast Mode tab gains a session-level panel showing throughput per wall-clock time by tier, with sample counts and an explicit note that cohorts are not randomised
- [ ] #6 Attribute registry and cardinality guard updated, and semconv divergence notes explain any key that has no convention equivalent
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
<!-- DOD:END -->
