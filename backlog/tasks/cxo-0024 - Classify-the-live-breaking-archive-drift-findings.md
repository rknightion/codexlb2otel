---
id: CXO-0024
title: Classify the live breaking archive drift findings
status: Parked
assignee:
  - '@codex'
created_date: '2026-09-04 19:32'
updated_date: '2026-09-04 21:32'
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
- [x] #1 A full scan of the current live archive enumerates both breaking findings without copying archive content into the repository or tracker
- [x] #2 Each breaking finding is classified as decoder-impacting or baseline-only, with a regression test for every required decoder change
- [ ] #3 Any accepted baseline is produced only by a full scan and TestSignature_CarriesNoConversationContent passes
- [ ] #4 After deployment, codexlb_archive_drift_findings with severity breaking reports zero on m7kni
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [x] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
<!-- DOD:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Inspect Camden's current archive path and deployed probe tooling read-only, preserving only content-free shape evidence. 2. Run a full unsampled scan against the embedded baseline without invoking the confirmation-gated baseline update. 3. Classify each breaking path against current decoder behavior and add fail-first synthetic regressions only where decoding is affected. 4. Run focused race tests and just check, then record exact evidence and either finalize or park only the baseline/deployment proof on the missing authorization.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Full live classification on 2026-09-04 used a 0600 temporary snapshot outside the repository and removed it after analysis. The unsampled scan covered 22 files, 6,125,758,705 compressed bytes, 4,231,850 lines, 3,373,446 gzip members, 15.6 GB decompressed, and zero undecodable records. Both breaking findings were decoder-impacting. First, 439,272 HTTP SSE envelopes contained real protocol events, including 3,868 response.completed events, but ParseEvent treated every envelope as non-JSON; this caused the 2.23% to 10.38% unparsed-payload finding. Second, seven HTTP error envelopes, six 401 and one 429, used payload.error and were silently discarded because Payload decoded only payload.text. Added fail-first synthetic regressions for SSE extraction, error-envelope normalization, and profiler decoding, then implemented a shared ParseEventText path and payload.error normalization. Focused race tests and just check passed. A second full scan of the identical snapshot proved the unparsed-payload breaking finding disappeared; only storage.payload_shape object{error} remains because the newly supported shape is absent from the embedded baseline. No baseline was accepted because this follow-up run has no explicit authorization for the confirmation-gated baseline update. Live zero-breaking deployment therefore remains pending that authorization.

CodeRabbit completed review_completed with two minor findings. Fixed the valid parser edge case by supporting an initial UTF-8 BOM plus CR-only and CRLF SSE line endings, with fail-first regressions; focused races and just check passed afterward. Left the unrelated missing language identifier on CXO-0026's imported Markdown fence because it is cosmetic tracker formatting and does not affect execution or evidence.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Classified both live breaking findings from a full 22-file unsampled Camden snapshot and fixed both decoder blind spots with fail-first synthetic regressions. The shared parser now decodes HTTP SSE event envelopes and structured HTTP payload.error failures; the profiler uses the same event parser. Focused race tests and just check pass, and a second full scan removed the unparsed-payload breaking finding. Parked with one supported-but-unbaselined object{error} shape because this follow-up run lacks explicit authority to update the embedded baseline. Resume by authorizing a full unsampled baseline update, verify TestSignature_CarriesNoConversationContent, publish and deploy, then prove severity=breaking is zero on m7kni.
<!-- SECTION:FINAL_SUMMARY:END -->
