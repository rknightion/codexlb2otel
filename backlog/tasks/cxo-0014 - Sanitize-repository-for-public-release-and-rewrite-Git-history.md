---
id: CXO-0014
title: Sanitize repository for public release and rewrite Git history
status: Done
assignee:
  - '@codex'
created_date: '2026-08-23 12:03'
updated_date: '2026-08-23 12:14'
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
- [x] #1 The tracked issue archive is removed and no issue-export content remains in the current tree
- [x] #2 Tracked Grafana tenant and instance identifiers are synthetic while configuration and tests remain internally consistent
- [x] #3 GitHub Actions retain the functional OpenBao and release configuration without environment-specific long-form commentary
- [x] #4 No committed absolute local developer path remains outside the explicitly accepted current CXO-0012 tracker reference
- [x] #5 Secret and privacy sweeps plus the repository gate pass on the sanitized tree
- [x] #6 All remote repository refs are rewritten and force-pushed without the removed archive or sanitized identifiers
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [x] #1 make check passes: gofmt -l . reports nothing, go vet ./... clean, go test ./... green
- [x] #2 go build ./... succeeds
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

Validation: YAML parsing and Python compilation passed; gitleaks found no secrets; make check passed including the 199.596s corpus-backed Loki test; go build ./... passed; staged diff check passed. CodeRabbit was skipped because the change is deletion, documentation, declarative workflow/configuration, and identifier-only wiring with no branching or application logic. A verified recovery bundle is at <local recovery bundle outside repository>. All three branches and all three tags were force-pushed and independently read back; historical object sweeps find neither removed archive path nor real Grafana identifier, with only the accepted CXO-0012 local path remaining.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Removed the issue archive and stale tracker references, randomized Grafana tenant examples, reduced release automation to functional configuration, and generalized committed local paths. Rewrote and force-pushed every branch and tag after verifying the sanitized tree, full gate/build, all-history content sweep, secret scan, and remote ref read-back.
<!-- SECTION:FINAL_SUMMARY:END -->
