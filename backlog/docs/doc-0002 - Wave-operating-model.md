---
id: doc-0002
title: Wave operating model
type: guide
created_date: '2026-08-14 17:01'
updated_date: '2026-08-14 17:01'
---
This document carries **only what is specific to codexlb2otel**. The campaign model itself - run
contract and run modes, the routing contract, authority and the thread pool, child lane briefs,
external-contract freezing, the unattended blocker contract, the goal-file template and the
pre-flight checklist - is the "Agent fan-out protocol (canonical)" document. Read that first. If
anything here reads like a restatement of it, delete it here rather than maintaining two copies.

## The gate

```bash
make check        # gofmt -l . ; go vet ./... ; go test ./...
go build ./...
```

`make test-short` (`CLB_CORPUS=/nonexistent CLB_NO_CORPUS=1 go test ./...`) is the fast inner loop -
the corpus tests are the slow ones. **A green `test-short` is not a green gate.** CI runs with
`CLB_NO_CORPUS=1` too, so corpus-backed assertions are skipped there as well; anything that claims a
corpus-measured fact has to be run locally against a real corpus and the number recorded in the task.

`make build` depends on **every** Go file, not on `cmd/<tool>`. A directory's mtime does not change
when a file inside it is edited, so the obvious dependency silently left a stale binary in place.
Do not "tidy" that back.

## Exclusive resources - one lane at a time, no exceptions

| Resource | Why it is exclusive |
| --- | --- |
| The live archive directory on the codex-lb host | Reachable only over the tailnet. Reads are cheap, but two lanes running `clbsync` race on the same local corpus tree. |
| `corpus/` and `corpus.sig.json` | The baseline is a **deliberate act**, always from a `-full` scan. A sampled scan must never set it, and two lanes cannot both accept a baseline. |
| The Grafana Cloud stack | Pushing telemetry live is observable by everything else pointed at it. A lane that emits must say so in its brief. |
| The deployed service and its config | One deployment, one owner per wave. |

## Contended files - a wiring pass, never parallel lanes

`internal/attr/attr.go` (the attribute registry), `internal/turn/reducer.go`,
`internal/sink/otlpmetric/record.go`, `config.example.yaml` and `backlog/config.yml`. Every wave that
touches telemetry touches at least two of these. Assign them to one lane or handle them in the wiring
pass.

## Recurring defects in THIS codebase

Each of these has already happened here at least once. They are the things to look for in review,
not general advice.

**Narrowing silently drops attributes.** `attr.Only(...)` (`internal/attr/attr.go:620`) is how every
instrument builds its attribute set, and an attribute simply absent from the list is gone with no
error and no test failure. This produced a real correctness bug: `codexlb.family` was dropped from
every token counter and every duration histogram, so every cost and latency number published to
Prometheus silently included synthetic probe traffic and no query could remove it (CXO-0003). When
you add an instrument, state which attributes it deliberately drops **and why**.

**A callback registered with a library is code that library calls at a moment of its choosing -
including re-entrantly, from inside a call you made.** Self-observability's async-instrument callback
asked the watcher for `Progress()` while `Poll` held the write lock; `Collect` blocked until the
`PeriodicReader`'s **default 30s timeout** - not `otlp.timeout`, which is why raising that changed
nothing - and every flush failed with `context deadline exceeded`, which reads exactly like a slow
network. Nothing was ingested at all (CXO-0005). Any lock held across a library call is a lock its
callbacks must not take.

**Both halves tested, never together.** That deadlock existed only where `internal/tail` and
`internal/sink/otlpmetric` meet: tail's tests drive `Poll` with an emit callback that does nothing,
the sink's tests call `Collect` from the test goroutine holding no watcher lock. When a wave changes
a seam, one test must exercise the seam itself. Note the shape of the regression test that caught
it, `TestWatcher_ProgressIsSafeFromInsideEmit`: the failure mode was "never returns", so it **bounds**
`Poll` at 10s rather than asserting on a value, or the suite would wedge instead of failing.

**A plain age cutoff evicts the wedged agent first.** It hit `live.retain_window`, and it is waiting
in the checkpoint-eviction work (CXO-0008): the thread that has been quiet longest is the interesting
one, not the dead one. Anything time-based here needs a mid-turn exemption and a test for it.

**A config fault on one sink stops every signal.** A 401 is classified as never-retryable, the error
propagates and the watcher correctly holds the checkpoint - so one missing scope on `agento11y` also
stopped Loki, metrics and traces, and the service ingested nothing (CXO-0006). Correct behaviour,
dangerous blast radius: a new sink ships **disabled** in `config.example.yaml`, with
`TestLoad_ExampleConfigIsDeployable` asserting it stays that way, so enabling it is a decision made
with the credential in hand.

**Sampling proves nothing about rare shapes.** The rarest real shapes occur ~10 times in 1.32M
records. `clbprobe -sampled` is right for a schedule and wrong for a baseline or for any claim that a
shape is absent. Absence findings are informational **by design** and must never page.

**Corpus tests assume the cheap archives are a rich sample.** Two quiet hours broke seven tests once.
A test that needs variety must select for it, not assume the newest capture has it.

## Personal data - the hard boundary

The conversation archives hold full prompts, tool output and assistant messages. They are gitignored
by tree and by extension, and `TestNoArchivesAreTracked` (`internal/fixture/tracked_test.go`) fails
the build if anything of that shape is ever staged. `corpus.sig.json` is content-free **by
construction** and `TestSignature_CarriesNoConversationContent` is what keeps it that way - during
earlier drift work, descending into embedded JSON briefly exposed a home-directory layout and private
repo names before that was closed.

The same bar applies to `backlog/`: write the shape, not the instance. Sweep before committing:

```bash
grep -rniE "@gmail|@rob-knight|/Users/rob|[0-9]{1,3}(\.[0-9]{1,3}){3}" backlog/ && echo "REVIEW THESE"
```

`[purged issue archive]` is the deliberate exception and is redacted - see `[purged issue archive]` for
the placeholder mapping and the non-redactions.

## Ownership and the escape hatch

A lane owns files, not topics. The escape hatch is explicit: **a lane that hits a decision its brief
does not cover STOPS and returns the question.** In this repo that specifically means any new
attribute key, any change to an instrument's cardinality, any baseline acceptance, and any change to
what a sink does with an error class. Those are contract decisions; a lane inventing an answer costs
more than the round trip. A boundary with no escape hatch is a stop condition wearing a safety label.

## Run-end against this tracker

- Landed work: `backlog task edit <id> --check-ac N -s Done` in **one call**, with the SHA in the
  final summary.
- Blocked work: `Parked`, with a concrete resume boundary in the notes - the condition that makes it
  un-parkable, not "needs more thought". CXO-0004 and CXO-0006 are the worked examples.
- Discovered work: a new task labelled `needs-triage`. Never a note in a terminal that nobody reads.
- The closing message to the terminal is a covering note - what the run learned that no single task
  captures. **Nothing durable may live only there.**
