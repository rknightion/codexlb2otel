---
title: Getting started
description: Build codexlb2otel, run a safe local smoke test, and prepare a container deployment
tags:
  - Configuration
  - Operations
---

# Getting started

`codexlb2otel` must be able to read a `codex-lb` conversation-archive directory and write its
checkpoint. Start with output disabled or pointed at test destinations; archive records can contain
complete prompts, messages, tool arguments, and command output.

## Build from source

The Go version is declared in `go.mod`:

```sh
git clone https://github.com/rknightion/codexlb2otel.git
cd codexlb2otel
just setup
just build
```

Copy the example configuration and change the archive paths:

```sh
cp config.example.yaml config.yaml
```

For the first run, disable `loki`, `otlp.metrics`, `otlp.traces`, and `agento11y`. The health
listener remains available on loopback:

```sh
./bin/codexlb2otel -config config.yaml
curl --fail http://127.0.0.1:9464/healthz
```

The binary also provides its own container health probe:

```sh
./bin/codexlb2otel -config config.yaml -healthcheck
```

## Run the published container

Tagged releases and the rolling `main` image are published to GHCR:

```sh
docker pull ghcr.io/rknightion/codexlb2otel:main
```

The image is distroless and runs as a non-root user. Mount:

- the archive directory at the path configured by `archive.dir`;
- a readable configuration file at `/etc/codexlb2otel/config.yaml`; and
- a writable persistent directory containing `archive.checkpoint`.

The repository's
[`docker-compose.yml`](https://github.com/rknightion/codexlb2otel/blob/main/docker-compose.yml)
shows the complete mount and health-check shape. Its host paths and numeric user are deployment
examples, not portable defaults: choose paths and a UID that can read your own archive without
loosening the archive's permissions.

## Enable one sink at a time

1. Enable OTLP metrics and confirm series arrive.
2. Enable traces if the additional volume is useful.
3. Review [Security](security.md), choose `loki.record_types`, then enable Loki.
4. Enable Agent Observability only with a token carrying the required generation-write scope.

See [Configuration](configuration.md) for secret indirection and [Troubleshooting](troubleshooting.md)
for empty-output and delivery failures.

For the host-specific Compose layout, optional Postgres join, drift probe, and post-deploy Grafana
verification, follow the [Camden deployment contract](operations.md#camden-deployment). The published
configuration does not imply that Camden has traces or Agent Observability enabled.
