---
id: CXO-0023
title: Move full-corpus validation out of the routine test gate
status: Done
assignee:
  - '@codex'
created_date: '2026-09-04 19:20'
updated_date: '2026-09-04 20:54'
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
- [x] #1 just check completes without reading corpus/processed and remains the documented pre-commit and CI build-test gate
- [x] #2 A clearly named opt-in corpus recipe runs the full corpus-backed checks and reports the corpus coverage and result separately from routine tests
- [x] #3 Corpus-derived reusable artifacts contain no conversation content and are protected by an automated privacy regression test
- [x] #4 Before and after wall-clock measurements are recorded, with the routine local gate targeted at no more than 10 minutes on this Mac
- [x] #5 AGENTS.md documents when to run the routine gate and when to run the full-corpus gate
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [x] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
<!-- DOD:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Make the conventional just test and the CI-facing test-short paths explicitly corpus-free, and make probe-ci exercise the intentional no-corpus contract from a fresh empty directory rather than scanning the local archive tree.
2. Add a clearly named test-corpus opt-in recipe that points at the selected corpus, runs the corpus-backed race suite, and performs the full drift scan so coverage is reported separately.
3. Update AGENTS.md and README.md with the routine versus opt-in policy and keep the existing content-free signature and tracked-archive privacy guards in the routine suite.
4. Measure just check wall time, verify it does not name or traverse corpus/processed, run the privacy regressions and task-surface validation, obtain a completed CodeRabbit review, then finalize from objective evidence.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Implemented the gate split. Routine `just test`, `just test-short`, and `just check` now force an absent corpus; `probe-ci` builds and runs against a fresh empty temporary directory instead of repository `corpus/`. Added explicit `just test-corpus`, which first emits exhaustive drift coverage for the selected corpus and then runs the serial corpus-backed race suite. Updated AGENTS.md and README.md.
Verification so far: the four privacy/baseline tests passed with the corrected Just argument form; `just probe-ci` accepted only exit 3 from its empty directory; `just check` passed in 38.28 seconds on this Mac, versus the Wave 1 full-corpus evidence of more than 104 minutes in one package and a final cancellation after 2 hours 3 minutes.

Bounded opt-in validation exposed and fixed a relative-path bug: `go test` evaluates CLB_CORPUS from each package directory, so the recipe now resolves the selected directory to an absolute path. The first bounded attempt failed in internal/sink/otlpmetric and is not counted as a pass. The corrected run passed: exhaustive scan covered 58 files, 7.1 GB compressed, 18.9 GB decompressed, 7,500,747 lines, and 6,139,430 members with zero undecodable records and no baseline drift; the filtered real-corpus series-count test then passed.

Final verification: `just check` passed after all review repairs in 7.06 seconds and the dashboard validator covered every generated and legacy traffic query. CodeRabbit reached terminal `review_completed`; every valid critical or major finding was fixed, the final pass had no valid major finding, and its single minor percentile-symmetry finding was fixed before the final gate. The full multi-hour corpus suite was deliberately not rerun; its prior cancellation remains distinct from the passing bounded opt-in proof.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Separated routine development from real-corpus confidence checks. `just test` and `just check` cannot read local archives, while explicit `just test-corpus` reports exhaustive coverage and runs corpus-backed races. The routine gate fell from a cancelled 2h03m full-corpus attempt to a final 7.06s pass; a bounded opt-in run covered 58 files and passed. Privacy guards, dashboard generation, failure-atomic artifacts, and final review are green.
<!-- SECTION:FINAL_SUMMARY:END -->
