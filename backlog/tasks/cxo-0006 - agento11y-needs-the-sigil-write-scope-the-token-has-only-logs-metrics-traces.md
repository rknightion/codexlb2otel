---
id: CXO-0006
title: 'agento11y needs the sigil:write scope; the token has only logs/metrics/traces'
status: Parked
assignee: []
created_date: '2026-08-14 16:59'
updated_date: '2026-09-04 22:30'
labels:
  - from-gh-issue
  - blocked-on-human
dependencies: []
priority: medium
type: bug
ordinal: 6000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
Migrated from the former GitHub issue tracker. Imported `Parked`, not
`To Do`: it cannot proceed inside this repo at all - see the resume boundary in the notes.

First production run, 2026-08-07:

```
agento11y: push rejected as unauthorized (this will not fix itself): 401:
{"status":"error","error":"authentication error: invalid scope requested"}
```

The endpoint and region are right - the agento11y host resolves to a sigil ELB and the same
host/credentials work for OTLP traces. It is the scope. `ExportGenerations` needs **`sigil:write`**
on the access policy; the deployed token was copied from the sibling exporters and carries
logs/metrics/traces write only.

**Trap, from gcx's own `agento11y-instrument` skill: in the Cloud UI, `sigil` is not in the default
resource list.** It has to be added via "Add scope", then tick Write. The Cloud resource kept the
old name even though the product is now Agent Observability, so searching for "agento11y" finds
nothing.

**Why this blocked everything, not just sigil:** the sink classifies a 401 as a config fault - never
retried, error propagates, watcher holds the checkpoint. Right call for a credential that will never
fix itself, but it means one missing scope stopped Loki, metrics and traces too. Combined with the
self-observability deadlock the service ingested nothing at all. `agento11y.enabled: false` now
ships (`ec6ecbc`) so the other three signals run, and `TestLoad_ExampleConfigIsDeployable` asserts
it stays false, so flipping it is a decision made with the credential in hand rather than an
accident.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 sigil:write added to the access policy (or a token minted from the stack's grafana-agento11y-app plugin, Connection tab, with sigil/metrics/traces/logs write), and the deployed .env updated
- [ ] #2 agento11y.enabled flipped to true in config.example.yaml, redeployed, and the assertion in TestLoad_ExampleConfigIsDeployable updated
- [ ] #3 Generations actually arrive, confirmed with gcx agento11y generations list - a 2xx is not enough, ExportGenerations can accept the request and still refuse individual generations, which is why accountResults parses the per-generation verdicts
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 make check passes: gofmt -l . reports nothing, go vet ./... clean, go test ./... green
- [ ] #2 go build ./... succeeds
<!-- DOD:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
**Parked, resume boundary:** un-park once the access-policy token carrying `sigil:write` exists and
is in the deployed `.env`. Minting or rescoping an access policy needs Grafana Cloud admin
credentials that are not wired into any tooling in this repo, so **this is Rob's action, not
something an agent can complete here.** Everything after that - the config flip, the test assertion,
the verification call - is ordinary work and is on the acceptance criteria.

Final v0.4.0 deployment recheck: Camden still has agento11y.enabled=false and otlp.traces.enabled=false. No approved sigil:write credential appeared during the run, so no config was changed and no generation arrival is claimed. Resume boundary remains a deployed token carrying sigil:write; then enable, deploy, and verify individual accepted generations.
<!-- SECTION:NOTES:END -->
