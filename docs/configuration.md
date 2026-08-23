---
title: Configuration
description: Configure archive ingestion, telemetry sinks, health, live sessions, summarization, and logging
tags:
  - Configuration
  - OpenTelemetry
---

# Configuration

The service loads defaults first and overlays YAML from `-config`. The annotated
[`config.example.yaml`](https://github.com/rknightion/codexlb2otel/blob/main/config.example.yaml)
is the complete reference.

## Secret values

Keep credentials out of YAML and Git. Every secret field accepts either form:

```yaml
token: "${CODEXLB2OTEL_OTLP_TOKEN}"
token: "file:/run/secrets/otlp-token"
```

An unset environment variable, unreadable file, or empty resolved value is a startup error. Secret
fields are redacted when formatted or returned by the health endpoint.

## Minimal structure

```yaml
service:
  name: codexlb2otel
  environment: lab

archive:
  dir: /srv/codex-lb/conversation-archive
  checkpoint: /var/lib/codexlb2otel/checkpoint.json
  poll_interval: 5s
  checkpoint_interval: 15m

loki:
  enabled: false

otlp:
  endpoint: https://your-otlp-gateway.example/otlp
  instance_id: "123456"
  token: "${CODEXLB2OTEL_OTLP_TOKEN}"
  metrics:
    enabled: true
    interval: 30s
  traces:
    enabled: false
    sample_ratio: 1
```

## Archive and checkpoints

`archive.checkpoint` persists compressed byte offsets and reducer state so restarts resume without
re-exporting the archive. Put it on durable writable storage. A clean shutdown always saves; an
unclean stop can replay up to `checkpoint_interval` of work.

Archive deletion is disabled by default. `archive.delete_after` applies a duration and
`archive.retain_days` applies UTC calendar-day retention. Reclamation never removes the newest file
or a file that has not been fully read, but deletion remains irreversible—enable only one policy
after confirming that the archive is a disposable buffer.

## Loki

Loki uses its native push API so bounded stream labels and structured metadata remain distinct.
`loki.url` must include `/loki/api/v1/push`. `loki.labels` is validated against the bounded
attribute catalogue at startup.

`loki.record_types` controls content exposure. An empty list exports all record kinds. A structural
timeline can use:

```yaml
loki:
  record_types: [turn, transport, error]
```

This omits prompt, message, tool-call, tool-output, agent-message, and instructions records.

## OTLP metrics and traces

One OTLP endpoint and credential serve both signals. Metrics and traces have independent `enabled`
switches. `metrics.interval` controls periodic export; `traces.sample_ratio` controls head sampling.

## Health and live view

The health service defaults to `127.0.0.1:9464`. The live view is separate, disabled by default,
and defaults to `127.0.0.1:9465`.

```yaml
live:
  enabled: true
  listen: 127.0.0.1:9465
  content: false
```

`content: false` retains models, tool names, relationships, timings, and token counts without prose.
A non-loopback listener requires `live.token` unless `live.allow_insecure` explicitly acknowledges
unauthenticated exposure.

## Optional generation and summary sinks

`agento11y` exports Grafana Agent Observability generations and is additive to Tempo traces.
`summarize` configures `clbsum`, which sends selected conversation content through OpenRouter.
Both are disabled by default and need separate credentials; `summarize` defaults to zero-data-
retention routing and denied provider data collection.
