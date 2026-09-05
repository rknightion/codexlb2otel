---
title: codexlb2otel
description: OpenTelemetry and Loki observability derived from codex-lb conversation archives
tags:
  - OpenTelemetry
  - Grafana
---

# codexlb2otel

`codexlb2otel` tails the conversation archives written by
[`codex-lb`](https://github.com/rknightion/codex-lb), reconstructs complete Codex turns, and exports
the signals that the proxy's request metrics cannot see.

It produces:

- OpenTelemetry metrics for models, tokens, cache behavior, rate limits, tools, latency, and
  exporter health;
- structured Loki records for turns, messages, tool activity, errors, and transport events;
- optional Tempo traces for response, engine-call, tool-call, and cross-response tool-result timing;
- optional Grafana Agent Observability generations; and
- an optional live view of current root sessions and their subagent trees.

## Why use it?

`codex-lb` already measures the proxy. This project reads the websocket archive to expose
conversation-level facts that the proxy's request metrics cannot see: served engine IDs, engine wait
breakdowns, per-call cache behavior, tool activity, response continuations, and parent-child agent
relationships. Optional read-only Postgres enrichment adds proxy wait breakdowns and bounded
diagnostics alongside those wire-derived facts. Content records retain capture order and provenance,
and function arguments keep valid JSON while marking opaque encrypted values.

The exporter does not decrypt encrypted reasoning content and does not duplicate the proxy's
existing request telemetry. Proxy enrichment is optional and read-only; its wait measurements are
kept separate rather than summed into a guessed end-to-end duration. Camden keeps Tempo and Agent
Observability disabled permanently because native per-profile Codex integration owns that observation
path.

## Start here

- [Getting started](getting-started.md) — build or run the container and perform a local smoke test.
- [Configuration](configuration.md) — configure archives, sinks, retention, health, and the live view.
- [Signals](signals.md) — understand what reaches metrics, logs, traces, and generations.
- [Security](security.md) — read this before enabling content-bearing sinks.
- [Dashboards](dashboards.md) — deploy or inspect the coverage-checked Grafana dashboard.

Source and releases are at
[github.com/rknightion/codexlb2otel](https://github.com/rknightion/codexlb2otel).
