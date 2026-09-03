---
id: CXO-0003
title: >-
  Probe traffic cannot be excluded from token or latency metrics: codexlb.family
  is stripped from all of them
status: To Do
assignee: []
created_date: '2026-08-14 16:59'
updated_date: '2026-09-03 14:11'
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
- [ ] #1 Decide, with the cardinality arithmetic written down, which instruments gain codexlb.family
- [ ] #2 At minimum every duration histogram and every token counter carries it, or probe traffic still cannot be excluded
- [ ] #3 Decide the same for reasoning.level and thread_source on the token counters
- [ ] #4 TestEveryEmittedAttributeKeyIsOnContract still passes; no new attribute key is invented - all three already exist in the registry
- [ ] #5 Corpus-backed cardinality check, measured not assumed
- [ ] #6 Dashboard PromQL panels exclude probe family by default, and the LogQL workarounds are reverted where PromQL now suffices
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 make check passes: gofmt -l . reports nothing, go vet ./... clean, go test ./... green
- [ ] #2 go build ./... succeeds
<!-- DOD:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Pre-flight 2026-09-03. Live series baseline on Mimir: ~7,100 active series for job=codexlb2otel; gen_ai_client_token_usage_bucket 1,140, the per-stage duration histograms 208-384 each, codexlb_tokens_total 56, codexlb_responses_total 66. Observed values today: family websocket|http|unknown (+probe in earlier captures), reasoning level low|medium|high|xhigh|max (5, 'max' is 11% of DB rows), thread_source user|subagent. Frozen decision for the wave (reversible, Rob to confirm): codexlb.family goes on every token counter and every duration histogram; gen_ai.request.reasoning.level and codexlb.thread_source go on the token counters and the new cost counter only, not on histograms. Budget: total active series must stay under 3x today's figure, measured on the synced corpus before landing (AC #5), with the arithmetic in these notes.
<!-- SECTION:NOTES:END -->
