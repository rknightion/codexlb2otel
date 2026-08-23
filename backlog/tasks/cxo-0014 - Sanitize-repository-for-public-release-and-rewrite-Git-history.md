---
id: CXO-0014
title: Sanitize repository for public release and rewrite Git history
status: In Progress
assignee:
  - '@codex'
created_date: '2026-08-23 12:03'
updated_date: '2026-08-23 12:05'
labels: []
dependencies: []
priority: high
type: chore
ordinal: 14000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
Remove the privacy-sensitive issue archive, replace real Grafana tenant identifiers with synthetic examples, minimize OpenBao and release workflow commentary to functional requirements, remove committed absolute local paths, then rewrite and force-push all repository history so removed values are no longer reachable.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 The tracked issue archive is removed and no issue-export content remains in the current tree
- [ ] #2 Tracked Grafana tenant and instance identifiers are synthetic while configuration and tests remain internally consistent
- [ ] #3 GitHub Actions retain the functional OpenBao and release configuration without environment-specific long-form commentary
- [ ] #4 No committed absolute local developer path remains outside the explicitly accepted current CXO-0012 tracker reference
- [ ] #5 Secret and privacy sweeps plus the repository gate pass on the sanitized tree
- [ ] #6 All remote repository refs are rewritten and force-pushed without the removed archive or sanitized identifiers
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 make check passes: gofmt -l . reports nothing, go vet ./... clean, go test ./... green
- [ ] #2 go build ./... succeeds
<!-- DOD:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Inventory every current and historical occurrence of the issue archive, real Grafana identifiers, OpenBao workflow commentary, and absolute local paths; freeze the accepted exceptions.

2. Remove the archive, replace identifiers consistently, minimize workflow comments without changing required action inputs, and generalize committed local paths.

3. Run focused searches, YAML/JSON parsing, the full repository gate and build, then CodeRabbit review and final diff review.

4. Create a recovery bundle outside the repository, commit the sanitized tip, rewrite all reachable refs from a fresh mirror, verify forbidden content is absent from every rewritten object, and force-push rewritten refs.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Removed the issue dump and its README; replaced the closed-issue index with a tombstone and removed stale dump references from migrated tasks. Replaced real Grafana identifiers with synthetic examples, minimized release workflow comments, and generalized the committed sigil-sdk path.
<!-- SECTION:NOTES:END -->
