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
- optional response cost and API-key dimensions;
- rate-limit headroom and credit state; and
- ingest lag, checkpoint age and size, sink rejection, queue, archive-read, reducer, and attribute-
  rejection health.

Metric attributes are deliberately narrowed per instrument. Do not assume a label available on a
response counter also exists on token or latency series. The generated full dashboard validates its
queries against the source catalogue for this reason.

OTLP-to-Prometheus translation replaces dots with underscores and adds conventional counter and unit
suffixes. Query the backend's translated series names, not the dotted OTel names from source.

### Metric attribute boundaries

`codexlb.family` is present on response and turn shapes and on every token counter and duration
histogram. Filter `codexlb.family = "probe"` when measuring user traffic. This is separate from the
record transport field, which is not a reliable way to identify synthetic health traffic.

The token counters and `gen_ai.client.token.usage` carry `gen_ai.request.reasoning.level`,
`codexlb.thread_source`, and `codexlb.api_key_name` where present. `codexlb.cost_usd` is a
Float64Counter in USD with the token-like dimensions, and is emitted only when enrichment supplied a
non-nil cost. An explicit zero cost is still a real value and is recorded. API-key names also appear
on response and turn counters. Response and turn metrics deliberately omit proxy status, proxy error
code, and proxy failure phase so those diagnostics do not multiply the response series.

The enrichment-related self-observability instruments are:

| Instrument | Shape | Meaning |
| --- | --- | --- |
| `codexlb.selfobs.enrich_lookups` | counter, `codexlb.selfobs.result` | `cache_hit`, `db_hit`, `miss`, `error`, or `disabled` |
| `codexlb.selfobs.enrich_lookup_duration` | seconds histogram | DB attempts only; cache hits have no lookup duration |
| `codexlb.selfobs.enrich_cache_entries` | gauge | Current bounded LRU entries |
| `codexlb.archive_drift_findings` | gauge, `codexlb.selfobs.severity` | Finding counts at `breaking`, `new`, or `info` severity |

### Response attributes from enrichment

The optional database join adds `cost_usd`, `api_key_id`, `api_key_name`, `proxy_status`,
`proxy_error_code`, `proxy_failure_phase`, `proxy.time_to_response_created`, and
`proxy.time_to_first_upstream_event`. The cost and timing values are numeric attributes on the
response span. API-key and proxy fields are response-scoped metadata and are not re-derived from
wire fields.

### September wire additions

The reducer preserves the following wire additions in structured turn metadata. Fields with a
registered attribute key are also available on the relevant spans:

| Wire source | Output keys |
| --- | --- |
| `client_metadata.x-codex-turn-metadata` | `codexlb.root_turn_id`, `codexlb.agent_name`, `codexlb.sandbox_mode`, `codexlb.window_number` |
| `response.prompt_cache_options` | `codexlb.prompt_cache.mode`, `codexlb.prompt_cache.ttl` (seconds; the turn JSON field is `prompt_cache_ttl_seconds`) |
| `response.prompt_cache_diagnostics` | `codexlb.prompt_cache.diagnostic` |
| `response.safety_buffering` | `safety_retry_model`, `safety_use_cases`, `safety_reasons`, and the `safety_buffering` marker |
| `response.usage.attribution.items.<id>` | Turn JSON totals `attribution_input_tokens`, `attribution_output_tokens`, `attribution_cached_tokens`, and `attribution_cache_write_tokens`; span attributes `codexlb.usage.attribution.input_tokens`, `codexlb.usage.attribution.output_tokens`, `codexlb.usage.attribution.cache_read_tokens`, and `codexlb.usage.attribution.cache_write_tokens`; item identifiers are not emitted |

No September wire addition is a metric dimension. The bounded metric shape remains within its
cardinality budget: the current corpus has 4,014 observed combinations against a 21,300-series
budget. The bounded candidates (`sandbox_mode`, prompt-cache mode, and prompt-cache diagnostic) were
considered and deliberately left out of metric dimensions; identity fields such as root turn,
turn-metadata agent, window number, cache TTL, and the identifier-free attribution totals remain
metadata by contract. Attribution item identifiers are discarded.
Use Loki metadata or span attributes for these per-turn details.

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

The response span carries the response-scoped enrichment attributes and proxy timings. The turn,
phase, and tool spans do not repeat those response-only diagnostics. This keeps a cost or proxy
measurement attached to the response it describes rather than making it look like a property of every
child span.

## Agent Observability generations

The optional generation sink exports conversation and generation records to Grafana Agent
Observability. It is additive: enabling it does not replace Tempo, and enabling Tempo alone does not
populate the generation store.

## Cardinality boundary

The attribute catalogue marks which fields may become metric dimensions or Loki labels. IDs and
content-shaped values stay out of those bounded sets. Startup validation rejects unsupported Loki
labels instead of creating an unbounded stream topology.
