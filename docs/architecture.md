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
maintaining separate interpretations of the archive. Content items are retained in five arrays
(`Prompts`, `Messages`, `ToolCalls`, `ToolOutputs`, and `AgentMessages`) and receive one ordinal
sequence across all five arrays. Each item also carries its wire id when present, the timestamp of
the archive frame that produced it, and provenance (`live`, `replayed`, or `unknown`). That
`captured_at` value describes archive observation; it is not authorship time or execution start.

Function-call arguments remain valid JSON after the reducer's structural encrypted-value handling.
The reducer recursively replaces encrypted-looking string values with `"[omitted: encrypted]"`,
records the original byte length in `input_chars`, counts replacements in `input_omitted`, and
marks a bound replacement with `input_truncated`. Custom tool calls keep their existing fields.
The same reduced Turn feeds every sink, so ordering, provenance, redaction markers, and original
lengths remain consistent across the outputs.

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

The checkpoint also persists the bounded tool-call correlation index. It stores call entries with the
originating response id, turn id, tool name, archive capture time, and a 1-based call occurrence.
Consumed entries remain in retained history so a reused call ID receives the next occurrence. A
matching result is classified as `exact`, `ambiguous`, or `none`; an exact entry records its
`origin_call_occurrence` and is then consumed for result matching. Tool-call span IDs use
`hashSpanID(threadID, callID)` when the occurrence is at most 1 and add the occurrence to the hash
for later uses. Exact result links apply the same rule to `origin_call_occurrence`. These occurrence
fields are wire metadata, not span attributes.

The index keeps at most 512 entries per thread and at most 4,096 resident threads. On insertion
past the global bound, it evicts the thread whose newest entry is oldest, breaking ties with the
lexically smallest thread ID. Entries older than 24 hours on the archive clock are removed during
expiry; a thread with no retained entries is removed on that pass. Reuse protection applies only
within this retained correlation history. Per-thread capacity eviction, global thread eviction,
and expiry reset numbering for a later reuse, so this is not a lifetime guarantee. The checkpoint
state version is 6; version 5 restores the other reducer state with an empty call index. Replay
deduplication prevents a replay from re-inserting a call or consuming its origin a second time, and
lookup never crosses thread boundaries.

## Enrichment boundary

When enabled, the Postgres source is read-only and additive. The response id drives an indexed
`request_logs.request_id` point lookup. A bounded background prefetch tails `request_logs.id` and
indexes the returned rows by both request id and `archive_request_id`; the latter is only a cache
alias and is never sent to the point query. Each lookup has its own timeout, and a database fault
cannot hold the archive checkpoint or stop another sink.

Enrichment attaches cost, API-key, proxy status/error/phase, response-created and first-upstream
timings, three nullable proxy wait measurements, and upstream status/error/transport diagnostics.
The wait measurements retain null versus an observed zero. Queue wait covers account selection,
admission, and failed failovers outside the successful attempt's latency anchor; response-create
gate and bridge-queue waits are inside the HTTP bridge. They remain separate observations and are
never added into an end-to-end total. Freeform database or error bodies are excluded.

## Sink isolation

Each sink has its own queue, timeout, batching, and rejection counters. Permanent input or delivery
failures are counted and dropped rather than blocking the checkpoint forever. Retryable transport
failures use bounded retry.

Loki is intentionally native rather than OTLP logs because Loki stream labels and structured
metadata are part of the query contract. Content lines use their input-side or output-side event
timestamp, and equal timestamps are ordered by the reducer ordinal. Metrics and traces share the
OTLP gateway. A result that arrives on a later response is represented by a `tool_result` child
span linked to the origin call span; no completed span is amended. Agent Observability generations
use their product-specific export endpoint when explicitly enabled, while the current Camden
deployment keeps both generations and traces off.

## Drift detection

`clbprobe` compares archives with `corpus.sig.json`, a content-free schema signature. A sampled pass
is fast and detects common new shapes; only a full pass can establish that a rare shape disappeared
or safely update the baseline. The daemon's optional drift runner performs one immediate scan and
then repeats it at its configured interval. It keeps the last successful finding counts when a scan
fails and reports the run error separately. Its `codexlb.archive_drift_findings` gauge is labeled
by `codexlb.selfobs.severity`; `info` means disappearance and is not a page-worthy failure.
