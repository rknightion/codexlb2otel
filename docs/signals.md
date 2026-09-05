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
histogram. Exclude `codexlb.family = "probe"`, for example with `codexlb.family != "probe"`, when
measuring user traffic. Dashboard user-traffic queries apply the family selector, whose default is
all non-probe values. This is separate from the record transport field, which is not a reliable way
to identify synthetic health traffic.

The primary operation, turn, and TTFT histograms retain model and their contract-specific cohort
labels. Lower-level critical-path, engine-timing, response-subtraction, and TBT histograms retain
only family, plus critical-path coverage where applicable. This keeps their bucket expansion inside
the measured active-series budget. Agent Observability histograms retain stable
`gen_ai.agent.name`; the instructions-hash `gen_ai.agent.version` remains on response records and
spans rather than multiplying histogram buckets.

`gen_ai.client.token.usage` is a `{token}` histogram with `gen_ai.provider.name`,
`gen_ai.operation.name`, and `gen_ai.token.type` dimensions. The operation is `streamText` when the
response delivered text deltas and `generateText` otherwise. Its five token types are `input`,
`output`, `reasoning`, `cache_read`, and `cache_write`; the last three are breakdowns, so they must
not be added to their `input` or `output` parents. The histogram also carries the stable Agent
Observability name and inclusive-token semantics that its consumer requires.

`codexlb.tokens` is the project-defined `{token}` counter for summing totals, not a second histogram.
It records the same five values by token type but deliberately omits the Agent Observability labels.
Both token instruments carry `codexlb.family`, `gen_ai.request.reasoning.level`,
`codexlb.thread_source`, and `codexlb.api_key_name` where present. `codexlb.cost_usd` is a
Float64Counter in USD with the token-like dimensions, and is emitted only when enrichment supplied a
non-nil cost. An explicit zero cost is still a real value and is recorded. API-key names also appear
on response and turn counters. Response and turn metrics deliberately omit proxy status, proxy error
code, and proxy failure phase, so those diagnostics do not multiply the response series.

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

The three proxy wait columns are nullable milliseconds in the turn JSON. They preserve the
difference between an observed zero and no observation: a non-null zero is emitted as `0`, while a
null value is omitted. Their anchors are deliberately different:

| Turn field | Proxy measurement |
| --- | --- |
| `proxy_queue_wait_ms` | Account selection and admission, including failed failover attempts; outside the successful attempt's latency anchor. |
| `proxy_response_create_gate_wait_ms` | Wait inside the HTTP bridge's response-create gate. |
| `proxy_bridge_queue_wait_ms` | Wait inside the HTTP bridge queue. |

The proxy wait histogram is `codexlb.proxy.wait` (Prometheus
`codexlb_proxy_wait_seconds`) in seconds. It records the three kinds separately with explicit
buckets `0.001, 0.002, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5`. Its exact cohort is
`codexlb.proxy.wait_kind`, `codexlb.family`, `codexlb.request_kind`, `gen_ai.request.model`, and
`codexlb.thread_source`. `codexlb.proxy.wait_coverage` (Prometheus
`codexlb_proxy_wait_coverage_total`) is a `{observation}` counter with one `present` or `absent`
observation per kind and response; its exact attributes are `codexlb.proxy.wait_kind`,
`codexlb.selfobs.result`, and `codexlb.family`. A present zero enters the histogram, while an
absent value enters coverage only. None of these waits is summed into an end-to-end duration.

The upstream diagnostics `upstream_status_code`, `upstream_error_code`, and `upstream_transport`
are copied into the turn JSON body and response span attributes as
`codexlb.upstream.status_code`, `codexlb.upstream.error_code`, and `codexlb.upstream.transport`.
They are content-only metadata with caps of 16, 32, and 8 values respectively, and are never
metric dimensions or Loki labels. Freeform database or error bodies, including failure detail,
error messages, client addresses, and endpoint identifiers, remain out of this diagnostic set.

### Content capture, ordering, and provenance

The reducer emits `Prompts`, `Messages`, `ToolCalls`, `ToolOutputs`, and `AgentMessages` in the
same reduced turn. Every item carries `ordinal`, `item_id`, `captured_at`, and `provenance`:

| Field | Meaning |
| --- | --- |
| `ordinal` | One monotonic archive-capture sequence across all five content arrays. |
| `item_id` | The wire item id, when the item had one. |
| `captured_at` | The archive frame timestamp at which the item was observed. It is not authorship time or execution start. |
| `provenance` | `live` for response output items, `replayed` for input history recovered from `response.create`, or `unknown` where the source cannot be classified. |

Function-call arguments remain JSON after structural handling. A string value is replaced with
`"[omitted: encrypted]"` when it is below a key whose name contains `encrypted` (case-insensitive)
or when it has the URL-safe-base64 Fernet shape: version byte `0x80` and at least 73 decoded bytes.
The detector recurses through maps and arrays, including a root string. Non-string leaves remain
unchanged; a map or array below an encrypted-named key keeps its structure while string descendants
in that context are replaced. `input_omitted` counts replacements. `input_chars` is the original
argument byte length, and `input_truncated` marks the existing capture bound. The reducer has no
input-specific option, so this bound uses `MaxToolOutputChars`, default 4096; the summary package's
`MaxCharsPerToolInput` is a later rendering limit. Redaction happens before bounding. If an input
cannot be parsed as JSON, it is retained as a JSON string, and bound fallbacks remain valid JSON.
Custom tool calls keep their existing behavior. Function-call extraction populates the `spawn_agent`
task, model, and effort fields when those arguments are present. Tool-output `chars` likewise remains
the original untruncated length.

