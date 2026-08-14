---
id: CXO-0005
title: >-
  Self-observability deadlocked the poll it observes: every metric flush timed
  out at 30s
status: Done
assignee: []
created_date: '2026-08-14 16:59'
labels:
  - from-gh-issue
dependencies: []
references:
  - [purged issue archive]
type: bug
ordinal: 5000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
Migrated from GitHub issue #29 (full body and 1 comment in `[purged issue archive]`). The issue was
filed **after** the fix landed, so the diagnosis would not live only in a commit message - so this
task is imported already `Done`. It is a record, not open work.

## Symptom

First production run, 2026-08-07. Every poll, forever:

```
level=ERROR msg="poll failed" err="read 2026-08-06T18.jsonl.gz: flush: otlpmetric: otlpmetric: flush: context deadline exceeded"
```

Nothing reached Mimir. The checkpoint never advanced - correctly, since a failed flush must hold it -
so the same chunk was re-read every 30s and no archive file was ever reclaimed. It read as a network
timeout against the OTLP gateway. It was not one.

## Cause

`Poll` holds `mu` for the whole pass. Inside that pass: `emit` -> `sinkEmit` ->
`otlpmetric.Sink.Flush` -> `MeterProvider.ForceFlush` -> reader `Collect` -> the SDK runs the
registered **async-instrument callback** -> that callback is `internal/selfobs` asking **this same
watcher** for `Progress()` -> `Progress()` took `mu.RLock()`.

A read lock cannot be acquired while the calling goroutine's own stack already holds the write lock.
`Collect` blocked until the `PeriodicReader`'s **default 30s timeout** fired.

Two things made it look like something else: the 30s is the *reader's* default, not `otlp.timeout`,
so raising `otlp.timeout` changed nothing and the number looked like a coincidence; and a blocked
`Collect` surfaces as `context deadline exceeded`, exactly what a slow network produces.
<!-- SECTION:DESCRIPTION:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Fixed by `ec6ecbc`. `Progress()` now takes **no lock**: the watcher publishes a snapshot under the
write lock at the end of each pass (and at construction, so a collection arriving before the first
poll reads a zero value rather than a half-initialised one) and `Progress()` reads it through an
`atomic.Pointer`. A reader sees state as of the last completed pass instead of a half-finished one,
which for an operational metric is not a cost.

Regression test `TestWatcher_ProgressIsSafeFromInsideEmit` calls `Progress` from inside the emit
callback and bounds `Poll` at 10s - the failure mode is "never returns", so an unbounded call would
wedge the suite instead of failing it. Mutation-checked by restoring the `RLock`, which fails it at
the 10s bound.

**Why no test caught it:** both halves were tested, neither together. `internal/tail`'s tests drive
`Poll` with an emit callback that does nothing; `internal/sink/otlpmetric`'s selfobs tests use a
`ManualReader` and call `Collect` from the test goroutine, holding no watcher lock. The deadlock
only existed where the two meet.

**The shape worth remembering: a callback registered with a library is code the library calls on a
goroutine and at a moment of its choosing - including re-entrantly, from inside a call you made. Any
lock held across that call is a lock the callback must not take.**

Found alongside it, not caused by it: the agento11y 401 - see the sigil scope task. The sink
correctly treats it as a config fault and holds the checkpoint, which means one missing scope stops
every other signal too; `agento11y` now ships disabled.
<!-- SECTION:FINAL_SUMMARY:END -->
