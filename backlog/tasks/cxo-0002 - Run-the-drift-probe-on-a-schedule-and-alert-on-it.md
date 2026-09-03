---
id: CXO-0002
title: Run the drift probe on a schedule and alert on it
status: In Progress
assignee:
  - '@codex'
created_date: '2026-08-14 16:58'
updated_date: '2026-09-03 18:48'
labels:
  - enhancement
  - from-gh-issue
dependencies: []
priority: medium
type: enhancement
ordinal: 2000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
Migrated from the former GitHub issue tracker.\nFollows on from `e3cb67d`.

`clbprobe` detects an archive format change in ~6s across 1.4GB. Run by hand it catches drift
whenever someone remembers to run it. The failure it guards against is silent: a field that gains a
second JSON type makes Go's decoder abandon the whole event, which looks like slightly fewer turns,
not like an error.

Scope:
- **In CI** - `clbprobe -full -fail-on breaking` against `corpus.sig.json`, skipped where no corpus
  is present.
- **Against the live archive** - a scheduled run over codex-lb's actual archive directory, since
  that is where a change appears first. Sampled mode is the right default at ~6s for a day.
- **As telemetry** - `codexlb_archive_drift_findings{severity="breaking|new|info"}`, so drift becomes
  an alert rather than a report nobody reads. `breaking` pages, `new` notifies.

Traps: sampling **proves nothing about rare shapes** - the rarest real shapes occur ~10 times in
1.32M records, so `-full` belongs on a slower cadence. Absence findings are informational by design
and must never page. `-update` from a sampled scan is warned against for good reason: regenerating
from `-full` is the only correct way to accept a change.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 CI job fails on a breaking finding where a corpus is available
- [ ] #2 Scheduled run against the live archive directory
- [ ] #3 Drift findings exposed as a metric by severity, with an alert on breaking
- [ ] #4 A protocol change deliberately injected into a fixture is caught end to end
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 make check passes: gofmt -l . reports nothing, go vet ./... clean, go test ./... green
- [ ] #2 go build ./... succeeds
<!-- DOD:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
Wave 1: replace the non-runnable scheduled workflow with the frozen in-process probe, embed the full-scan baseline, expose findings, then wire and verify live.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
State at migration (2026-08-14), verified in the checkout rather than taken from the issue:

- **AC 1 is already met.** `.github/workflows/ci.yml:108` runs `/tmp/clbprobe -fail-on breaking corpus`.
- **AC 2 is written but NOT live.** `.github/workflows/scheduled-archive-probe.yml` exists and its
  own header says so in capitals: it targets `runs-on: [self-hosted, ...]` with a **placeholder
  label set**, because the archive lives on the codex-lb host and is reachable only over Tailscale,
  so a GitHub-hosted runner has no path to it. Until a self-hosted runner carrying those labels is
  registered on the tailnet, every trigger queues forever with nothing to pick it up. The
  archive-path placeholder needs a human too. **Do not read the file's presence as the job running.**
- AC 3 and AC 4 are untouched.

Decision 2026-09-03 (Rob to confirm before the wave): AC #2 is satisfied IN-PROCESS, not by GitHub Actions. rknightion is a User account with no runner groups and public-repo CI must not run on self-hosted runners, so scheduled-archive-probe.yml can never go live; delete it. codexlb2otel already tails the live archive, so it runs the sampled probe itself on a schedule (config block probe: enabled/interval, default 24h, sampled) against an embedded baseline and emits codexlb.archive_drift_findings (gauge, {finding}) by codexlb.selfobs.severity in breaking|new|info. The alert rule and the legacy panel already reference that name. Evidence the guard is needed: today's probe against the synced corpus reports 7 breaking and 576 new findings (see CXO-0007) that nothing had watched for since 2026-08-09.
<!-- SECTION:NOTES:END -->
