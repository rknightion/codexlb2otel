---
id: CXO-0008
title: checkpoint.json grows without bound and is fully rewritten + fsynced every 5s
status: Done
assignee:
  - '@codex'
created_date: '2026-08-14 17:00'
updated_date: '2026-09-04 20:49'
labels:
  - enhancement
  - from-gh-issue
dependencies: []
priority: high
type: bug
ordinal: 8000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
Migrated from the former GitHub issue tracker.\nAsked in passing ("will it grow indefinitely?"), measured rather than assumed. The answer is yes,
and there is a second-order effect that is the bigger cost today.

## Measured, 2026-08-09

```
files entries:        55   (43 tombstoned deleted)         6,308 bytes
reducer.prev entries: 950                                639,941 bytes   <- 96% of the file
reducer.seq  entries: 445                                 19,174 bytes
                                                   total 665,539 bytes
```

55 hourly file entries ~= 2.3 days of running, so roughly **8 new threads/hour, 17 new series/hour**,
at ~673 bytes per `prev` entry.

## 1. Nothing is ever pruned

Three maps, none with an eviction path - `rg 'delete\('` over `internal/tail` and
`internal/turn/state.go` finds nothing that removes from any of them. `reducer.prev` (keyed by
thread x request_kind) is 96% of the file; `reducer.seq` is one int per thread ever seen; `files`
keeps a `Deleted: true` **tombstone** per reclaimed archive so a reappearing name is not re-read -
correct by design (`2026-08-06T18` has existed as two unrelated files) but monotonic.

~275 KB/day, ~100 MB/year, dominated by dead threads that will never be seen again.

**Why it cannot be pruned by count:** `prev` exists because a series resuming with no baseline
reports a whole logical turn's tokens and wall time as one response's delta. Dropping a *live*
thread's entry re-introduces exactly the over-count the file prevents. Eviction has to be by
last-seen age, and that needs a timestamp that is not currently persisted.

## 2. The real cost today: write amplification

`Watcher.Poll` calls `save()` **unconditionally on every poll** (`watcher.go:344`), and `save()` does
a full `Snapshot()` of both maps, a full JSON marshal, a temp-file write, an **fsync** and a rename -
no dirty check, no dependence on whether anything changed. At `archive.poll_interval: 5s` that is
17,280 saves/day, so at 612 KB: **~10.5 GB written and fsynced per day**, growing linearly with
thread count, to persist a file whose contents mostly did not change.

The atomicity is not the problem and must stay - a half-written checkpoint surviving a crash is
worse than no checkpoint. Doing it every 5 seconds regardless of need is the problem.

The size is already observable: `Watcher.Progress` exposes `ReducerSeriesCount` and
`ReducerThreadCount` (`watcher.go:239-240`). Nothing acts on it and nothing alerts on it.

## Options, not yet decided

1. Skip the save when nothing changed - cheapest, removes most of the 10 GB/day on an idle host.
   Needs a dirty flag set by `readFile`/`Evict`/`reclaim`.
2. Age out `prev`/`seq` entries. Must not evict a series that is merely quiet mid-turn - the same
   trap `live.retain_window` already hit, where a plain age cutoff evicts the wedged agent first.
3. Both, which is probably right: (1) fixes the write cost now, (2) fixes the growth.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 Decide and record whether eviction is by age, and what the age is anchored to
- [x] #2 prev/seq entries for long-dead series are removed; live and mid-turn ones never are
- [x] #3 A checkpoint save that would write identical bytes is skipped
- [x] #4 A test proves an evicted-then-returning series is flagged BaselineReset rather than silently over-counting - the failure this whole file exists to prevent
- [x] #5 Growth and save rate measured on the live host before and after
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [x] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
<!-- DOD:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
Wave 1: implement age-anchored reducer and tombstone eviction with mid-turn exemption and baseline-reset regression coverage, then measure live after deployment.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Live measurement 2026-09-03 14:40 UTC on camden (/opt/codexlb2otel/data/checkpoint.json), the BEFORE figure for AC #5: 5,683,577 bytes total; files 532 entries (523 deleted tombstones) 63 KB; reducer.prev 8,854 entries 5.84 MB; reducer.seq 3,891 entries 168 KB. Versus 665 KB on 2026-08-09 that is ~200 KB/day growth, and at one full rewrite+fsync per 5s poll roughly 98 GB/day written. Deployed archive.poll_interval is 5s; config has a checkpoint_interval key (internal/config/config.go:117) - check whether it is already honoured by the save path before designing the dirty flag.

