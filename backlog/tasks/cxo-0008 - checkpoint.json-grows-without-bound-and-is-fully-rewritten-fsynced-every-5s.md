---
id: CXO-0008
title: checkpoint.json grows without bound and is fully rewritten + fsynced every 5s
status: To Do
assignee: []
created_date: '2026-08-14 17:00'
updated_date: '2026-08-23 12:10'
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
- [ ] #1 Decide and record whether eviction is by age, and what the age is anchored to
- [ ] #2 prev/seq entries for long-dead series are removed; live and mid-turn ones never are
- [ ] #3 A checkpoint save that would write identical bytes is skipped
- [ ] #4 A test proves an evicted-then-returning series is flagged BaselineReset rather than silently over-counting - the failure this whole file exists to prevent
- [ ] #5 Growth and save rate measured on the live host before and after
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 make check passes: gofmt -l . reports nothing, go vet ./... clean, go test ./... green
- [ ] #2 go build ./... succeeds
<!-- DOD:END -->
