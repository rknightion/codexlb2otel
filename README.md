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

## Status

Early. See [#1](https://github.com/rknightion/codexlb2otel/issues/1) for the build plan.
