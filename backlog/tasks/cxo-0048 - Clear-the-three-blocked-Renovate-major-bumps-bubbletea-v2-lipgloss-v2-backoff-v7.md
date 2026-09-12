---
id: CXO-0048
title: >-
  Clear the three blocked Renovate major bumps: bubbletea v2, lipgloss v2,
  backoff v7
status: Parked
assignee:
  - '@codex'
created_date: '2026-09-12 16:54'
updated_date: '2026-09-12 18:11'
labels:
  - deps
dependencies: []
priority: medium
type: chore
ordinal: 47000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
Three Renovate major-version PRs have been red since they opened and cannot self-merge, so the module graph is pinned behind them indefinitely:

  PR 54  github.com/charmbracelet/bubbletea    v1 -> v2
  PR 55  github.com/charmbracelet/lipgloss     v1 -> v2
  PR 56  github.com/cenkalti/backoff/v5        v5 -> v7

Each fails the same set of jobs (build/lint/test/probe, golangci-lint, govulncheck, docker image builds, ci-success), which is the signature of a compile break rather than a flaky gate, and all three are breaking API rewrites rather than drop-in bumps.

bubbletea and lipgloss are TUI dependencies, so the blast radius is the interactive investigation tools, not the exporter's signal path. backoff sits in the retry path and is the one with real behavioural exposure: a v5-to-v7 rewrite changes retry semantics, and this codebase already has a settled position that a config fault on one sink must not stop every signal.

bubbletea and lipgloss should be taken together - they share the charmbracelet v2 API generation and splitting them leaves the tools uncompilable in between. Take backoff separately so its retry-behaviour change is reviewable on its own diff.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 bubbletea v2 and lipgloss v2 land together in one commit; every TUI tool compiles and just check passes
- [x] #2 backoff v7 lands as its own commit, with the retry-semantics change between v5 and v7 stated explicitly and a test covering the sink retry path that this codebase depends on
- [ ] #3 All three Renovate PRs are closed or merged and no new red PR is left behind
- [x] #4 govulncheck and the docker image build both pass on main at the resulting SHA
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [x] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
<!-- DOD:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Migrate bubbletea and lipgloss v2 together and validate all TUI tools. 2. Migrate backoff v7 separately, document the retry-semantics delta, and add focused sink retry coverage. 3. Verify main CI jobs and reconcile the superseded Renovate pull requests without touching release PR 79.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Wave 5 implementation complete: Charm v2 landed together in 3fdcfff using the canonical charm.land module paths; backoff v7 and explicit retry-semantics tests landed separately in 5848d14; all CLIs build, just check passes, and exact-head CI 34709771715 includes successful govulncheck and docker image jobs. AC3 remains open because superseded Renovate PRs 54, 55 and 56 are still OPEN; external-write authority did not grant manual PR mutation.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Parked only at external bot reconciliation. Resume when live GitHub state shows PRs 54, 55 and 56 closed or merged with no replacement red PR; then check AC3 and mark Done without changing code. PR 79 remains untouched.
<!-- SECTION:FINAL_SUMMARY:END -->
