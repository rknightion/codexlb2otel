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
state, pending sink work, and rejected records. A healthy process with a stale archive is not healthy
ingestion.

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

## Investigation tools

Build all tools with `just build`:

- `clbfind` reconstructs one response or an entire thread;
- `clbsum` summarizes selected sessions, with explicit third-party data handling;
- `clbprobe` detects archive schema drift;
- `clbprofile` produces a full induced schema from a capture; and
- `clbstat` surveys event coverage.

Captured archives and profile output can contain personal data. Keep them outside Git and delete
temporary copies when the investigation is complete.
