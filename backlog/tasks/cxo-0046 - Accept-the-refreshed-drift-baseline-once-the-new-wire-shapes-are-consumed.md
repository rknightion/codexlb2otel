---
id: CXO-0046
title: Accept the refreshed drift baseline once the new wire shapes are consumed
status: Done
assignee: []
created_date: '2026-09-12 10:09'
updated_date: '2026-09-12 19:05'
labels:
  - wire
dependencies:
  - CXO-0041
  - CXO-0042
priority: low
type: chore
ordinal: 45000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
The embedded baseline in internal/profile/baseline/corpus.sig.json is stale against the live capture. A full scan on 2026-09-12 over 74 files, 10,244,207 lines, 8,329,873 gzip members and 25.8GB decompressed reported 72 findings: 0 breaking, 72 new, 0 info. 21 event types seen, 0 unnamed by internal/frame, 0 undecodable lines.

Every finding is additive and every one of them is now tracked. They are the error-event quota block, the routing-hint header, the passthrough metadata object, the models etag, the compaction turn-metadata block, the gpt-reserve additional rate-limit family, and first observations of rate_limits.limit_reached true, rate_limits.allowed false, plan_type prolite, error.type usage_limit_reached, response.output_item item.type compaction and metadata.use_cases bio.

This is deliberately the LAST step of the wave, not the first. Accepting the baseline before the extraction lands would silence the only signal that the shapes are unconsumed, and the corpus suite would then validate against a baseline nothing reads. Accepting it after means the daemons codexlb_archive_drift_findings severity=new gauge returns to zero and can signal the NEXT genuine change - which it currently cannot, because it sits permanently non-zero.

The acceptance is a deliberate human act by repository rule: just baseline is confirm-gated, overwrites a committed file, and must always run from a FULL scan, never a sampled one. It must not be automated into an unattended run and --yes must never be passed. The local corpus was synced from the host on 2026-09-12 and holds 74 files at 9.7GB; a fresh sync is only needed if this slips past the hosts one-day retention.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 just probe over the full local corpus reports 0 breaking findings immediately before acceptance
- [x] #2 just baseline is run interactively by the operator from that full scan, never with --yes and never from a sampled scan
- [x] #3 just check passes after acceptance, including probe-ci
- [x] #4 The deployed services drift gauge returns to zero for severity new, verified live rather than assumed
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [x] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
<!-- DOD:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Full scan 2026-09-12 over 74 files / 10,244,207 lines / 8,329,873 members / 25.8GB decompressed: 0 breaking, 72 new, 0 info, 0 undecodable, 21 event types all named by internal/frame. Baseline accepted interactively from that full scan; no --yes and no JUST_YES=1. just check passes under LC_ALL=C (exit 0), including probe-ci; it fails under en_GB collation because dashboard-sidecar's sort -u is locale-sensitive, which is a pre-existing gate defect unrelated to this task. AC4 remains open: the baseline is embedded in the binary, so the deployed drift gauge cannot fall to zero until camden runs an image built from this commit.

Live verification after redeploying camden to 4d301f0 (container running healthy at that revision): codexlb_archive_drift_findings severity=new reads 0, down from 96; severity=breaking 0; severity=info 97, the by-design absence findings that must never page. Baseline accepted at commit 4d301f0dba19b92785d0ab43dfc005b2c59a721b.
<!-- SECTION:NOTES:END -->
