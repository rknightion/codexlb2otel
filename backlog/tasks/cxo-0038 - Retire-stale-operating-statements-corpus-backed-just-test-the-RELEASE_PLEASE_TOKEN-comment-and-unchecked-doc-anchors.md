---
id: CXO-0038
title: >-
  Retire stale operating statements: corpus-backed just test, the
  RELEASE_PLEASE_TOKEN comment, and unchecked doc anchors
status: In Progress
assignee:
  - '@codex'
created_date: '2026-09-06 09:23'
updated_date: '2026-09-06 09:33'
labels: []
dependencies: []
priority: low
type: chore
ordinal: 37000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
Three statements in the repo are no longer true and will mislead the next agent. (1) The Wave operating model doc (doc-0002, Task interface section) says just test is the full local race suite that uses the corpus if synced; since CXO-0023 just test and just check are corpus-free and just test-corpus is the opt-in gate. (2) .github/workflows/publish.yml's header comment says release-please opens its PR under RELEASE_PLEASE_TOKEN, a PAT; that secret is revoked and release-please.yml mints a per-run GitHub App token from the OpenBao broker via id-token. (3) The docs link check the gate runs validates relative file links but not anchors, so a renamed heading breaks a link silently. Edit the doc through the backlog CLI (backlog doc), never by hand.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 doc-0002's Task interface section matches AGENTS.md on which recipes read the corpus, updated via the backlog CLI
- [ ] #2 publish.yml's comment describes the OpenBao-minted App token and names no PAT
- [ ] #3 The link check fails on a relative link whose anchor does not exist in the target file, with a test or a deliberate broken-link dry run quoted
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
<!-- DOD:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
Wave 3: execute the frozen lane ownership and decisions in codex/goal-2026-09-06-wave3.md. Root commits wave 0 before dispatch; L2/L7 depend on L1 and the recorded D15 verdict. Integrate, run the rewritten corpus gate once after wiring, review, push once, verify Watchtower and m7kni, then reconcile acceptance and resume boundaries.
<!-- SECTION:PLAN:END -->
