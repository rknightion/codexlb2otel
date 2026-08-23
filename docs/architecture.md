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
 gzip member reader ──► frame decoder ──► turn reducer
                                              │
                        ┌─────────────────────┼────────────────────┐
                        ▼                     ▼                    ▼
                   OTLP metrics          Loki records        Tempo traces
                        │                                          │
                        └──────── checkpointed cumulative state ───┘
                                              │
                                   live view / generations
```

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
or safely update the baseline.
