---
id: CXO-0002
title: Run the drift probe on a schedule and alert on it
status: To Do
assignee: []
created_date: '2026-08-14 16:58'
updated_date: '2026-08-23 12:10'
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
<!-- SECTION:NOTES:END -->
