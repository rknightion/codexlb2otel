---
id: CXO-0004
title: >-
  Publish runs are red at the last step: build-provenance attestation needs a
  public repo
status: Done
assignee: []
created_date: '2026-08-14 16:59'
updated_date: '2026-09-04 22:30'
labels:
  - from-gh-issue
  - blocked-on-human
dependencies: []
priority: medium
type: bug
ordinal: 4000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
Migrated from the former GitHub issue tracker.\n
`publish.yml` delegates to the fleet's shared `rknightion/.github` `container-publish.yml`.
Everything works except the final step:

```
Error: Failed to persist attestation: Feature not available for user-owned
private repositories. To enable this feature, please make this repository public.
```

**The image still publishes** - per-arch builds, manifest-list merge, GHCR push and the cosign
signature all succeed; only `actions/attest-build-provenance` fails, after the image is already
usable. So this is a red run, not a broken pipeline.

**It cannot be fixed from this repo.** The attestation step in the shared workflow is
unconditional: its inputs gate `sbom`, `sign`, `trivy`, `dockerhub-login` and `otel-tracing`, but
there is no `attest` input. Every sibling `*2otel` repo is public, which is why none of them hits it.

**Decision 2026-08-07, recorded so it is not re-litigated.** Both real fixes were considered and
deliberately not taken:
- *Make this repo public.* History was checked and is clean - no `.jsonl`/`.gz` on any ref, no `.env`
  or key, `internal/fixture/tracked_test.go` guards it going forward, and `corpus.sig.json` is
  content-free by construction. Not taken because going public is effectively irreversible: the
  history can be cloned and cached even if the setting is flipped back.
- *Add an `attest` input upstream.* Not taken because it is a fleet-wide change to another repo for
  one caller's benefit.

**The cost of leaving it:** a permanently-red publish run trains everyone to ignore publish
failures, which is how the next real one gets missed. Two real bugs were caught in this workflow by
it going red, and neither would have been noticed if red were the normal state. So this must not
stay parked indefinitely.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 Publish runs are green, by whichever route
- [x] #2 Multi-arch :main and the semver release tags keep working
- [x] #3 cosign signing and SBOM generation keep working
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 make check passes: gofmt -l . reports nothing, go vet ./... clean, go test ./... green
- [x] #2 go build ./... succeeds
<!-- DOD:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
**Parked, resume boundary:** un-park when *either* of these becomes true, and not before -

1. this repo goes public (which resolves it outright, and the history was already audited clean for
   exactly that move), or
2. `rknightion/.github`'s `container-publish.yml` gains an `attest` input defaulting to true, at
   which point pin to the new version here and set it false.

Nothing in this repo can move it in the meantime; the attestation step is unconditional upstream.
Do not re-litigate the two rejected fixes - the reasons are in the description.

Pre-flight 2026-09-03: resume boundary (1) has occurred - the repo is PUBLIC (gh repo view: visibility PUBLIC, isFork false). Publish run 33759794468 at 9a481da (2026-09-03) is fully green including the 'merge + sign + sbom' job, which is where attest-build-provenance runs, so AC #1 and AC #3 are proven for the :main path. AC #2's semver half is NOT yet proven: no tag-triggered publish has run since the repo went public (last release v0.3.0 was 2026-08-23; publish.yml triggers on push tags). Stays Parked with a narrower boundary: close when the next release-please tag publishes green. Nothing for an agent to build here. The single red run since (33622188619, 2026-09-02) was a stepsecurity/trivy network timeout, unrelated.

Release boundary completed on 2026-09-04. Release Please PR 45 merged as 8df1abbc5941b98d8803306d83e7212348e0a51a and created v0.4.0. Release workflow 33925438925 completed successfully, including both architecture builds, multi-arch manifest publication, cosign signing, build-provenance attestation, SPDX and CycloneDX SBOM generation, and release-asset attachment. GHCR tag 0.4.0 resolves to sha256:e62782f6062a20febb2a464a8802d4a03e5454de5ec932ed845ad378906ac1c7, identical to main. Exact-head CI 33925438082 also succeeded. The stale make-check DoD remains unchecked because CXO-0018 retired Makefile; the current just check gate passed and includes the build.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Closed after public-repository publication was proven on both moving-main and semver paths. v0.4.0 targets 8df1abbc5941b98d8803306d83e7212348e0a51a; workflow 33925438925 published multi-arch tag 0.4.0 at digest sha256:e62782f6062a20febb2a464a8802d4a03e5454de5ec932ed845ad378906ac1c7, signed it, generated provenance, and attached both SBOM formats. Exact-head CI 33925438082 passed.
<!-- SECTION:FINAL_SUMMARY:END -->
