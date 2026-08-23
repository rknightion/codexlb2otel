---
title: Signals
description: Metrics, Loki records, Tempo spans, and generation data emitted by codexlb2otel
tags:
  - OpenTelemetry
  - Loki
  - Tempo
---

# Signals

All outputs derive from the same reduced turns, but each signal is shaped for a different job.

## Metrics

The OpenTelemetry metric set covers:

- responses and completed turns;
- input, output, reasoning, cached, and tool token accounting;
- response, queue, engine, tool, time-to-first-token, and critical-path durations;
- tool calls, engine calls, transport events, and baseline resets;
- model, service-tier, reasoning-effort, request-kind, agent, and cache dimensions where available;
- rate-limit headroom and credit state; and
- ingest lag, checkpoint age and size, sink rejection, queue, archive-read, reducer, and attribute-
  rejection health.

Metric attributes are deliberately narrowed per instrument. Do not assume a label available on a
response counter also exists on token or latency series. The generated full dashboard validates its
queries against the source catalogue for this reason.

OTLP-to-Prometheus translation replaces dots with underscores and adds conventional counter and unit
suffixes. Query the backend's translated series names, not the dotted OTel names from source.

## Loki records

Each content or event kind is a separate JSON line:

| Record type | Purpose |
| --- | --- |
| `turn` | Reduced turn metadata, timing, usage, routing, and outcome |
| `prompt` | Human request content |
| `message` | Assistant content |
| `tool_call` | Tool name and arguments |
| `tool_output` | Tool result content |
| `agent_message` | Inter-agent communication |
| `instructions` | Instruction content present on the wire |
| `transport` | Bounded websocket and response events |
| `error` | Reduced failure information |

Bounded attributes can be promoted to stream labels; other fields remain structured metadata or JSON
body fields. See [Security](security.md) before enabling content records.

## Traces

Tempo receives a trace tree spanning the response and its engine and tool work. Trace attributes use
OpenTelemetry GenAI conventions where they match the actual wire semantics, plus `codexlb.*`
attributes for capture-specific details.

Tracing can be disabled or head-sampled independently from metrics. Sampling reduces trace volume but
does not change Loki or metric output.

## Agent Observability generations

The optional generation sink exports conversation and generation records to Grafana Agent
Observability. It is additive: enabling it does not replace Tempo, and enabling Tempo alone does not
populate the generation store.

## Cardinality boundary

The attribute catalogue marks which fields may become metric dimensions or Loki labels. IDs and
content-shaped values stay out of those bounded sets. Startup validation rejects unsupported Loki
labels instead of creating an unbounded stream topology.
