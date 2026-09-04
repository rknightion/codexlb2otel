---
id: CXO-0024
title: Classify the live breaking archive drift findings
status: To Do
assignee: []
created_date: '2026-09-04 19:32'
labels: []
dependencies: []
priority: high
type: bug
ordinal: 24000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
The Wave 1 in-process probe first ran on camden at 2026-09-04T19:23Z and immediately exported 2 breaking, 172 new, and 75 informational findings against the embedded full-scan baseline. The metric and dashboard now make the drift visible, but the two breaking findings must be classified before any baseline is accepted because a breaking type change can silently reduce decoded telemetry.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 A full scan of the current live archive enumerates both breaking findings without copying archive content into the repository or tracker
- [ ] #2 Each breaking finding is classified as decoder-impacting or baseline-only, with a regression test for every required decoder change
- [ ] #3 Any accepted baseline is produced only by a full scan and TestSignature_CarriesNoConversationContent passes
- [ ] #4 After deployment, codexlb_archive_drift_findings with severity breaking reports zero on m7kni
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
<!-- DOD:END -->
