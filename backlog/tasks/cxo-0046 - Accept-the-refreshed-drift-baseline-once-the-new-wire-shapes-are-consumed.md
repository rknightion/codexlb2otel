---
id: CXO-0046
title: Accept the refreshed drift baseline once the new wire shapes are consumed
status: To Do
assignee: []
created_date: '2026-09-12 10:09'
updated_date: '2026-09-12 10:09'
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
- [ ] #1 just probe over the full local corpus reports 0 breaking findings immediately before acceptance
- [ ] #2 just baseline is run interactively by the operator from that full scan, never with --yes and never from a sampled scan
- [ ] #3 just check passes after acceptance, including probe-ci
- [ ] #4 The deployed services drift gauge returns to zero for severity new, verified live rather than assumed
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
<!-- DOD:END -->
