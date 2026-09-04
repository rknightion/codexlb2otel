---
title: Architecture
description: How codexlb2otel tails gzip archives, reconstructs turns, checkpoints state, and fans out telemetry
tags:
  - OpenTelemetry
  - Operations
---

# Architecture

`codexlb2otel` is a stateful archive tailer. It does not proxy traffic and does not call
`codex-lb`; the only coupling is the conversation-archive directory on disk.

```text
codex-lb websocket archive
          │
          ▼
 gzip member reader ──► frame decoder ──► turn reducer ──► optional DB enrichment
                                              │
                        ┌─────────────────────┼────────────────────┐
                        ▼                     ▼                    ▼
                   OTLP metrics          Loki records        Tempo traces
                        │                                          │
                        └──────── checkpointed cumulative state ───┘
                                              │
                                   live view / generations
```

The drift runner is a separate in-process reader of the same archive directory. It compares each
scan with the embedded content-free baseline and feeds finding counts into self-observability; it
does not alter the baseline or the tailer's checkpoint.

## Archive reader

`codex-lb` appends complete gzip members. The reader can resume at a compressed byte offset and
resynchronize at member boundaries, so it does not have to inflate every earlier record after each
poll. Chunked reads bound memory use while a file is growing.

## Frame and turn reduction

The frame layer normalizes websocket and response events. The turn reducer then joins continuations,
tool calls and outputs, usage updates, rate limits, errors, and parent-agent references into one
logical turn. The CLI tools, live view, logs, metrics, and traces use the same reducer rather than
maintaining separate interpretations of the archive.

## Checkpoint contract

The checkpoint stores archive offsets plus cumulative reducer state. It does not contain prompt or
message bodies, but its map keys include conversation identifiers. Atomic writes and a clean-shutdown
save keep it recoverable; the configured checkpoint interval bounds duplicate replay after a hard
crash.

Reducer state has its own `archive.state_retain` policy. Completed series are aged by the newest
archive event timestamp, while a series with an open response is retained wholesale. A series that
returns after eviction is flagged `BaselineReset`; its current cumulative value is an upper bound,
not an exact delta. Deleted-file tombstones prevent a reclaimed path from being treated as the same
generation forever and are pruned after their UTC filename day is more than three days old.

## Enrichment boundary

When enabled, the Postgres source is read-only and additive. The response id drives an indexed
`request_logs.request_id` point lookup. A bounded background prefetch tails `request_logs.id` and
indexes the returned rows by both request id and `archive_request_id`; the latter is only a cache
alias and is never sent to the point query. Each lookup has its own timeout, and a database fault
cannot hold the archive checkpoint or stop another sink.

## Sink isolation

Each sink has its own queue, timeout, batching, and rejection counters. Permanent input or delivery
failures are counted and dropped rather than blocking the checkpoint forever. Retryable transport
failures use bounded retry.

Loki is intentionally native rather than OTLP logs because Loki stream labels and structured
metadata are part of the query contract. Metrics and traces share the OTLP gateway. Agent
Observability generations use their product-specific export endpoint.

## Drift detection

`clbprobe` compares archives with `corpus.sig.json`, a content-free schema signature. A sampled pass
is fast and detects common new shapes; only a full pass can establish that a rare shape disappeared
or safely update the baseline. The daemon's optional drift runner performs one immediate scan and
then repeats it at its configured interval. It keeps the last successful finding counts when a scan
fails and reports the run error separately. Its `codexlb.archive_drift_findings` gauge is labeled
by `codexlb.selfobs.severity`; `info` means disappearance and is not a page-worthy failure.
