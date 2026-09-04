---
id: CXO-0025
title: Publish and deploy the final Wave 1 local commits
status: To Do
assignee: []
created_date: '2026-09-04 19:37'
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
- [ ] #1 origin/main contains the final local Wave 1 commit or a documented successor without rewriting published history
- [ ] #2 CI and publish complete successfully at the exact pushed SHA, with run IDs recorded
- [ ] #3 Camden runs the image digest built from that exact SHA, reports healthy, and has zero unexpected restarts
- [ ] #4 Live metrics and the codexlb2otel-full dashboard are rechecked after deployment, with absent signals reported as unproven rather than passed
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
<!-- DOD:END -->
