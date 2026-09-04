---
id: CXO-0002
title: Run the drift probe on a schedule and alert on it
status: Done
assignee:
  - '@codex'
created_date: '2026-08-14 16:58'
updated_date: '2026-09-04 20:20'
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
- [x] #2 Scheduled run against the live archive directory
- [x] #3 Drift findings exposed as a metric by severity, with an alert on breaking
- [x] #4 A protocol change deliberately injected into a fixture is caught end to end
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [x] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
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

L5 implemented at commit 472ff94: added internal/drift with injectable baseline and scanner seams, immediate plus interval scheduling, sampled or full archive scanning, metric-ready finding counts for breaking, new, and info, last run time and error stats, and tests for severity aggregation, scheduled repeats, error-stat retention, and an injected protocol type change caught end to end. Deleted the non-runnable self-hosted scheduled workflow. Updated dashboards/alerts/drift-breaking.yaml to use codexlb_archive_drift_findings with codexlb_selfobs_severity="breaking". Validation passed: gofmt, go test -race -count=1 ./internal/drift, go vet ./internal/drift, and git diff check. Wiring to the embedded baseline and OTLP remains in the root integration pass. Decisions: run immediately on startup before the interval; retain the last successful finding counts when a later scan errors while updating LastRun and LastError.

Root wiring landed at 6a54f61. Camden runs the in-process sampled probe immediately at startup with a 24 hour interval. Live m7kni values after the partial-SHA deployment were breaking=2, new=172, and info=75; the breaking alert query and dashboard panel use the real metric and label. The two breaking findings are tracked in CXO-0024 and must not be baselined without classification. Final just check passed at 334a4db; final-SHA deployment remains CXO-0025.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Replaced the unrunnable scheduled workflow with an immediate and periodic in-process archive drift probe, exported bounded severity metrics, and wired the breaking alert. Focused race tests, injected protocol-drift coverage, final just check, and live m7kni values prove the implementation. The full corpus suite remains explicitly unproven.
<!-- SECTION:FINAL_SUMMARY:END -->
