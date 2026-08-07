# codexlb2otel

Tails [codex-lb](https://github.com/rknightion/codex-lb)'s conversation-archive files, derives
model and agent telemetry from the raw Codex websocket traffic, and emits it to Grafana Cloud as
**OTLP metrics** and **Loki logs**.

codex-lb captures every frame of the Codex CLI's `wss://chatgpt.com/backend-api/codex/responses`
session. That capture carries telemetry available nowhere else — OpenAI's internal engine ids, queue
wait, per-engine-call cache hit ratios, the sub-agent spawn tree, and the full conversation — none of
which appears in codex-lb's own metrics or its Postgres request log.

## What it deliberately does not do

- **No duplication.** codex-lb already exports 60 Prometheus metric families (proxy internals) and
  logs cost/latency/tokens to Postgres, already dashboarded. This adds only what the wire capture
  uniquely knows.
- **No decryption.** `reasoning.encrypted_content` is encrypted by OpenAI with OpenAI's key.
  Reasoning traces are permanently opaque; only token counts survive.

## Content warning

This ships **full conversation content** to Loki — assistant messages, tool input, and complete
command stdout. Anything the agent printed, including a secret it happened to `cat`, lands in your
log store. That is a deliberate choice for a private, single-tenant deployment. Set
`emit.loki.content: false` for an event timeline without bodies.

## The corpus

Captured archive hours live in `corpus/`, which is **gitignored in full and must stay that way** —
these files hold real prompts, tool output and assistant messages. Drop new captures in as they
arrive; subdirectories per source are the convention and are walked recursively:

```
corpus/
  nas-2026-08-06/     2026-08-06T10.jsonl.gz ...
  camden-2026-08-06/  2026-08-06T18.jsonl.gz ...
```

Source-tagging is not cosmetic. codex-lb reopens the archive `O_APPEND|O_CREAT` per batch, so moving
a file away makes it recreate the same path from scratch — `2026-08-06T18.jsonl.gz` exists as two
entirely unrelated captures, and a flat directory silently keeps whichever was copied last.

Tests discover the corpus rather than naming files (`CLB_CORPUS` overrides the location). A missing
corpus **fails**; CI sets `CLB_NO_CORPUS=1` to opt out explicitly. Two guards back this up:
`TestNoArchivesAreTracked` fails if git is tracking anything capture-shaped, and
`TestCorpusDirectoryIsIgnored` fails if the drop zone stops being ignored.

`corpus/` is ignored, so **`git clean -xdf` will delete it.** Keep the originals elsewhere.

## Tools

| | |
|---|---|
| `clbprobe` | has the format changed? Samples the `.gz` files in place and diffs against `corpus.sig.json`. |
| `clbprofile` | full induced schema of a capture — every field, type and value range. |
| `clbstat` | survey a directory and flag event types the reducer does not handle. |

`clbprobe` is the routine check. It reads compressed data in place and resynchronises each sampled
window onto a gzip member boundary, so 1.4 GB is characterised in about six seconds:

```sh
clbprobe corpus/                    # drift check, exits 1 on anything new
clbprobe -full corpus/              # exhaustive; required before concluding something is absent
clbprobe -full -update corpus/      # accept the current shape as the baseline
```

`corpus.sig.json` is the committed baseline. It is **content-free by construction** — values are
recorded only where they are provably enum-like and the field name is not identifier- or
content-shaped, and `TestSignature_CarriesNoConversationContent` pins that.

What it reports, in severity order:

- **breaking** — a known field arriving as a second JSON type (Go's decoder abandons the whole event
  on one mismatch; this is how 1,500 input items were silently discarded), a new payload framing
  such as SSE, or the multi-member property that byte-offset resume depends on going away.
- **new** — an unseen event type, field, header, or enum value (a new model, a new error code).
- **info** — anything that has *disappeared*. Never a failure: a sampled scan reads a fraction of the
  bytes and the rarest real shapes occur ~10 times in 1.3M records.

## Status

Early. See [#1](https://github.com/rknightion/codexlb2otel/issues/1) for the build plan.
