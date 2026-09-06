---
id: CXO-0037
title: Tool-call span IDs collide when a call ID is reused within a thread
status: Done
assignee:
  - '@codex'
created_date: '2026-09-06 09:23'
updated_date: '2026-09-06 12:31'
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
- [x] #1 The derivation stays deterministic for the unique case so existing tests and the documented span-ID scheme in docs/signals.md remain true; the doc states the reuse rule
- [x] #2 Within retained correlation history (512 entries per thread, 4,096 threads, 24 h archive age), a reused call ID in one thread produces two distinct tool-call span IDs, and the tool_result link resolves to the call the correlation index matched (exact) or carries no link (ambiguous); a synthetic exporter test pins both. Uniqueness after eviction is explicitly out of scope: the occurrence counter resets with the retained history.
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
Wave3 final source 3bc28996c78c3533ae30f370f01a247aa6dea3ac. One git push was rejected (fetch first); zero successful pushes. Remote advanced by Renovate at10:16:57Z. No second attempt under the one-push contract. Synthetic trace tests prove retained-history occurrence IDs and exact result links, ambiguous/none no links, legacy pin ecfd3b8786c25b11. Source-level, traces disabled. Unqualified reuse guarantee is not proven after per-thread512/global4096/24h eviction resets occurrence numbering. Resume requires an approved bounded identity design or explicitly scoped retention guarantee, followed by source tests and publication. Resume in a newly authorized publication run: fetch and reconcile remote main 72c29953f4d6a03528cbf0c2f319d30900cbaf01 with the preserved local commits, run the integrated gate on the reconciled tree, authorize a new push, verify CI/publish and Watchtower at that SHA, then update and read back the m7kni dashboard and prove the new dimensions live.

2026-09-06: Rob scoped AC to retained correlation history (traces are permanently off; a post-eviction collision would only overwrite a span in a disabled sink). Verified: go test -race ./internal/sink/otlptrace/ ok 1.401s covering TestToolCallSpanIDs_DistinguishReusedCallIDs, TestToolResultLink_UsesMatchedCallOccurrence, TestToolResultLink_NonExactMatchHasNoOriginLink, TestToolCallSpanID_UniqueCaseKeepsLegacyValue; docs/signals.md states the retained-history scope.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Source 3bc28996c78c3533ae30f370f01a247aa6dea3ac; no pushed SHA. Synthetic trace tests prove retained-history occurrence IDs and exact result links, ambiguous/none no links, legacy pin ecfd3b8786c25b11. Source-level, traces disabled. Unqualified reuse guarantee is not proven after per-thread512/global4096/24h eviction resets occurrence numbering. Resume requires an approved bounded identity design or explicitly scoped retention guarantee, followed by source tests and publication. Resume in a newly authorized publication run: fetch and reconcile remote main 72c29953f4d6a03528cbf0c2f319d30900cbaf01 with the preserved local commits, run the integrated gate on the reconciled tree, authorize a new push, verify CI/publish and Watchtower at that SHA, then update and read back the m7kni dashboard and prove the new dimensions live.

Closed at cce4673 with the AC scoped to retained correlation history by Rob's decision; occurrence-based span IDs and exact/ambiguous links pinned by four synthetic tests (source-level, traces disabled).
<!-- SECTION:FINAL_SUMMARY:END -->
