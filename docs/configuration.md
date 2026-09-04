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

`archive.state_retain` defaults to `168h` and bounds completed reducer state separately from archive
file retention. The age is measured from the newest archive event timestamp in each series, not from
the host clock. A series with an open response is kept as a whole. Snapshots written before this
timestamp was persisted use load time as their first anchor. Set it to `0` to disable state eviction.
When an evicted series returns, its next cumulative reading is marked `BaselineReset` and is an upper
bound because work may have happened while its baseline was absent.

Archive deletion is disabled by default. `archive.delete_after` applies a duration and
`archive.retain_days` applies UTC calendar-day retention. Reclamation never removes the newest file
or a file that has not been fully read. The two policies are independent OR rules: an eligible file
is deleted when either configured policy selects it. Enabling both broadens reclamation, so confirm
that the archive is a disposable buffer and choose both thresholds deliberately.

Deleted-file tombstones are retained briefly so a restarted tailer does not mistake a reclaimed
filename for a new generation. Tombstones whose UTC filename day is more than three days old are
pruned.

## Optional Postgres enrichment

Enrichment is disabled by default and is independent of archive delivery. Enable it only when an
existing read-only DSN is available:

```yaml
postgres:
  enabled: true
  dsn: "${CODEXLB2OTEL_POSTGRES_DSN}"
  lookup_timeout: 2s
  prefetch_interval: 5s
  cache_entries: 50000
```

The service uses pgx/v5. After a cache miss, the response id drives an indexed point lookup on
`request_logs.request_id`. A background prefetch follows
the monotonically increasing `request_logs.id` tail and stores rows under both `request_id` and
`archive_request_id` when the latter is present. `archive_request_id` is a cache alias from
prefetched rows, never a point-query key. The in-process LRU is bounded by `cache_entries`.

Enrichment supplies `cost_usd`, `api_key_id`, `api_key_name`, `proxy_status`, `proxy_error_code`,
`proxy_failure_phase`, `proxy.time_to_response_created`, and
`proxy.time_to_first_upstream_event`. Cost and proxy timings remain numeric response-span
attributes; the API-key and proxy status fields are response-scoped metadata and span attributes.
Lookup outcomes are
`cache_hit`, `db_hit`, `miss`, `error`, and `disabled`.

The database role must already exist and have `SELECT` on `request_logs`, `api_keys`, and
`accounts`. The service does not create roles, change grants, or write to the database. Invalid
bounds, an empty or unresolved DSN secret, or a malformed DSN that prevents pool construction
disables enrichment at startup. A syntactically valid DSN whose database is unreachable can still
construct the lazy pool; the service starts, enrichment lookups and prefetches report errors until
the database is reachable, and the archive, Loki, metrics, traces, and other enabled sinks continue
independently. Lookup timeouts and query errors lose only the affected enrichment result.

## In-process drift probe

The optional `probe` section runs the archive profile comparison inside the daemon, against its
embedded content-free baseline:

```yaml
probe:
  enabled: true
  interval: 24h
  sampled: true
```

An enabled probe runs once during startup and then at the configured interval. Sampled scans are
appropriate for routine detection but cannot prove that a rare shape is absent and never update the
baseline. Only a full scan can support a baseline update or an absence claim. Scan errors preserve
the last successful finding counts while updating the run and error state.

The finding gauge is `codexlb.archive_drift_findings`, labeled by
`codexlb.selfobs.severity` with `breaking`, `new`, or `info`. Informational disappearance findings
are not failures and must not page by themselves.

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
Camden keeps traces disabled until the token's trace scope is proven. Do not treat the checked-in
example's trace setting as the deployed Camden setting.

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

## Camden deployment contract

Release automation publishes the runtime image to GHCR. Camden runs that published image as its own
Compose project from `/opt/compose/codexlb2otel/compose.yml`, with the mounted deployment configuration at
`/opt/codexlb2otel/config.yaml` and secrets in `/opt/compose/codexlb2otel/.env`. Keep those host files out of
the repository. The compose healthcheck invokes the service binary with `-healthcheck`, which checks
`/healthz` using the same mounted configuration.

Leave `postgres.enabled: false` unless `/opt/compose/codexlb2otel/.env` can supply an existing read-only DSN.
Do not create a database role or document credentials as part of deployment. If the DSN reaches a
database on the Camden host, the service stanza needs this host mapping:

```yaml
extra_hosts:
  - "host.docker.internal:host-gateway"
```

Probe may be enabled against Camden's real archive. Agent Observability and traces remain disabled
until their token scopes are proven. After a rollout, verify container health and then verify the
enabled Grafana signals separately: ingest lag and rejection health, metrics series, Loki records,
and traces or generations only when their switches and credentials are intentionally enabled. A
healthy container or HTTP success alone is not evidence that a signal reached its backend.