Loki retains the established event timestamp choices. Input-side prompts, instructions, tool
outputs, and agent messages use `inputTS`; model-side messages and tool calls use `outputTS`. When
two records have the same timestamp, Loki records sort by `ordinal`. `captured_at` remains the
archive observation timestamp in structured metadata and does not change those event timestamps.

### Tool-result origins

The reducer keeps a per-thread index from `(call_id)` to the originating response, turn, and tool
name. A single match sets `origin_response_id`, `origin_turn_id`, `origin_tool_name`, and
`origin_match: "exact"`; more than one open call with the same id is `"ambiguous"`; no match is
`"none"`. An exact match is consumed, so duplicate outputs cannot match the same call again. The
index is persisted in checkpoint state version 5, bounded to 512 entries per thread and 24 hours
on the archive clock, and pruned on insert. It never searches another thread. Replay uses the
existing deduplication set, so replayed history neither re-inserts calls nor consumes an origin
twice.

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

The following queries use synthetic placeholders; replace values in angle brackets before running
them. The first starts with a result and exposes its origin classification and ids:

```logql
{service_name="codexlb2otel", codexlb_record_type="tool_output"}
  | json call_id="call_id", origin_response_id="origin_response_id", origin_turn_id="origin_turn_id", origin_tool_name="origin_tool_name", origin_match="origin_match"
  | origin_match="exact"
```

Use the returned `origin_response_id` or `origin_turn_id` to find the originating turn, and the
`call_id` to find the originating invocation:

```logql
{service_name="codexlb2otel", codexlb_record_type="turn"}
  | json response_id="response_id", turn_id="turn_id"
  | response_id="<origin-response-id>"

{service_name="codexlb2otel", codexlb_record_type="tool_call"}
  | json call_id="call_id", name="name"
  | call_id="<call-id>"
```

For a per-response comparison of proxy waits with model-side timing, extract both families from
the turn record. Nullable proxy fields are empty when no database observation exists:

```logql
{service_name="codexlb2otel", codexlb_record_type="turn"}
  | json response_id="response_id", proxy_queue_wait_ms="proxy_queue_wait_ms", proxy_response_create_gate_wait_ms="proxy_response_create_gate_wait_ms", proxy_bridge_queue_wait_ms="proxy_bridge_queue_wait_ms", engine_queue_max_ms="engine_queue_max_ms", pre_inference_ms="pre_inference_ms", turn_time_seconds_delta="turn_time_seconds_delta"
  | response_id!=""
```

The upstream diagnostic and proxy-versus-upstream mismatch views keep the proxy fields beside the
wire-derived status. The first query can inspect status and transport. The second derives a bounded
`code_match` value and filters to differing proxy and upstream error codes:

```logql
{service_name="codexlb2otel", codexlb_record_type="turn"}
  | json response_id="response_id", upstream_status_code="upstream_status_code", upstream_error_code="upstream_error_code", upstream_transport="upstream_transport"
  | upstream_error_code!=""

{service_name="codexlb2otel", codexlb_record_type="turn"}
  | json response_id="response_id", proxy_error_code="proxy_error_code", upstream_error_code="upstream_error_code", upstream_status_code="upstream_status_code"
  | proxy_error_code!=""
  | upstream_error_code!=""
  | label_format code_match="{{if eq .proxy_error_code .upstream_error_code}}match{{else}}mismatch{{end}}"
  | code_match="mismatch"
```

The first diagnostic query can omit its final filter to inspect status and transport rows as well.
The second returns only differing bounded codes without promoting either code to an unbounded Loki
label.

## Traces

Tempo receives a trace tree spanning the response and its engine and tool work. Trace attributes use
OpenTelemetry GenAI conventions where they match the actual wire semantics, plus `codexlb.*`
attributes for capture-specific details.

Tracing can be disabled or head-sampled independently from metrics. Sampling reduces trace volume but
does not change Loki or metric output.

The response span carries the response-scoped enrichment attributes, proxy timings, and any
non-null proxy waits. The turn, phase, and tool spans do not repeat those response-only diagnostics.
This keeps a cost or proxy measurement attached to the response it describes rather than making it
look like a property of every child span.

A tool result recovered on a later response becomes a `tool_result` child of the receiving response
span. It has a span link to the deterministic origin tool-call span, plus
`codexlb.tool.origin_response_id` and `codexlb.tool.origin_match`; ended spans are never amended.
This cross-response link is proven at source level in synthetic exporter tests. Camden keeps both
Tempo traces and Agent Observability generations disabled permanently while native per-profile
Codex integration owns that observation path, so it has no live-signal claim.

## Agent Observability generations

The optional generation sink exports conversation and generation records to Grafana Agent
Observability. It is additive: enabling it does not replace Tempo, and enabling Tempo alone does not
populate the generation store. The current Camden deployment leaves this sink off permanently;
native per-profile Codex integration owns Agent Observability there.

## Cardinality boundary

The attribute catalogue marks which fields may become metric dimensions or Loki labels. IDs,
content-shaped values, proxy waits, upstream diagnostics, and origin ids stay out of those bounded
sets. Startup validation rejects unsupported Loki labels instead of creating an unbounded stream
topology.

## Deliberate omissions

`response.access_programs.cyber` is not emitted in this wave. The frozen `Turn` contract has no
field for it, so adopting it would require a shared-type change outside this lane. Tool namespaces
remain deferred under the existing tool-name cap decision.
