---
id: CXO-0019
title: Restyle the live monitor onto design system v2 (family follower)
status: In Progress
assignee:
  - '@codex'
created_date: '2026-08-31 12:13'
updated_date: '2026-09-03 18:48'
labels:
  - design-system
dependencies: []
ordinal: 19000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
The v2 design is committed at design/console-v2/: the Live monitor v2 canvas, codexlb2otel-implementation-spec.md, implementation-spec.md (THE FAMILY SPEC from tailscale2otel - its section 1 is the shared token block, byte-identical across tailscale2otel, opnsense2otel, graph2otel and codexlb2otel; copy it, never edit it per repo), and internal/live/ui/ holding a draft restyled index.html - treat the draft as reference, not finished code. Read both specs in full before any code change.

Scope: the single static ui/index.html with inline CSS/vanilla JS, served via go:embed, stays exactly that; no framework, no build step, no CDN, no external network request. This repo is the family outlier today (its own light-first palette) and moves onto the shared family token block. Fonts self-hosted per the family spec. Light default honouring prefers-color-scheme with the existing toggle kept and winning. The page's differentiators are the live thread/conversation tree (nesting, SSE live-row arrival affordance, active/complete/failed request states on the semantic word+shape rule) and the empty state before traffic. Machine text (thread ids, model names, token counts, durations, timestamps) in JetBrains Mono. NOTE: this repo's gate is make check (Makefile, not justfile). SEQUENCING: tailscale2otel task TSO-0103 lands first; if it extended the shared token block, inherit the extended version.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 the page renders on the family token block, light and dark, light default
- [ ] #2 the shared token block matches the family spec section 1 byte-for-byte (as landed by the standard-setter)
- [ ] #3 live tree, SSE arrival affordance and request states match the canvas; empty state present
- [ ] #4 no external network requests; AA pairs hold
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
- [ ] #2 make check green
<!-- DOD:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
Wave 1: adopt the landed family token block, self-host fonts, preserve the live tree semantics, and validate light/dark, accessibility and no-network behavior.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Pre-flight 2026-09-03: the description's NOTE that this repo's gate is make check is STALE - CXO-0018 migrated the task surface to just; the gate is 'just check' (DoD #1 is right, DoD #2 is the stale one). TSO-0103 (family standard-setter) is Done as of 2026-09-01, so the sequencing precondition is met; inherit whatever token block it landed. Design package is at design/console-v2/ (implementation-spec.md = family spec, codexlb2otel-implementation-spec.md = this repo, internal/ holds the draft index.html).
<!-- SECTION:NOTES:END -->
