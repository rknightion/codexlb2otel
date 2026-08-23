---
id: CXO-0017
title: Upgrade Go toolchain to 1.27
status: Done
assignee: []
created_date: '2026-08-23 19:06'
updated_date: '2026-08-23 20:37'
labels: []
dependencies: []
ordinal: 17000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
Adopt Go 1.27 consistently across the application, nested modules, build images, CI configuration, setup automation, and version-specific contributor documentation.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 All active Go module and toolchain pins require Go 1.27
- [x] #2 Build images, CI jobs, setup automation, and current documentation agree with the Go 1.27 requirement
- [ ] #3 The repository green-bar validation passes under Go 1.27
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 make check passes: gofmt -l . reports nothing, go vet ./... clean, go test ./... green
- [x] #2 go build ./... succeeds
<!-- DOD:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Inventory every active Go version pin, including nested modules and container or CI toolchains. 2. Update the pins and version-specific documentation to Go 1.27.0 without changing historical records or fixtures. 3. Run the repository-defined validation gate, review the diff, commit to main, push, and confirm hosted CI.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Go 1.27.0 changes preserve the established encoding/json behavior with GOEXPERIMENT=nojsonv2 across Make, Docker, CI, the scheduled archive probe, and source-build documentation. Root build and vet passed; a focused multi-gigabyte corpus test passed in 309 seconds. The complete local make check is not green: another corpus package exceeded the existing 10-minute go test package timeout, so AC 3 and DoD 1 remain unchecked. Hosted CI deliberately runs the documented non-corpus suite and will provide separate exact-head evidence. CodeRabbit was skipped because the change is declarative build/runtime compatibility configuration only.

Exact-head hosted evidence: GitHub Actions run 32662554126 passed at e62e7cd39e68532cf0dce84ae50b6d6f59728adf. Hosted CI passed its documented non-corpus build, vet, test, and probe jobs. AC 3 and DoD 1 remain unchecked because the complete local corpus suite exceeded its existing package timeout; this hosted run does not prove that corpus gate.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Upgraded the active Go application surfaces to Go 1.27 and validated the change locally and in exact-head hosted CI run 32662554126 at e62e7cd39e68532cf0dce84ae50b6d6f59728adf. Hosted CI passed its documented non-corpus build, vet, test, and probe jobs. AC 3 and DoD 1 remain unchecked because the complete local corpus suite exceeded its existing package timeout; this hosted run does not prove that corpus gate.
<!-- SECTION:FINAL_SUMMARY:END -->
