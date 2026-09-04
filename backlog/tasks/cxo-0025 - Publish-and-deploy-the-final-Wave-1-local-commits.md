---
id: CXO-0025
title: Publish and deploy the final Wave 1 local commits
status: Done
assignee:
  - '@codex'
created_date: '2026-09-04 19:37'
updated_date: '2026-09-04 22:30'
labels: []
dependencies: []
priority: high
type: task
ordinal: 25000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
Wave 1 exhausted its sole authorised push at 20dafa64dc3d7c73c05dcef166c196826ac9d15a. Local main continued to 334a4db0f453c2da94d94e9f108aa429e9bc2a38 with dashboard, September wire adoption, documentation, decoder, review and cardinality fixes. Camden is healthy on the earlier image, so those final local changes have no exact-SHA CI, publish or runtime proof. This task preserves the integration boundary without treating the partial deployment as final evidence.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 origin/main contains the final local Wave 1 commit or a documented successor without rewriting published history
- [x] #2 CI and publish complete successfully at the exact pushed SHA, with run IDs recorded
- [x] #3 Camden runs the image digest built from that exact SHA, reports healthy, and has zero unexpected restarts
- [x] #4 Live metrics and the codexlb2otel-full dashboard are rechecked after deployment, with absent signals reported as unproven rather than passed
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [x] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
<!-- DOD:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Verify repository ownership, exact local head, clean worktree, and the already-green routine gate. 2. Push main without rewriting history and record the exact remote SHA. 3. Follow the exact-SHA CI and publish workflows to terminal conclusions, retaining failed or cancelled attempts separately. 4. Deploy only the digest built from that SHA using the repository runbook, then prove Camden health and restart count. 5. Recheck the live metric and dashboard surfaces, marking absent data unproven, and finalize the remaining parked tasks only where this evidence actually satisfies their prerequisites.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Pushed exact SHA 2f6c0cf972ece15c3d653c43efcb4cd618a74bc7. CI run 33923698394 failed after every Go test and four other jobs passed because dashboard-check invoked rg, which is absent on the GitHub-hosted runner. This is failed diagnostic history, not proof. Replaced the checker dependency with grep -E and revalidated locally with just check in 7.4s; publish and deployment evidence must use the successor SHA.

Successor SHA 13a12a51e0492dea5e4877205aca8b5b97b667a7 CI run 33924036739 also failed after all Go tests passed: the first portability edit corrected dashboard-sidecar, but dashboard-check had a second identical rg invocation. Local PATH masked it. Corrected the exact remaining line, verified no rg invocation remains in the justfile, and reran dashboard-check successfully. This run remains failed diagnostic history.

Dashboard generation 13 update succeeded, but exact spec diff found Grafana canonicalized away 14 empty custom objects. Treated that as a mismatch rather than a pass. Removed the no-op defaults at the generator source, regenerated the artifact, and reran just check successfully in 7.8s. CodeRabbit was not rerun because the integrated two-pass ceiling had already been reached; this is a one-line generator canonicalization with dashboard generation and diff validation.

Final successor deployment evidence: release merge SHA 8df1abbc5941b98d8803306d83e7212348e0a51a is on origin/main. Exact-head CI 33925438082, main publish 33925438635, and release-please plus tag-image publication 33925438925 all completed successfully. Camden was recreated from ghcr.io/rknightion/codexlb2otel:main at multi-arch digest sha256:e62782f6062a20febb2a464a8802d4a03e5454de5ec932ed845ad378906ac1c7, OCI revision 8df1abbc5941b98d8803306d83e7212348e0a51a, StartedAt 2026-09-04T22:28:34.843165438Z, healthy with failing streak 0 and restart count 0; the in-container healthcheck exited 0. Dashboard generation 13 is exact-spec identical to the committed resource; an identical resourceVersion-safe update was accepted as a no-op and did not manufacture a generation bump. Immediate m7kni read-back showed the 300-minute and 10080-minute rate-limit window labels present. Cost, Postgres db_hit, secondary-window, and requested-tier series were absent; enrichment emitted disabled only. Drift reported breaking=1, new=37, info=99. Those absences remain unproven or parked, not passed. Routine just check passed locally in 7.5 seconds; just test-corpus was deliberately not run under the revised gate policy.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Published and deployed the complete Wave 1 successor through release merge 8df1abbc5941b98d8803306d83e7212348e0a51a. Exact-head CI 33925438082 and publish 33925438635 succeeded; Camden runs digest sha256:e62782f6062a20febb2a464a8802d4a03e5454de5ec932ed845ad378906ac1c7 with matching OCI revision, healthy state, zero restarts, and a passing in-container healthcheck. Dashboard generation 13 is exact-spec identical. Live rate-limit window labels are present, while Postgres, cost, secondary-window, requested-tier, and zero-breaking evidence remain honestly absent or parked.
<!-- SECTION:FINAL_SUMMARY:END -->