CORRECTION 2026-09-03: the description's section 2 (save on every poll) is STALE and my earlier 98 GB/day figure in these notes was WRONG. Commit 7d93bc6 (2026-08-09, perf(tail): checkpoint on an interval, not on every poll) added archive.checkpoint_interval (default 15m, internal/tail/watcher.go saveDue) and an identical-bytes skip via a content fingerprint (watcher.go save, lastSaved). AC #3 is therefore already met and is checked here on code evidence plus the live config (no checkpoint_interval override, so 15m). Current write cost is bounded at ~96 saves/day x 5.7 MB = ~0.5 GB/day. What remains is section 1 only: reducer.prev/seq/files tombstones are still never evicted (8,854 prev entries live). AC #1, #2, #4 and the AFTER half of #5 are the open work.

L4 implementation landed at commit d83368b. Age-based reducer state eviction is anchored to archive timestamps for timing baselines; reducer state v3 restores with timestamps defaulted to load time; outer checkpoint format v2 accepts v1 and rewrites current format. Dead prev and seq entries are removed, open response threads are retained, returning evicted series emits BaselineReset with the full current cumulative reading, Progress counts shrink after eviction, and file tombstones more than three UTC filename days old are pruned. Focused race tests, go vet, gofmt, just lint, just build, just test-short, and git diff check passed. One broader go test -race invocation unexpectedly consumed the present corpus despite CLB_NO_CORPUS=1 and timed out after 10 minutes in pre-existing TestReducer_NoNegativeDeltas while decompressing all 58 archives; internal/tail completed green. Decision: open response exemption is thread-wide because request_kind can arrive after opening. Integration still must wire archive.state_retain. Review note for L8: replace the temporary seqSeenByReducer sidecar with a Reducer-owned timestamp field and update it on every logical-turn sequence increment so a recent seq entry without a timing baseline is not eligible immediately.

After measurement at 2026-09-04T19:53:43Z, 30 minutes 40 seconds after the partial-SHA container start: checkpoint size 7,049,349 bytes; files 68 with 48 tombstones; reducer.prev 9,370; reducer.seq 4,094; checkpoint version 2. Before was 5,683,577 bytes, files 532 with 523 tombstones, prev 8,854, and seq 3,891. The file grew under fresh traffic, so no shrink is claimed, while stale file entries and tombstones were materially pruned. The configured 15 minute periodic save contract remains in force with identical-byte skips, plus explicit reclaim and clean-shutdown saves. Container restart count was zero and Docker health was healthy. Final just check passed; the full corpus just test was cancelled and remains unproven.

Review reconciliation: the claimed unwired state_retain seam is stale. Current cmd/codexlb2otel/main.go passes cfg.Archive.StateRetain directly into tail.Config. internal/config/load_test.go proves 168h parses from YAML and internal/tail/watcher_test.go TestWatcher_StateRetainShrinksPublishedReducerCounts proves that configured value evicts old reducer state. No source change was required.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Bounded reducer and checkpoint state with archive-time eviction, open-response protection, safe baseline resets, version-compatible restore, and stale tombstone pruning. Focused race tests and final just check passed; the 30 minute live sample proved tombstones fell from 523 to 48 while fresh traffic increased active reducer maps and total file size, so no false shrink claim is made.
<!-- SECTION:FINAL_SUMMARY:END -->
