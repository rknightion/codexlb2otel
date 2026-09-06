---
id: CXO-0037
title: Tool-call span IDs collide when a call ID is reused within a thread
status: In Progress
assignee:
  - '@codex'
created_date: '2026-09-06 09:23'
updated_date: '2026-09-06 09:33'
labels: []
dependencies: []
priority: low
type: bug
ordinal: 36000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
CXO-0028 froze the tool-call span ID as hashSpanID(threadID, callID) so a later tool_result span can link to its origin call. The correlation index already treats a reused call ID as ambiguous, but the trace sink still derives the same span ID for both calls, so two distinct spans in one trace share an ID and the second overwrites the first in Tempo. Traces are permanently disabled on camden (settled decision, CXO-0006), so this is source-level only; it should still be correct because the sink is shipped and tested. CodeRabbit raised it in the wave 2 review and it was deferred because it changes the frozen D14 contract. The derivation is internal/correlation/ids.go HashSpanID and its callers in internal/sink/otlptrace.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 A reused call ID within one thread produces two distinct tool-call span IDs, and the tool_result link resolves to the call the correlation index matched (exact) or carries no link (ambiguous); a synthetic exporter test pins both
- [ ] #2 The derivation stays deterministic for the unique case so existing tests and the documented span-ID scheme in docs/signals.md remain true; the doc states the reuse rule
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
<!-- DOD:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
Wave 3: execute the frozen lane ownership and decisions in codex/goal-2026-09-06-wave3.md. Root commits wave 0 before dispatch; L2/L7 depend on L1 and the recorded D15 verdict. Integrate, run the rewritten corpus gate once after wiring, review, push once, verify Watchtower and m7kni, then reconcile acceptance and resume boundaries.
<!-- SECTION:PLAN:END -->
