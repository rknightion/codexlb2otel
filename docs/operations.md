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
visible in `codexlb.selfobs.enrich_lookups`; lookup duration covers DB attempts only.

If the DSN, pool, or query is unavailable, enrichment is disabled or records an error while archive
tailing and the other sinks continue. The read-only role must already have `SELECT` on `request_logs`,
`api_keys`, and `accounts`; this service never creates roles or changes grants.

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

Probe may be enabled on the real archive. Camden keeps Agent Observability and traces disabled while
their token scopes remain unproven. After each rollout, check container health and then verify the
enabled Grafana signals separately: self-observability health, metric series, Loki records, and any
intentionally enabled Tempo or generation data. A successful healthcheck or HTTP response is not
proof of downstream signal delivery.

## Investigation tools

Build all tools with `just build`:

- `clbfind` reconstructs one response or an entire thread;
- `clbsum` summarizes selected sessions, with explicit third-party data handling;
- `clbprobe` detects archive schema drift;
- `clbprofile` produces a full induced schema from a capture; and
- `clbstat` surveys event coverage.

Captured archives and profile output can contain personal data. Keep them outside Git and delete
temporary copies when the investigation is complete.
