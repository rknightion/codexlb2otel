---
title: Dashboards
description: Use and validate the full-telemetry Grafana dashboard supplied with codexlb2otel
tags:
  - Grafana
  - Loki
  - Tempo
---

# Dashboards

The supported dashboard is
[`dashboards/v2/codexlb2otel-full.json`](https://github.com/rknightion/codexlb2otel/blob/main/dashboards/v2/codexlb2otel-full.json).
It is a Grafana dashboard-schema v2 document with tabs for the complete metric, Loki-record, and
trace inventory.

## Regenerate it

The JSON is generated, not hand-edited:

```sh
python3 dashboards/v2/generate.py > dashboards/v2/codexlb2otel-full.json
```

The generator reconciles its panels against the metric constants, emitted Loki record types, and
span names. It exits non-zero and names missing coverage rather than producing a dashboard that
silently ignores a new signal.

The twelve tabs include enriched cost and token shape, status disagreement diagnostics, agent
topology, ID lookup, and trace views. The `$family` selector defaults to websocket, HTTP, and
unknown traffic, excluding probes from cost, token, and latency views unless probes are selected.
`gen_ai.provider.name`, `gen_ai.operation.name`, and raw request-log columns are deliberately
omitted because they are constant, unasked, or already shown by codex-lb's dashboard.

If metric constants changed, regenerate the source sidecar first using the command documented in
[`dashboards/README.md`](https://github.com/rknightion/codexlb2otel/blob/main/dashboards/README.md).
The check recipe detects a stale sidecar or generated JSON and validates every v2 metric and label
reference against the checked-out source.

## Datasources

The checked-in dashboard uses the datasource UIDs from the environment where it was developed. Before
importing into another Grafana stack, map the Prometheus/Mimir, Loki, and Tempo datasource references
to that stack's UIDs.

## Query semantics

Prometheus receives translated OTel names: dots become underscores, counters gain `_total`, and
units can add suffixes such as `_seconds`. Loki labels also replace dots with underscores but do not
apply metric suffix rules.

Some token and duration questions require LogQL because their metric instruments intentionally do not
carry every routing dimension. Panel descriptions record these boundaries. Treat a blank panel as a
query or deployment problem to investigate, not evidence that the exporter emitted zero.

## Legacy assets

The numbered classic dashboards and file-provisioned alert rules under `dashboards/` predate the
deployed full-telemetry dashboard. They remain useful query references, but the v2 dashboard is the
coverage-checked primary artifact.
