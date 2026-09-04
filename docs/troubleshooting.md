---
title: Troubleshooting
description: Diagnose empty archives, missing telemetry, checkpoint failures, Loki rejection, and live-view access
tags:
  - Operations
  - Configuration
---

# Troubleshooting

## The process is healthy but no turns appear

Check `archive.dir` inside the process or container, not only on the host. The service identity needs
directory traversal and read permission on the archive files. A bind mount can exist while exposing
an empty or unreadable path.

Confirm that `codex-lb` is writing complete gzip members and inspect ingest-lag and archive-read
self-observability metrics. The poll interval bounds normal visibility delay.

## Startup fails while loading a token

`${NAME}` requires a non-empty environment variable in the service process. Docker Compose variable
substitution and `env_file` are different mechanisms; verify the variable reaches the container.

For `file:PATH`, confirm the service UID can read the mounted file and that trailing whitespace is
the only content being trimmed.

## Checkpoint writes fail

The parent directory must exist and be writable by the runtime UID. Persist a directory rather than
mounting an absent file over the checkpoint path. Do not solve an ownership mismatch by making the
conversation archive broadly readable.

If a checkpoint is lost, expect replay. Loki can receive duplicate lines, and cumulative metric
state can produce a noisy cold start.

## Metrics or traces do not arrive

Confirm the relevant `otlp.metrics.enabled` or `otlp.traces.enabled` switch is true. Check that the
endpoint is the OTLP gateway base expected by the backend and that `instance_id` is the correct basic-
auth username for that endpoint.

Use sink pending, rejection, and export-failure metrics plus process logs. A blank backend dashboard
does not distinguish disabled export from rejected delivery.

If only enrichment data is absent, inspect `codexlb.selfobs.enrich_lookups` and its
`codexlb.selfobs.result` values. `disabled` means the feature has no usable DSN or was not enabled;
`miss` means no matching request row; `error` means a point lookup failed or timed out. Cache hits
do not produce a lookup-duration sample. These conditions do not stop archive ingestion or the other
sinks. Confirm that the existing database role has `SELECT` on `request_logs`, `api_keys`, and
`accounts`, and that the response id matches `request_logs.request_id`; `archive_request_id` is only
used as a prefetch cache alias.

## Loki accepts pushes but records are missing

Check `loki.record_types`, `max_line_age`, and the backend's query time range. Old lines can be dropped
locally to avoid a backend accepting the HTTP request while discarding over-age samples. Oversized
lines are truncated or rejected according to the configured local budget before they can block
delivery.

Only catalogue-approved bounded fields may be used in `loki.labels`; an invalid label fails startup.

## Agent Observability returns 401 or 403

The generation endpoint is separate from the generic OTLP gateway and needs a token with the product's
generation-write permission. Verify the full `.../api/v1/generations:export` URL, instance username,
and token scope.

Camden keeps this sink disabled until its token scope is proven. The same rule applies to OTLP
traces there. Do not interpret a healthy container as proof that either signal is enabled or
accepted.

## The live view is unreachable

The live and health servers use different ports. Confirm `live.enabled`, `live.listen`, and any
container port mapping. On a non-loopback bind, supply the token as `Authorization: Bearer ...` or
the `token` query parameter. Validation refuses an unauthenticated non-loopback listener unless
`allow_insecure` is explicit.

## A drift probe reports new or breaking shapes

Do not update the baseline from a sampled run. Reproduce with a full scan, inspect the decoder and
reducer impact, then update `corpus.sig.json` only after the new shape is supported or deliberately
accepted.

The in-process probe runs immediately when enabled and then at its configured interval. It compares
with the embedded baseline and never updates it. On a scan error, the last successful finding counts
remain visible while the run error is recorded. `info` findings describe possible disappearance and
are not proof of absence from a sampled scan. Check `codexlb.archive_drift_findings` with
`codexlb.selfobs.severity` to separate `breaking`, `new`, and `info` findings.

## State has been evicted or totals look high after a gap

`archive.state_retain` ages completed reducer baselines using archive event timestamps. Open
responses are exempt, but a series that returns after eviction is marked `BaselineReset`; its
cumulative reading is an upper bound because earlier activity may be missing. Exclude reset samples
from exact aggregate totals and let the series establish a new baseline.

If a reclaimed filename appears to be new, check the checkpoint's deleted-file tombstones. Tombstones
are intentionally pruned once their UTC filename day is more than three days old; replay after that
boundary cannot use the old path identity.

## Camden deployment is healthy but Grafana is empty

The release pipeline publishes the GHCR image, while Camden runs the separate project at
`/opt/compose/codexlb2otel/compose.yml` with `/opt/codexlb2otel/config.yaml` and `/opt/compose/codexlb2otel/.env`.
Verify those mounted files, the archive path, and the binary `/healthz` healthcheck first. Then
verify each enabled Grafana signal separately. If Postgres is enabled, an archive-only deployment
can still be healthy when enrichment is disabled; if the DSN uses the host database, confirm the
Compose service has `host.docker.internal:host-gateway` in `extra_hosts`.
