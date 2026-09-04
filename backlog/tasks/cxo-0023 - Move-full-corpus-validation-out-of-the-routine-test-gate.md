---
id: CXO-0023
title: Move full-corpus validation out of the routine test gate
status: To Do
assignee: []
created_date: '2026-09-04 19:20'
labels: []
dependencies: []
priority: medium
type: enhancement
ordinal: 23000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
Wave 1 showed that the current corpus-backed race suite can spend more than 104 minutes in a single package while repeatedly processing a 7.1 GB archive corpus. That cost is disproportionate for routine homelab development and makes the pre-push feedback loop impractical, while the real-corpus evidence remains valuable as an explicit confidence check.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 just check completes without reading corpus/processed and remains the documented pre-commit and CI build-test gate
- [ ] #2 A clearly named opt-in corpus recipe runs the full corpus-backed checks and reports the corpus coverage and result separately from routine tests
- [ ] #3 Corpus-derived reusable artifacts contain no conversation content and are protected by an automated privacy regression test
- [ ] #4 Before and after wall-clock measurements are recorded, with the routine local gate targeted at no more than 10 minutes on this Mac
- [ ] #5 AGENTS.md documents when to run the routine gate and when to run the full-corpus gate
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
<!-- DOD:END -->
