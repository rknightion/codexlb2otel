---
id: CXO-0004
title: >-
  Publish runs are red at the last step: build-provenance attestation needs a
  public repo
status: Parked
assignee: []
created_date: '2026-08-14 16:59'
updated_date: '2026-08-14 17:00'
labels:
  - from-gh-issue
  - blocked-on-human
dependencies: []
references:
  - [purged issue archive]
priority: medium
type: bug
ordinal: 4000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
Migrated from GitHub issue #27 (full body in `[purged issue archive]`).

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
- [ ] #1 Publish runs are green, by whichever route
- [ ] #2 Multi-arch :main and the semver release tags keep working
- [ ] #3 cosign signing and SBOM generation keep working
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 make check passes: gofmt -l . reports nothing, go vet ./... clean, go test ./... green
- [ ] #2 go build ./... succeeds
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
<!-- SECTION:NOTES:END -->
