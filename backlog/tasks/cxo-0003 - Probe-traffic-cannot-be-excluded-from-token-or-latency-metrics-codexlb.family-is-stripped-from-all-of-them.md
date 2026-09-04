---
id: CXO-0003
title: >-
  Probe traffic cannot be excluded from token or latency metrics: codexlb.family
  is stripped from all of them
status: Done
assignee:
  - '@codex'
created_date: '2026-08-14 16:59'
updated_date: '2026-09-04 20:49'
labels:
  - from-gh-issue
dependencies: []
priority: high
type: bug
ordinal: 3000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
Migrated from the former GitHub issue tracker. Refs the old #14, #3, #23,
#7 - all closed, indexed in the closed-issues document.

Found while building the dashboards, and it blocks one of that work's acceptance criteria.

codex-lb's health probes **deliberately exercise the real models**, and they "cannot be separated by
model - only by `family`". So `codexlb.family` is the only way to keep synthetic traffic out of a
cost or latency panel.

`internal/sink/otlpmetric/record.go` attaches `attr.Family` to exactly two instruments:
`codexlb.transport_events` and `codexlb.baseline_resets`. Every other instrument narrows the base
attribute set with `attr.Only(...)` and drops it - all token counters and the token-usage histogram,
every duration histogram, the timing family, engine calls, tool calls, web search, image-gen tokens.
`codexlb.responses` and `codexlb.turns` do carry it, because they emit the unnarrowed base set.

**Every cost and latency number this service publishes to Prometheus silently includes probe
traffic, and no query can remove it.** The dashboards worked around it in LogQL against the turn
record's JSON body, since `family` *is* structured metadata on every Loki line - which leaves the
metrics pipeline, the one built for aggregation, as the one that cannot be trusted.

**The decision this needs:** adding `codexlb.family` multiplies series count by the observed family
values - measured at **3** on the current corpus (`websocket`, `http`, `unknown`, with `probe` seen
in earlier captures), capped at 8 in the registry, so a real multiplier of 3-4. That is a genuine
cardinality cost and a contract decision.

Worth reviewing at the same time, same `attr.Only` cause: no token metric carries
`gen_ai.request.reasoning.level` or `codexlb.thread_source`, so "token shape by effort and by
user-vs-subagent" is not answerable in PromQL either. Both are bounded and small (4 and 3 observed).
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 Decide, with the cardinality arithmetic written down, which instruments gain codexlb.family
- [x] #2 At minimum every duration histogram and every token counter carries it, or probe traffic still cannot be excluded
- [x] #3 Decide the same for reasoning.level and thread_source on the token counters
- [x] #4 TestEveryEmittedAttributeKeyIsOnContract still passes; no new attribute key is invented - all three already exist in the registry
- [x] #5 Corpus-backed cardinality check, measured not assumed
- [x] #6 Dashboard PromQL panels exclude probe family by default, and the LogQL workarounds are reverted where PromQL now suffices
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [x] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
<!-- DOD:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
Wave 1: add the frozen family, effort and thread-source dimensions plus cost emission, measure corpus cardinality, then update dashboards after wiring.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Pre-flight 2026-09-03. Live series baseline on Mimir: ~7,100 active series for job=codexlb2otel; gen_ai_client_token_usage_bucket 1,140, the per-stage duration histograms 208-384 each, codexlb_tokens_total 56, codexlb_responses_total 66. Observed values today: family websocket|http|unknown (+probe in earlier captures), reasoning level low|medium|high|xhigh|max (5, 'max' is 11% of DB rows), thread_source user|subagent. Frozen decision for the wave (reversible, Rob to confirm): codexlb.family goes on every token counter and every duration histogram; gen_ai.request.reasoning.level and codexlb.thread_source go on the token counters and the new cost counter only, not on histograms. Budget: total active series must stay under 3x today's figure, measured on the synced corpus before landing (AC #5), with the arithmetic in these notes.

L2 complete at commit 3897b83. Cardinality arithmetic: budget is 3 times 7,100 = 21,300 active series; clbstat measured 4,014 distinct metric attribute combinations across 58 processed archives. Added codexlb.family to token counters and duration histograms. Added gen_ai.request.reasoning.level and codexlb.thread_source only to codexlb.tokens, gen_ai.client.token.usage, and codexlb.cost_usd. Added codexlb.api_key_name to responses, turns, token instruments, and cost. Added codexlb.cost_usd Float64Counter unit USD, recorded only when Turn.CostUSD is set, including explicit zero. Each selector documents what it drops. clbstat now measures the sink selectors directly without printing attribute values and tolerates newer object-shaped payload.text records as unparseable input. Validation passed: go test -race -count=1 ./internal/sink/otlpmetric, go vet on cmd/clbstat and the metric package, gofmt clean, git diff check, just lint, just build, and just test-short. Corpus result: files=58, members=6139430, lines=7500747, unparseable=167919, responses=36281, combinations=4014. Cost cardinality is unit-tested because the archive corpus has no Postgres-enriched CostUSD. Dashboard AC remains for L7. Route deviation: requested gpt-5.6-terra high was unavailable; selected gpt-5.5 high.

Final correction at 334a4db measured projected Prometheus wire series, including histogram bucket, sum, and count multipliers and object-shaped payload records. The first corrected projection was 29,301, above the 21,300 budget. Narrowing high-fanout low-level histograms and removing unbounded instructions-hash versions from Agent Observability histograms reduced the final projection to 11,201 while retaining family on every required token and duration instrument and stable agent names. Live active series on the deployed partial SHA measured 13,787, also below budget. Final just check passed. The full corpus just test was cancelled as disproportionate and is not a pass.

Final 30 minute instant query counted 8,614 active series for job=codexlb2otel. An earlier post-restart sample counted 13,787. Both are below 21,300; neither covers the unpushed final source, whose corpus projection is 11,201.

A later full-branch CodeRabbit review found that 25 response and 5 turn queries still omitted the dashboard family variable even though the metrics carried it. Added the family selector to every response and turn PromQL query and a generator regression that fails on any future omission. The pre-fix dashboard-check failed with exact counts 25 and 5; the corrected dashboard-check passed.

The next completed review found a numerator/denominator mismatch in the Cost per turn panel. A fail-first generator regression proved the query did not restrict either side to user turns. Both the enriched cost numerator and completed-turn denominator now share a selector with request_kind=turn, excluding prewarm, compaction, and memory costs. Dashboard generation and dashboard-check pass.

Reconciled the remaining legacy dashboards after review. Converted both token-by-effort and token-by-thread-source panels in dashboards/02-tokens-cost-shape.json from LogQL workarounds to codexlb_tokens_total PromQL, corrected stale contract prose, made all six token queries exclude probe traffic, and added probe exclusion to both turn queries in dashboards/04-agent-tree.json. A new dashboard-check regression scans every dashboard and fails any codexlb_tokens_total, codexlb_responses_total, or codexlb_turns_total query lacking a family selector; it failed first on the two agent-tree queries and now passes.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Added family to every required token and duration instrument, bounded effort and thread-source dimensions to token and cost instruments, and emitted replay-safe USD cost. Full-corpus projection is 11,201 and live partial-deploy active series are 13,787 against the 21,300 budget; final just check and dashboard validation passed.
<!-- SECTION:FINAL_SUMMARY:END -->
