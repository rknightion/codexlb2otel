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

The Latency & Critical Path tab shows proxy queue, response-create gate, and bridge queue waits as
separate p50/p95 distributions, plus present/absent coverage by wait kind. It also compares proxy-wait
p95 with total model response-duration p95 for the shared model and family cohort. Queue wait covers
account selection, admission, and failed failover attempts outside the successful attempt's latency
anchor; response-create gate and bridge queue waits are inside the HTTP bridge. The two measurements have
different anchors, so the dashboard never adds them into an end-to-end total.

The Errors & Transport tab reads `upstream_status_code` and `upstream_error_code` from turn JSON in
Loki, where it shows their distributions and a table of proxy/upstream error-code mismatches. The
Conversation Logs tab adds a tool-call input-presence ratio by kind, tool-result origin matches
(`exact`, `ambiguous`, and `none`), an ordinal ordering example, and a lookup panel driven by the
`$upstream_error_code` text variable. For example, the lookup panel uses:

```logql
{service_name="codexlb2otel", codexlb_record_type="turn"}
| json upstream_error_code="upstream_error_code"
| upstream_error_code != ""
| upstream_error_code="$upstream_error_code"
```

The ordering example is an instant Loki metric query using `sort` over parsed ordinal samples. Set the
Lookup ID to a response ID so the query stays within one Turn: ordinals restart for each Turn. Current
LogQL sorts metric query results by sample value; it does not reorder raw log rows by a parsed field.
The record panels therefore retain the `ordinal` structured metadata so the capture order can be
checked while inspecting the original lines.

`gen_ai.provider.name`, `gen_ai.operation.name`, and raw request-log columns are deliberately omitted
because they are constant, unasked, or already shown by codex-lb's dashboard. Upstream diagnostics
remain Loki fields only and never become metric dimensions. Freeform database error bodies and
identifiers are excluded, and the three proxy waits remain separate observations with no composite
end-to-end wait.

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
