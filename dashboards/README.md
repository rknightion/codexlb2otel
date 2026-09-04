# codexlb2otel dashboards & alerts (issue #14)

Dashboards and alerts as code, built to complement the existing `codex-lb-overview` dashboard
(Postgres-derived, the proxy's own view) with what only the wire capture knows. **Nothing here is
provisioned.** These are files only - no `gcx` write call, no push to any Grafana instance, was made
building them.

## Files

| File | Covers |
|---|---|
| `01-rate-limits-accounts.json` | Per-account rate-limit headroom and credits - never averaged across accounts |
| `02-tokens-cost-shape.json` | Token/cost shape by model, effort, thread source |
| `03-turn-latency-critical-path.json` | End-to-end turn latency, critical-path decomposition, issue #23's engine_service_*/engine_iapi_* split |
| `04-agent-tree.json` | Spawn rate, subagent kind mix, agent-message topology |
| `05-tool-usage.json` | Tool usage by name, calls-per-response distribution |
| `06-errors-transport.json` | Error codes/types, websocket transport health (frame_type/close_code) |
| `07-pipeline-health.json` | Ingest lag, decode health, sink delivery health (issue #8's self-observability metrics) |
| `08-response-thread-lookup.json` | Issue #15's deferred dashboard: paste any of 4 ids, get metadata + timing + ordered conversation |
| `alerts/*.yaml` | Six alert rules, Grafana alerting file-provisioning format (`apiVersion: 1` / `groups`) |
| `scripts/check_names.py` | Programmatic cross-check: every metric/label/json-field name referenced above against the source code |

## Source of truth: code, not issues

Every metric and attribute name here was read out of `internal/attr/names.go` and
`internal/sink/otlpmetric/{instruments,record,selfobs}.go`, and every Loki JSON field path out of
`internal/turn/turn.go`'s own `json:` tags - never out of an issue's prose. This mattered in practice:
issue #8's self-observability metrics **landed in this repo's working tree, uncommitted, while this
dashboard set was being built** (a concurrent lane, per this task's own instructions). The pipeline
health dashboard was drafted once against placeholder names invented from issue #8's scope list, then
rebuilt against the real, now-landed names once `check_names.py` (also written mid-task) flagged the
mismatch. `dashboards/alerts/ingest-lag.yaml` and `loki-rejections.yaml` were corrected the same way.
See each file's own panel/rule descriptions for what changed and why - this is not restated here.

**Issue #13's drift-findings metric (`codexlb_archive_drift_findings{severity}`) is still a genuine
forward reference** as of this writing - confirmed absent from both files above. It is the one metric
in this entire set that does not exist in code yet; every panel/rule referencing it says so.

## The dot-to-underscore mangling rule

OTel attribute/metric names in this codebase are dotted (`codexlb.tokens`, `gen_ai.request.model`).
Both Prometheus (via Grafana Cloud's OTLP ingest) and Loki (via `attr.LokiKey`, applied once at push
time in `internal/sink/loki/push.go`'s `kvMap`) reject dots in identifiers, so both mangle dots to
underscores. That much is stated in the code itself (`LokiKey`'s own doc comment, and its historical
400 story). Two further rules are NOT stated in the code and were confirmed by directly querying the
live Mimir instance (`grafanacloud-prom`, `job="codexlb2otel"`, `gcx metrics series`, 2026-08-07),
because the OTel-to-Prometheus name-and-suffix translation is dictated by the exporter, not by this
codebase:

1. **Monotonic sums (Counters) get `_total` appended.** `codexlb.tokens` → `codexlb_tokens_total`.
   Confirmed live for `codexlb_responses_total`, `codexlb_turns_total`, `codexlb_tokens_total`,
   `codexlb_tool_calls_total`, `codexlb_engine_calls_total`, `codexlb_baseline_resets_total`,
   `codexlb_attributes_rejected_total`.
2. **Histograms and Gauges get their declared OTel unit's Prometheus suffix appended, then histograms
   split into `_bucket`/`_sum`/`_count`.** Unit table used here (the only units this codebase declares):
   `s`→`_seconds`, `%`→`_percent`, `By`→`_bytes`, `1`→`_ratio` **(gauges only** - verified against
   `opentelemetry-collector-contrib`'s `pkg/translator/prometheus` docs via context7: counters do not
   get `_ratio` even at unit `1`; none of this codebase's unit-`1` instruments are counters, so this
   never bites here, but the check script enforces it anyway). Bracket units (`{token}`, `{response}`,
   `{call}`, `{event}`, `{attribute}`, `{file}`, `{read}`, ...) get **no suffix at all**.
   Confirmed live for `codexlb_engine_wall_seconds_{bucket,sum,count}`,
   `codexlb_rate_limit_used_percent`, `codexlb_rate_limit_reset_after_seconds`,
   `codexlb_credits_unlimited_ratio`, `codexlb_credits_balance` (no unit, no suffix).
3. **A suffix is never doubled.** If the dotted name already ends in the unit word
   (`codexlb.selfobs.ingest_lag_seconds`, unit `s`), the mangled name stays as-is rather than
   becoming `..._seconds_seconds`. Not directly live-verified (issue #8's metrics postdate the query
   above), but matches the documented translator behavior and the precedent of
   `gen_ai_client_operation_duration_seconds_bucket` (also confirmed live, and also a name that
   doesn't accidentally double).

**One live discrepancy worth recording, not a rule violation**: `attr.MetricTTFT` has now been renamed
twice, so a deployed binary can be one of three names behind. It was `codexlb.time_to_first_token`,
then `gen_ai.server.time_to_first_token` (issue #18/#23, for GenAI-convention compliance), and since
issue #32 it is **`gen_ai.client.time_to_first_token`** (mangled:
`gen_ai_client_time_to_first_token_seconds_*`) - agent-observability's name rather than the registry's,
because its TTFT panels match that string literally and the spec-correct name had no consumer at all.
The dashboards here always use the **current code's** name; redeploying is what makes the live series
match.

**Loki labels and structured metadata use the SAME dot-to-underscore mangling, no suffix logic at
all** (Loki has no counter/histogram concept) - `attr.LokiKey`. Confirmed by reading `push.go`
directly: `kvMap` is the one place `LokiKey` is applied, to both the stream label map and the
structured-metadata map, unconditionally.

## Per-metric attribute matrix (why some filters are missing on purpose)

Every panel here was built by reading `record.go`'s `recordX` functions one at a time - which
`attr.Only(...)` / `attr.With(...)` call actually narrows each instrument's attribute set - never by
assuming every metric carries every attribute. Two gaps this surfaced, both flagged in the affected
panels' own descriptions rather than silently worked around:

- **No token/duration/tool-call instrument carries `codexlb.family`.** Only `codexlb.responses`,
  `codexlb.turns`, `codexlb.baseline_resets` and `codexlb.transport_events` do (`recordCounts`'s own
  `base`-derived attrs). So **probe-family traffic cannot be excluded from a Prometheus token-shape or
  latency panel at all** - the dimension simply isn't on the wire for those instruments. Where this
  mattered most (issue #14 names token/cost shape explicitly), `02-tokens-cost-shape.json`'s effort/
  thread-source panels are built against **Loki** instead, which does have `codexlb_family` as a
  stream label on every record type including `turn`.
- **No token-carrying instrument carries `codexlb.request.reasoning.level` (effort) or
  `codexlb.thread_source`.** `recordTokens`'s narrowed attribute set is exactly `(provider, operation,
  request_model, response_model, account_id, request_kind, agent_name, agent_version, token_type,
  token_semantics)` - the last four added by issue #32 - so effort and thread source are still absent.
  Issue #14 asks for "token
  and cost shape by model, effort and thread source", and effort/thread-source are not achievable via
  PromQL against `codexlb_tokens_total` at all. Solved the same way as the family gap: LogQL `unwrap`
  aggregation against the turn record's own JSON body, which does carry both alongside the token
  counts.

  **Read that as "no *token* instrument", not "effort is unavailable to PromQL".** `codexlb.responses`
  and `codexlb.turns` both carry `gen_ai.request.reasoning.level` alongside `gen_ai.request.model` -
  confirmed live against Mimir on 2026-08-17 (`/api/v1/series`, 3-day window: 731 and 616 series
  respectively, both label sets including `gen_ai_request_reasoning_level`). So a *request-count* panel
  by model and effort is plain PromQL and needs no Loki at all; that is what the **Model Usage** tab in
  `v2/generate.py` is built on. Only token and cost by effort have to go to LogQL. Routing a count
  panel through Loki because of the sentence above is the mistake this note exists to prevent.
- **`codexlb.baseline_reset` and `codexlb.critical_path.coverage` only apply to specific instruments** -
  see `03-turn-latency-critical-path.json`'s own top panel for the exact list. Applying either filter
  to an instrument that doesn't carry it wouldn't error; it would silently return zero data, which is
  the exact failure mode this task's brief warned about. Every panel was checked against `record.go`
  individually rather than copy-pasting a filter set across panels.
- **Subagent depth (chain length) is not built anywhere.** It needs recursive
  `parent_thread_id`/`forked_from_thread_id`/`parent_turn_id` traversal, which is graph work no
  PromQL or LogQL aggregation performs. `04-agent-tree.json` says so directly and ships a browsable
  proxy table instead of a fabricated metric.

## Dashboard format

Classic Grafana dashboard JSON model (`schemaVersion`, `panels[]`, `templating.list[]`) - **not** the
newer `dashboard.grafana.app/v2` `elements`/`layout` schema `codex-lb-overview` itself is stored in
(`gcx dashboards get codex-lb-overview`, read-only, confirmed `apiVersion: dashboard.grafana.app/v2`).
Judgment call: v2 is materially harder to hand-author correctly without a live iterate-and-snapshot
loop, which this task explicitly rules out. The classic model remains a valid provisioning input
(`gcx dashboards create -f <file> --api-version dashboard.grafana.app/v1` accepts it, and Grafana's
server-side upgrade path converts it) - reconcile/convert as a separate step before actually
provisioning, don't assume `gcx dashboards create` with no `--api-version` override necessarily wants
this shape.

Alerts use the Grafana alerting file-provisioning format (`apiVersion: 1` / `groups` / `rules`),
matching the `AlertRuleGroupExport`/`ProvisionedAlertRule` shapes documented at
`grafana.com/docs/.../provision-alerting-resources/http-api-provisioning` (the same shape
`GET /api/v1/provisioning/alert-rules/:uid/export?format=yaml` produces) - confirmed via context7,
not recalled from memory, since getting alert-rule YAML subtly wrong (a bad `noDataState` value, a
missing `datasourceUid`) is exactly the kind of thing that looks fine until it's evaluated.

Datasource uids used throughout, as given and read-only-confirmed (`gcx datasources list --context
m7kni`, 2026-08-07): `grafanacloud-prom` (Mimir), `grafanacloud-logs` (Loki). Hardcoded directly into
every panel/query rather than templated - matches how the environment facts were handed off for this
task.

## Running the cross-check

```
python3 dashboards/scripts/check_names.py
```

Parses `internal/attr/names.go`, `internal/sink/otlpmetric/{instruments,selfobs}.go` and
`internal/turn/turn.go` directly (no network, no Grafana access), computes every valid Prometheus wire
name and Loki label/metadata key per the mangling rule above, and greps every `expr` string in
`dashboards/*.json` and `dashboards/alerts/*.yaml` for `codexlb_*`/`gen_ai_*` identifiers and `| json
alias="field.path"` extractions, flagging anything that doesn't resolve. It does **not** talk to
Grafana/Prometheus/Loki and cannot prove a panel renders - it proves internal consistency between what
these files say and what the Go source actually emits, which is the thing that goes silently wrong
(a dashboard full of names that "look right" and return nothing) if this check doesn't exist.

Current result: **PASS** - every identifier resolves, except the deliberately-flagged issue #13
forward reference, which the script explicitly allowlists rather than silently passing.

## v2/ - the full-telemetry dashboard

`v2/codexlb2otel-full.json` is a single `dashboard.grafana.app/v2` dashboard with twelve tabs covering
**every** signal this exporter produces: all declared metrics, all 9 Loki record types, and the trace tree.
It is live on the m7kni stack in the `codexlb2otel` folder.

It is **generated**, never hand-edited:

```bash
python3 dashboards/v2/generate.py > dashboards/v2/codexlb2otel-full.json
gcx --context m7kni dashboards update codexlb2otel-full -f dashboards/v2/codexlb2otel-full.json
```

The generator refuses to emit a dashboard that has lost coverage. It reconciles the panels it built
against `internal/attr`'s metric constants (via `v2/.metrics_from_code.txt`), against the record types
the Loki sink emits, and against the span names in Tempo - and exits non-zero naming whatever is
missing. Add a metric to the exporter without adding it here and the generator fails rather than
silently never plotting it. Regenerate the sidecar after touching `internal/attr/names.go` with the
`dashboard-sidecar` just recipe. Its extraction includes metric constants with digits:

```bash
rg -n '^\s+Metric[A-Za-z0-9]+\s+=\s+"' internal/attr/names.go \
  | sed -E 's/.*= *"([^"]+)".*/\1/' | sort -u > dashboards/v2/.metrics_from_code.txt
```

The eight numbered dashboards beside it predate deployment and were never pushed; the v2 dashboard
supersedes them and is the one validated against live data.

The v2 tabs include enriched cost and token shape, proxy-versus-wire status diagnostics, agent
message and parent/child topology, ID lookup, and trace panels for both `execute_tool` and
`invoke_agent`. `$family` is a Prometheus-backed selector that defaults to non-probe traffic, so
cost, token, and latency panels do not accidentally price synthetic probe requests.

Deliberate omissions: `gen_ai.provider.name` is constant, `gen_ai.operation.name` has no requested
question, and raw request-log columns remain on the codex-lb dashboard rather than being duplicated.
