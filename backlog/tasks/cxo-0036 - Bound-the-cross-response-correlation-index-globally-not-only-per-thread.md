---
id: CXO-0036
title: 'Bound the cross-response correlation index globally, not only per thread'
status: Done
assignee:
  - '@codex'
created_date: '2026-09-06 09:23'
updated_date: '2026-09-06 10:50'
labels: []
dependencies: []
priority: medium
type: bug
ordinal: 35000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
CXO-0028 froze the correlation index at 512 entries per thread and 24 hours of archive-clock age, but the number of threads is unbounded: every thread that ever records a tool call keeps a map entry until its entries age out, and the whole index is persisted in every checkpoint write. A burst of short-lived threads grows the checkpoint and the resident set without limit until 24 hours pass. CodeRabbit raised this in the wave 2 review and it was deferred because it changes the frozen D13 contract. The index lives in internal/turn/correlate.go and is persisted by internal/turn/state.go at stateVersion 5.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 The index holds at most a fixed number of threads (frozen in the wave goal), evicting the thread with the oldest newest-entry first, and drops empty thread entries on expiry
- [x] #2 A test proves resident entries and checkpoint size stay bounded under 10,000 distinct threads each recording one call
- [x] #3 Checkpoint compatibility: stateVersion 5 files load unchanged; if the format changes, the version is bumped and older versions load with an empty index
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
Wave3 final source 3bc28996c78c3533ae30f370f01a247aa6dea3ac. One git push was rejected (fetch first); zero successful pushes. Remote advanced by Renovate at10:16:57Z. No second attempt under the one-push contract. Implemented cap4096 with oldest-newest/lexical eviction, active expiry, archive-minute inactive sweeps and immediate capacity enforcement. Synthetic10000-thread checkpoint bound4MiB and v6/v5-empty-index compatibility tests pass. Final just check exit0; CodeRabbit2 complete0findings. Source acceptance complete; deployment remains deferred by rejected push.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Source 3bc28996c78c3533ae30f370f01a247aa6dea3ac; no pushed SHA. Implemented cap4096 with oldest-newest/lexical eviction, active expiry, archive-minute inactive sweeps and immediate capacity enforcement. Synthetic10000-thread checkpoint bound4MiB and v6/v5-empty-index compatibility tests pass. Final just check exit0; CodeRabbit2 complete0findings. Source acceptance complete; deployment remains deferred by rejected push.
<!-- SECTION:FINAL_SUMMARY:END -->
