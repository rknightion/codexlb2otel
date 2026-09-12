---
title: Operations
description: Checkpointing, health checks, retention, drift probes, upgrades, and CLI tools
tags:
  - Operations
  - Configuration
---

# Operations

## Readiness and self-observability

`/healthz` reports readiness and ingestion state on the configured health listener. The service
binary can perform the same probe for a container health check:

```sh
codexlb2otel -config /etc/codexlb2otel/config.yaml -healthcheck
```

Monitor the exporter's own metrics for ingest lag, archive reads, checkpoint age and size, reducer
state, pending sink work, rejected records, enrichment outcomes, and drift findings. A healthy process
with a stale archive is not healthy ingestion.

## Restarts and upgrades

Send `SIGTERM` and allow a clean exit so the latest checkpoint is saved. Preserve the checkpoint
across image changes. Starting without it replays archives and can duplicate Loki lines and metric
deltas.

The rolling `main` image tracks every main-branch publish. Use a semantic-version image tag when a
deployment should advance only during a controlled upgrade.

## Archive retention

Retention is off by default. If archives are merely a buffer, choose either duration-based
`archive.delete_after` or UTC-day-based `archive.retain_days`. Confirm backups and recovery needs
first: fully ingested does not mean recoverable from a telemetry backend.

`archive.state_retain` is independent of file retention and defaults to `168h`. It ages completed
reducer baselines by the newest archive event timestamp in each series. Open responses are exempt
wholesale, and old snapshots without a stored timestamp use load time. A deleted-file tombstone is
pruned after its UTC filename day is more than three days old. When a returning series has no retained
baseline, the next value is marked `BaselineReset` and is an upper bound, so exclude it from exact
cumulative token and engine-timing totals. A database-enriched response cost is a separate point
value and is not reconstructed from reducer state.

## Schema drift

Run a sampled drift probe for routine checks:

```sh
just probe-sampled
```

Run a full scan before updating `corpus.sig.json` or claiming a shape has disappeared:

```sh
just probe
just baseline
```

The baseline contains structure and safe enums, not conversation bodies.

`just test-corpus` is the opt-in full-corpus confidence gate. It runs the exhaustive probe and the
non-race corpus suite and is expected to take about 25–30 minutes. Routine `just test` and
`just check` remain corpus-free; concurrency coverage is provided by the race-enabled routine test
path.

The daemon can run the same comparison in-process with `probe.enabled`. It scans once immediately,
then at `probe.interval`; `probe.sampled` is suitable for routine detection but cannot prove absence
or update the baseline. A scan error retains the last successful severity counts and records the
current run error. Watch `codexlb.archive_drift_findings` by
`codexlb.selfobs.severity` (`breaking`, `new`, `info`); informational disappearance is not a page.

## Optional database enrichment

Postgres enrichment is disabled by default and must use an existing read-only role. It performs an
indexed lookup on `request_logs.request_id`, with a bounded LRU and a background `request_logs.id`
tail prefetch. The prefetch may match `archive_request_id` from its cache, but that value is never
used as a point-query key. `cache_hit`, `db_hit`, `miss`, `error`, and `disabled` outcomes are
visible in `codexlb.selfobs.enrich_lookups`; lookup duration covers DB attempts only. The joined row
also supplies nullable proxy waits (`latency_queue_ms`, `latency_response_create_gate_wait_ms`, and
`latency_bridge_queue_wait_ms`) and bounded upstream status, error-code, and transport fields.
Null means no observation; a stored zero remains an observed zero. On this websocket-only deployment,
the `queue` and `bridge_queue` wait kinds are structurally absent: codex-lb assigns them only on its
HTTP-bridge submit path, which the websocket request path does not use. The
`response_create_gate` wait is a separate observation and can still be present. Do not sum these
waits into an end-to-end latency total.

If the DSN, pool, or query is unavailable, enrichment is disabled or records an error while archive
tailing and the other sinks continue. The read-only role must already have `SELECT` on `request_logs`,
`api_keys`, `accounts`, `usage_history`, `additional_usage_history`, and `api_key_accounts`; this
service never creates roles or changes grants.

### Camden enrichment check

Camden runs enrichment enabled through its dedicated `codexlb2otel_ro` role. That role is read-only
and has `SELECT` on `request_logs`, `api_keys`, `accounts`, `usage_history`,
`additional_usage_history`, and `api_key_accounts`; its secret connection details stay in the
deployment environment. When `db_hit` stops, inspect the `codexlb.selfobs.enrich_lookups`
counter by `codexlb.selfobs.result` and distinguish `disabled`, `error`, and `miss` before changing
anything. Check the lookup-duration histogram and the service logs for timeout or query errors, then
verify that the response id still matches `request_logs.request_id` and that the role retains its
six table grants. A `miss` can be a missing request row, while `error` indicates the database operation
failed. Cache hits have no lookup-duration sample. The archive tail and other sinks should continue
while this is investigated; use the cost counter as a regression check when cost data was expected.

## Optional account poller

`account_poller` is a separate, disabled-by-default database reader for account health and quota
gauges. It does not depend on archive traffic, so it can report an account that has served no
requests. Enable it only with the same existing read-only DSN and role:

```yaml
account_poller:
  enabled: true
  dsn: "${CODEXLB2OTEL_POSTGRES_DSN}"
  interval: 2m
  query_timeout: 2s
```

At each interval it reads account status, routing policy, quota windows, model quota windows, credit
balance, and key eligibility from five account tables, then publishes an immutable snapshot for
the metric callbacks. Those callbacks never query the database. An invalid DSN or a polling failure
disables or degrades this optional signal only; archive tailing and the other sinks continue.

## Camden deployment

Release automation publishes the image to GHCR. Camden consumes that image from its dedicated Compose
project at `/opt/compose/codexlb2otel/compose.yml`, with `/opt/codexlb2otel/config.yaml` as the mounted
configuration and `/opt/compose/codexlb2otel/.env` as the secret source. The healthcheck uses the service
binary and its `/healthz` endpoint, not a separate shell or HTTP client.

Keep `postgres.enabled` false unless an existing read-only DSN is available. If that DSN targets a
database on the host, include this mapping in the service stanza:

```yaml
extra_hosts:
  - "host.docker.internal:host-gateway"
```

Probe may be enabled on the real archive. Camden's settled configuration has Postgres enrichment
enabled through `codexlb2otel_ro`, while Agent Observability and traces remain disabled permanently.
Native per-profile Codex integration owns Agent Observability; do not enable either sink as part of
this deployment. After each rollout, check container health and then verify the enabled Grafana
signals separately: self-observability health, metric series, and Loki records. Trace-link behavior
is source-level only in this deployment, so a healthy process or HTTP response cannot serve as live
Tempo or generation evidence.

## Investigation tools

Build all tools with `just build`:

- `clbfind` reconstructs one response or an entire thread;
- `clbsum` summarizes selected sessions, with explicit third-party data handling;
- `clbprobe` detects archive schema drift;
- `clbprofile` produces a full induced schema from a capture; and
- `clbstat` surveys event coverage.

Captured archives and profile output can contain personal data. Keep them outside Git and delete
temporary copies when the investigation is complete.
