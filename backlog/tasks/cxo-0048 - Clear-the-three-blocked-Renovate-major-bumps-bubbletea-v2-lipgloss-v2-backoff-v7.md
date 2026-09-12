---
id: CXO-0048
title: >-
  Clear the three blocked Renovate major bumps: bubbletea v2, lipgloss v2,
  backoff v7
status: In Progress
assignee:
  - '@codex'
created_date: '2026-09-12 16:54'
updated_date: '2026-09-12 17:30'
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
- [ ] #1 bubbletea v2 and lipgloss v2 land together in one commit; every TUI tool compiles and just check passes
- [ ] #2 backoff v7 lands as its own commit, with the retry-semantics change between v5 and v7 stated explicitly and a test covering the sink retry path that this codebase depends on
- [ ] #3 All three Renovate PRs are closed or merged and no new red PR is left behind
- [ ] #4 govulncheck and the docker image build both pass on main at the resulting SHA
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
<!-- DOD:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Migrate bubbletea and lipgloss v2 together and validate all TUI tools. 2. Migrate backoff v7 separately, document the retry-semantics delta, and add focused sink retry coverage. 3. Verify main CI jobs and reconcile the superseded Renovate pull requests without touching release PR 79.
<!-- SECTION:PLAN:END -->
