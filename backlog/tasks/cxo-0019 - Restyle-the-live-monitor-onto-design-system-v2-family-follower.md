---
id: CXO-0019
title: Restyle the live monitor onto design system v2 (family follower)
status: Done
assignee:
  - '@codex'
created_date: '2026-08-31 12:13'
updated_date: '2026-09-04 19:41'
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
- [x] #1 the page renders on the family token block, light and dark, light default
- [x] #2 the shared token block matches the family spec section 1 byte-for-byte (as landed by the standard-setter)
- [x] #3 live tree, SSE arrival affordance and request states match the canvas; empty state present
- [x] #4 no external network requests; AA pairs hold
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [x] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
- [ ] #2 make check green
<!-- DOD:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
Wave 1: adopt the landed family token block, self-host fonts, preserve the live tree semantics, and validate light/dark, accessibility and no-network behavior.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Pre-flight 2026-09-03: the description's NOTE that this repo's gate is make check is STALE - CXO-0018 migrated the task surface to just; the gate is 'just check' (DoD #1 is right, DoD #2 is the stale one). TSO-0103 (family standard-setter) is Done as of 2026-09-01, so the sequencing precondition is met; inherit whatever token block it landed. Design package is at design/console-v2/ (implementation-spec.md = family spec, codexlb2otel-implementation-spec.md = this repo, internal/ holds the draft index.html).

L6 UI landed at commit ffa7ea0: restyled internal/live/ui/index.html onto the landed m7kni 2otel family token block and copied the three tailscale2otel font assets with matching hashes. Preserved the one-grid live tree, SSE and REST flow, caret stopPropagation, thread target behavior, and expanded-turn detail, and added DOM-built SVG status shapes, arrived-row markers, structured empty and lost-stream states, content-withheld status, and the isTarget ordering fix. Validation passed: token block byte-identical to tailscale2otel; no external URL, CDN, remote font, or script source matches; all checked contrast pairs AA with minimum 4.51; node syntax check; two headless Chrome fixtures; go test -race -count=1 ./internal/live; and git diff check. Root integration still needs to embed and serve /_static/fonts/ and add font-src self to the CSP before the task can close.

Root integration at 6a54f61 embeds and serves all three local font assets and permits only self-hosted fonts in CSP. The implementation is present in the healthy deployed partial-SHA image. The live view remains disabled by deployed configuration, so no operator-facing live browser session was manufactured. Source validation includes byte-identical family tokens, headless light and dark fixtures, SSE states, no external URLs, minimum contrast 4.51, focused race tests, and final just check green at 334a4db.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Restyled the live monitor to the shared family tokens, self-hosted and served the fonts under a restrictive CSP, preserved SSE and tree behavior, and added accessible states. Headless light and dark fixtures, URL and contrast checks, focused race tests, and final just check passed.
<!-- SECTION:FINAL_SUMMARY:END -->
