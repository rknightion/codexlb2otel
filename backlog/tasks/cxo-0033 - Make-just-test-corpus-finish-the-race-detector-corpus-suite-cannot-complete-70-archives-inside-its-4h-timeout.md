---
id: CXO-0033
title: >-
  Make just test-corpus finish: the race-detector corpus suite cannot complete
  70 archives inside its 4h timeout
status: To Do
assignee: []
created_date: '2026-09-06 00:05'
labels: []
dependencies: []
priority: medium
type: bug
ordinal: 32000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
just test-corpus runs the full-corpus suite with -race and a 4h timeout. On the 70-file (9.4 GB compressed, 25 GB decompressed) corpus the attr package alone had opened 23 of 70 archives after 98 minutes in its first corpus test, so the suite cannot finish before the timeout; wave 1 and wave 2 both cancelled it, and the 2026-09-06 release run stopped it at 1h38m. The same suite without -race passes in about 24 minutes (attr 413s, loki 454s, turn 533s, everything else seconds), which is what the v0.5.0 release was gated on. The race detector adds nothing here that the corpus-free race gate (just test) does not already cover; what the corpus gate proves is caps, sizing and drift against real captures. Decide the shape: drop -race from test-corpus, or keep a race pass only for the small packages and run the three corpus-heavy packages without it, or sample the corpus for the race pass.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 just test-corpus completes green on the full 70-file corpus on the lab Mac inside its own timeout
- [ ] #2 AGENTS.md and the justfile doc comment describe what the corpus gate proves and that concurrency coverage comes from just test
- [ ] #3 The wave operating model doc (doc-0002) test-corpus once-per-wave rule still holds and names the expected wall time
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
<!-- DOD:END -->
