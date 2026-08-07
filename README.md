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

Captured archives live in `corpus/processed/`, which is **gitignored and must stay that way** — these
files hold real prompts, tool output and assistant messages.

```sh
./corpus/sync              # pull anything new from camden, then offer to probe it
./corpus/sync -dry-run     # say what it would fetch
./corpus/sync -yes         # no prompts
./corpus/sync -progress    # rsync's live per-file counter
```

`clbsync` identifies files by a fingerprint of their first bytes, **not by name**. That is not
fussiness: codex-lb opens the archive `O_APPEND|O_CREAT` per batch, so moving a file away makes it
recreate the same path from scratch, and `2026-08-06T18.jsonl.gz` has already existed as two entirely
unrelated captures. A name-only sync keeps whichever it saw first and never fetches the second. The
fingerprint is stable as a file grows and changes when it is recreated, which separates the three
cases that matter: **new**, **grown** (the hour codex-lb is still writing, refetched cheaply by
rsync), and **replaced** (kept alongside its predecessor as `NAME.gen2.jsonl.gz`, never overwritten).

Tests discover the corpus rather than naming files (`CLB_CORPUS` overrides the location). A missing
corpus **fails**; CI sets `CLB_NO_CORPUS=1` to opt out explicitly. Two guards back this up:
`TestNoArchivesAreTracked` fails if git is tracking anything capture-shaped, and
`TestCorpusDirectoryIsIgnored` fails if the drop zone stops being ignored.

`corpus/` is ignored, so **`git clean -xdf` will delete it.** Keep the originals elsewhere.

## Running the tools

Everything runs from the repo root. `go run` needs no build step but recompiles each time, which is
noticeable on a tool you use interactively:

```sh
go run ./cmd/clbfind resp_052b6a...        # no build step
make build && ./bin/clbfind resp_052b6a... # faster to re-run; bin/ is gitignored
```

Every tool prints its own `-h`. Common tasks have Make targets:

| | |
|---|---|
| `make build` | build all five tools into `bin/` |
| `make sync` | pull new archives off the codex-lb host |
| `make probe` | fast drift check against the baseline (exit 1 on anything new) |
| `make probe-full` | exhaustive drift check |
| `make baseline` | accept the current shape as the baseline (always from a full scan) |
| `make check` | gofmt + vet + tests |
| `make test-short` | tests without the corpus, the fast inner loop |

`make probe CORPUS=some/other/dir` overrides the directory.

## Tools

| | |
|---|---|
| `corpus/sync` | pull new archives off the codex-lb host (wraps `cmd/clbsync`) |
| `clbfind` | look up one response by id: why it ran as it did, and what was said |
| `clbprobe` | has the format changed? Samples the `.gz` files in place and diffs against `corpus.sig.json`. |
| `clbprofile` | full induced schema of a capture — every field, type and value range |
| `clbstat` | survey a directory and flag event types the reducer does not handle |

### clbfind

Paste an id from codex-lb's UI, a dashboard or an alert:

```sh
clbfind resp_052b6a1e90eb18d3016a75b032be908191   # search the whole corpus
clbfind resp_052b6a... corpus/processed/2026-08-07T10.jsonl.gz   # just that archive
clbfind -thread resp_...                          # the whole conversation it belongs to
clbfind -json ws_481fa8fa32724155a2ff8f372d7448ce # the reduced Turn
```

Paths after the id narrow the search. codex-lb's request-details view names the archive a response
came from, so pasting it alongside the id cuts a lookup to ~2.7s against ~5.6s for the whole corpus,
and the gap widens as the corpus grows. `-thread` deliberately widens back out — a conversation runs
for hours and crosses archive rotations, so honouring the narrowing would truncate the transcript at
an hour boundary.

Both passes are sharded across cores. That is only possible because codex-lb closes a gzip member per
batch, so a worker can seek into the middle of a file and resynchronise instead of decoding
everything before it.

It prints the model and reasoning parameters, the routing metadata that explains them (request kind,
thread source, subagent kind, parent thread), the critical-path timing breakdown, token usage,
per-account rate-limit headroom, and the conversation itself.

Built on the same `internal/turn` reducer that feeds the exporters, so what it prints is what will
land in Loki rather than a second view of the archive that drifts separately. It is the working
prototype of the query this project exists to enable.

**Note on ids.** codex-lb's UI labels `resp_*` as "Request ID". The archive's own `request_id` is
something else — `ws_<hex32>` or a UUID. `clbfind` takes either, but the distinction matters when
querying: they are different keys.

**One response is rarely the whole answer.** A mid-turn continuation carries only the tool result
that provoked it; the human's actual request is several responses back. `-thread` replays the whole
conversation through one reducer in archive order.

### clbprobe

The routine drift check. Reads compressed data in place and resynchronises each sampled window onto a
gzip member boundary, so 1.4 GB is characterised in about six seconds:

```sh
clbprobe corpus/                    # drift check, exits 1 on anything new
clbprobe -full corpus/              # exhaustive; required before concluding something is absent
clbprobe -full -update corpus/      # accept the current shape as the baseline
```

`corpus.sig.json` is the committed baseline. It is **content-free by construction** — values are
recorded only where they are provably enum-like and the field name is neither identifier- nor
content-shaped, and `TestSignature_CarriesNoConversationContent` pins that.

What it reports, in severity order:

- **breaking** — a known field arriving as a second JSON type (Go's decoder abandons the whole event
  on one mismatch; this is how 1,500 input items were silently discarded), a new payload framing
  such as SSE, or the multi-member property that byte-offset resume depends on going away.
- **new** — an unseen event type, field, header, or enum value (a new model, a new error code).
- **info** — anything that has *disappeared*. Never a failure: a sampled scan reads a fraction of the
  bytes and the rarest real shapes occur ~10 times in 1.3M records.

Tool `parameters` and `format` subtrees are recorded opaquely. They are JSON Schema the *client*
writes to describe its own tool config, and walking them buried two real findings under 217 noise
items the first time a capture contained a tool the baseline lacked. Tool identity is still tracked
at `tools[].name`, so a genuinely new tool still surfaces.

## Status

Early. See [#1](https://github.com/rknightion/codexlb2otel/issues/1) for the build plan.
