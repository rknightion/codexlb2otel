---
id: CXO-0006
title: 'agento11y needs the sigil:write scope; the token has only logs/metrics/traces'
status: Done
assignee:
  - '@codex'
created_date: '2026-08-14 16:59'
updated_date: '2026-09-05 18:59'
labels:
  - from-gh-issue
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
- [x] #1 Generations actually arrive, confirmed with gcx agento11y generations list - a 2xx is not enough, ExportGenerations can accept the request and still refuse individual generations, which is why accountResults parses the per-generation verdicts
- [x] #2 Production configuration enables Agent Observability generations and OTLP traces using the approved credential; public example defaults remain disabled for users without credentials.
- [x] #3 The deployed Agent Observability credential supports generation ingestion, proven by accepted genuine generations and cloud read-back; reuse of the existing working credential is authorized.
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [x] #1 make check passes: gofmt -l . reports nothing, go vet ./... clean, go test ./... green
- [x] #2 go build ./... succeeds
<!-- DOD:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
Verify the authorized credential without printing it; deploy it to the existing production env file; enable generation and trace sinks without resetting checkpoints; recreate only the exporter; verify genuine generation and matching trace arrival. Record production evidence and preserve unrelated work.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
**Parked, resume boundary:** un-park once the access-policy token carrying `sigil:write` exists and
is in the deployed `.env`. Minting or rescoping an access policy needs Grafana Cloud admin
credentials that are not wired into any tooling in this repo, so **this is Rob's action, not
something an agent can complete here.** Everything after that - the config flip, the test assertion,
the verification call - is ordinary work and is on the acceptance criteria.

Final v0.4.0 deployment recheck: Camden still has agento11y.enabled=false and otlp.traces.enabled=false. No approved sigil:write credential appeared during the run, so no config was changed and no generation arrival is claimed. Resume boundary remains a deployed token carrying sigil:write; then enable, deploy, and verify individual accepted generations.

2026-09-05 decision (Rob, wave 2 pre-flight): leave agento11y AND otlp traces disabled for wave 2. No sigil:write token will be minted before that wave. Stays Parked; resume boundary unchanged.

2026-09-05 updated decision from Rob: enable conversations and traces now; reuse the proven Claude Personal Agent Observability credential. This supersedes the earlier Wave 2 disablement. Scope is the production configuration; public example defaults remain safe for users without credentials.

Acceptance criterion 2 updated to the authorized production scope. This is a deployment configuration repair, with no application logic change; safe public example defaults and their test remain unchanged. Skipped new tests and CodeRabbit for declarative configuration; validated Compose and live cloud delivery instead.

Production restored on 2026-09-05 at 18:56 UTC. Reused the explicitly authorized working credential without minting or rescoping a policy. Both sinks enabled; Compose validated and only exporter recreated, checkpoint retained. Genuine codex and codex/subagent catalog timestamps advanced to today. Direct generation read-back linked to the same Tempo trace/span, streamText with SPAN_KIND_CLIENT and STATUS_CODE_OK. Container healthy with zero restarts. Historical replay was not attempted. The credential criterion now explicitly permits the user-authorized existing credential rather than requiring unnecessary token minting.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Restored production Agent Observability conversations and OTLP traces using the authorized existing credential. Genuine codex and codex/subagent generations arrived after restart; a direct generation read linked to an identical Tempo trace/span with SPAN_KIND_CLIENT and STATUS_CODE_OK. Container healthy with zero restarts. Compose validation and dry-run passed. Repository just check and just build passed (the historical make-check DoD is satisfied by the current just task surface). No application source or public example defaults changed. Archive checkpoint preserved; no historical backfill performed.
<!-- SECTION:FINAL_SUMMARY:END -->
