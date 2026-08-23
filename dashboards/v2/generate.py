#!/usr/bin/env python3
"""Generate the codexlb2otel full-telemetry dashboard (Grafana dashboard.grafana.app/v2).

Why a generator rather than a checked-in JSON blob: the acceptance bar for this
dashboard is total coverage - every metric this exporter can emit, every Loki record
type, and the trace tree, each used by at least one panel. That is a claim about a
600KB document, and the only way to keep it true through edits is to make the document
a function of a declared inventory and fail the build when the two disagree. See
verify() at the bottom: it reconciles the panels actually emitted against
internal/attr's metric constants and against the record types observed in Loki, and
raises rather than writing a dashboard that quietly dropped something.

Prometheus name mangling is done here, once, in prom(): OTel's dotted names become
underscored, counters gain _total, and unit suffixes (_seconds, _bytes, _ratio) are
appended by the Grafana Cloud OTLP gateway. Getting that wrong produces a panel that
is silently empty rather than an error, which is the failure mode this whole file is
arranged to avoid.

Usage:
    python3 dashboards/v2/generate.py > dashboards/v2/codexlb2otel-full.json
    gcx --context m7kni dashboards create -f dashboards/v2/manifest.json
"""

import json
import os
import sys

PROM = "grafanacloud-prom"
LOKI = "grafanacloud-logs"
TEMPO = "grafanacloud-traces"

JOB = 'job="codexlb2otel"'

# Every metric internal/attr declares, as the OTel name. Kept in this file rather than
# read from the Go source so the generator runs anywhere; verify() diffs it against
# .metrics_from_code.txt, which IS extracted from the source, so drift fails loudly.
ALL_METRICS = [
    "codexlb.attributes_rejected", "codexlb.baseline_resets", "codexlb.client_tool_pause",
    "codexlb.credits.balance", "codexlb.credits.unlimited", "codexlb.engine_calls",
    "codexlb.engine_iapi_inference", "codexlb.engine_iapi_sampling", "codexlb.engine_iapi_tbt",
    "codexlb.engine_service_inference", "codexlb.engine_service_minus_iapi_tbt",
    "codexlb.engine_service_sampling", "codexlb.engine_service_tbt",
    "codexlb.engine_uncached_prompt_tokens", "codexlb.engine_wall", "codexlb.errors",
    "codexlb.harness_unblocked", "codexlb.image_gen_tokens", "codexlb.pre_inference",
    "codexlb.rate_limit.model_used_percent", "codexlb.rate_limit.reset_after",
    "codexlb.rate_limit.used_percent", "codexlb.responses",
    "codexlb.responses_excl_engine_and_tool", "codexlb.responses_excl_engine_wait_sampling",
    "codexlb.responses_excl_engine_wait_sampling_iapi", "codexlb.responsesapi_excl_client_tools",
    "codexlb.safety_buffering_events", "codexlb.sampling_and_stream",
    "codexlb.selfobs.bytes_read", "codexlb.selfobs.current_file_offset",
    "codexlb.selfobs.decode_errors", "codexlb.selfobs.file_replacements",
    "codexlb.selfobs.files_reclaimed", "codexlb.selfobs.files_watched",
    "codexlb.selfobs.gzip_members_decoded", "codexlb.selfobs.ingest_lag_seconds",
    "codexlb.selfobs.lines_decoded", "codexlb.selfobs.open_responses",
    "codexlb.selfobs.partial_member_reads", "codexlb.selfobs.reducer_series",
    "codexlb.selfobs.reducer_threads", "codexlb.selfobs.sink_pending",
    "codexlb.selfobs.sink_rejections", "codexlb.selfobs.turns_emitted",
    "codexlb.selfobs.turns_evicted", "codexlb.selfobs.undecodable_lines", "codexlb.tokens",
    "codexlb.tool_calls", "codexlb.tool_calls_per_operation", "codexlb.transport_events",
    "codexlb.turn.duration", "codexlb.turns", "codexlb.web_search_requests",
    "gen_ai.client.operation.duration", "gen_ai.client.token.usage",
    "gen_ai.client.time_to_first_token",
]

# Prometheus name for each OTel metric, as the Grafana Cloud OTLP gateway writes it.
# Confirmed against the live series list on the deployment stack, 2026-08-07 - not derived
# from the spec and hoped for. The four marked ZERO-TRAFFIC are declared in the code
# and have simply never fired on this capture (no image generation, no web search, no
# client-tool pause recorded, no sink rejection since the last restart); they still get
# panels, because "no data" and "no panel" are different answers.
PROM_NAME = {
    "codexlb.attributes_rejected": "codexlb_attributes_rejected_total",
    "codexlb.baseline_resets": "codexlb_baseline_resets_total",
    "codexlb.client_tool_pause": "codexlb_client_tool_pause_seconds",     # ZERO-TRAFFIC
    "codexlb.credits.balance": "codexlb_credits_balance",
    "codexlb.credits.unlimited": "codexlb_credits_unlimited_ratio",
    "codexlb.engine_calls": "codexlb_engine_calls_total",
    "codexlb.engine_iapi_inference": "codexlb_engine_iapi_inference_seconds",
    "codexlb.engine_iapi_sampling": "codexlb_engine_iapi_sampling_seconds",
    "codexlb.engine_iapi_tbt": "codexlb_engine_iapi_tbt_seconds",
    "codexlb.engine_service_inference": "codexlb_engine_service_inference_seconds",
    "codexlb.engine_service_minus_iapi_tbt": "codexlb_engine_service_minus_iapi_tbt_seconds",
    "codexlb.engine_service_sampling": "codexlb_engine_service_sampling_seconds",
    "codexlb.engine_service_tbt": "codexlb_engine_service_tbt_seconds",
    "codexlb.engine_uncached_prompt_tokens": "codexlb_engine_uncached_prompt_tokens_total",
    "codexlb.engine_wall": "codexlb_engine_wall_seconds",
    "codexlb.errors": "codexlb_errors_total",
    "codexlb.harness_unblocked": "codexlb_harness_unblocked_seconds",
    "codexlb.image_gen_tokens": "codexlb_image_gen_tokens_total",         # ZERO-TRAFFIC
    "codexlb.pre_inference": "codexlb_pre_inference_seconds",
    "codexlb.rate_limit.model_used_percent": "codexlb_rate_limit_model_used_percent",
    "codexlb.rate_limit.reset_after": "codexlb_rate_limit_reset_after_seconds",
    "codexlb.rate_limit.used_percent": "codexlb_rate_limit_used_percent",
    "codexlb.responses": "codexlb_responses_total",
    "codexlb.responses_excl_engine_and_tool": "codexlb_responses_excl_engine_and_tool_seconds",
    "codexlb.responses_excl_engine_wait_sampling": "codexlb_responses_excl_engine_wait_sampling_seconds",
    "codexlb.responses_excl_engine_wait_sampling_iapi": "codexlb_responses_excl_engine_wait_sampling_iapi_seconds",
    "codexlb.responsesapi_excl_client_tools": "codexlb_responsesapi_excl_client_tools_seconds",
    "codexlb.safety_buffering_events": "codexlb_safety_buffering_events_total",
    "codexlb.sampling_and_stream": "codexlb_sampling_and_stream_seconds",
    "codexlb.selfobs.bytes_read": "codexlb_selfobs_bytes_read_total",
    "codexlb.selfobs.current_file_offset": "codexlb_selfobs_current_file_offset_bytes",
    "codexlb.selfobs.decode_errors": "codexlb_selfobs_decode_errors_total",
    "codexlb.selfobs.file_replacements": "codexlb_selfobs_file_replacements_total",
    "codexlb.selfobs.files_reclaimed": "codexlb_selfobs_files_reclaimed_total",
    "codexlb.selfobs.files_watched": "codexlb_selfobs_files_watched",
    "codexlb.selfobs.gzip_members_decoded": "codexlb_selfobs_gzip_members_decoded_total",
    "codexlb.selfobs.ingest_lag_seconds": "codexlb_selfobs_ingest_lag_seconds",
    "codexlb.selfobs.lines_decoded": "codexlb_selfobs_lines_decoded_total",
    "codexlb.selfobs.open_responses": "codexlb_selfobs_open_responses",
    "codexlb.selfobs.partial_member_reads": "codexlb_selfobs_partial_member_reads_total",
    "codexlb.selfobs.reducer_series": "codexlb_selfobs_reducer_series",
    "codexlb.selfobs.reducer_threads": "codexlb_selfobs_reducer_threads",
    "codexlb.selfobs.sink_pending": "codexlb_selfobs_sink_pending",
    "codexlb.selfobs.sink_rejections": "codexlb_selfobs_sink_rejections_total",  # ZERO-TRAFFIC
    "codexlb.selfobs.turns_emitted": "codexlb_selfobs_turns_emitted_total",
    "codexlb.selfobs.turns_evicted": "codexlb_selfobs_turns_evicted_total",
    "codexlb.selfobs.undecodable_lines": "codexlb_selfobs_undecodable_lines_total",
    "codexlb.tokens": "codexlb_tokens_total",
    "codexlb.tool_calls": "codexlb_tool_calls_total",
    "codexlb.tool_calls_per_operation": "codexlb_tool_calls_per_operation",
    "codexlb.transport_events": "codexlb_transport_events_total",
    "codexlb.turn.duration": "codexlb_turn_duration_seconds",
    "codexlb.turns": "codexlb_turns_total",
    "codexlb.web_search_requests": "codexlb_web_search_requests_total",   # ZERO-TRAFFIC
    "gen_ai.client.operation.duration": "gen_ai_client_operation_duration_seconds",
    "gen_ai.client.token.usage": "gen_ai_client_token_usage",
    "gen_ai.client.time_to_first_token": "gen_ai_client_time_to_first_token_seconds",
}

# The nine record types the Loki sink emits, as observed live. verify() requires a
# panel for each.
RECORD_TYPES = ["turn", "message", "agent_message", "prompt", "instructions",
                "tool_call", "tool_output", "error", "transport"]

# Span names observed in Tempo. The four critical_path.* children are the span-level
# expression of the same decomposition the latency tab shows as histograms.
SPAN_NAMES = ["turn", "critical_path.pre_inference", "critical_path.engine_wall",
              "critical_path.sampling_and_stream", "critical_path.other",
              "execute_tool", "generateText", "streamText"]

_covered_metrics = set()
_covered_records = set()
_covered_spans = set()
_panel_id = [0]


def prom(metric, suffix=""):
    """Prometheus series name for an OTel metric, marking it covered."""
    _covered_metrics.add(metric)
    return PROM_NAME[metric] + suffix


# Template-variable filters. Applied only to metrics that actually carry the label -
# adding a matcher for a label a series does not have silently empties the panel.
F_FULL = 'gen_ai_request_model=~"$model", codexlb_account_id=~"$account", codexlb_request_kind=~"$kind"'
F_MODEL = 'gen_ai_request_model=~"$model", codexlb_account_id=~"$account"'
F_ACCT = 'codexlb_account_id=~"$account"'

# gen_ai.token.type is a NESTED taxonomy, not a flat enum: cache_read is a sub-bucket
# of input and reasoning a sub-bucket of output. Only input+output may be summed or
# stacked; doing it across all four double-counts.
#
# The cache bucket is spelled cache_read, and this file has now had it BOTH ways round.
# It was "cached" here until issue #32 - and this comment used to record that
# cache_read had been tried and "came back empty", which was true at the time and is
# the exact wrong lesson to carry forward: the panel was empty because the emitter
# spelled it "cached", not because cache_read was a guess. #32 renamed the emitted
# value, because agent-observability's own dashboard matches cache_read literally in
# five panels and read zero from all of them. Nothing standard was given up either way:
# the registry's gen_ai.token.type enum defines only input and output.
ADDITIVE_TOKENS = 'gen_ai_token_type=~"input|output"'
NESTED_TOKENS = 'gen_ai_token_type=~"cache_read|reasoning"'

# Not every response series carries a model - some frames report usage before the model
# is known. Harmless in a rate panel, but in a pie chart the unlabelled series renders
# as a slice literally called "Value", so exclude it where the label IS the dimension.
HAS_MODEL = 'gen_ai_request_model!=""' 


def sel(extra=None, filt=F_FULL):
    parts = [JOB]
    if filt:
        parts.append(filt)
    if extra:
        parts.append(extra)
    return "{" + ", ".join(parts) + "}"


def q(expr, legend="", ref="A", ds=PROM, group="prometheus", instant=False, fmt=None):
    spec = {"expr": expr}
    if legend:
        spec["legendFormat"] = legend
    if instant:
        spec["instant"] = True
        spec["range"] = False
    if group == "loki":
        spec["queryType"] = fmt or "range"
    if group == "tempo":
        spec = {"query": expr, "queryType": fmt or "traceql", "limit": 20,
                "tableType": "traces"}
    return {"kind": "PanelQuery", "spec": {"refId": ref, "hidden": False, "query": {
        "kind": "DataQuery", "group": group, "version": "v0",
        "datasource": {"name": ds}, "spec": spec}}}


# refIds must be unique WITHIN a panel; Grafana renders "No data" with an error badge
# if they collide, and says nothing about why. Several panels here are assembled by
# concatenating helper output (hist_quantiles returns A/B/C, hist_avg returned A), so
# collisions were guaranteed the moment two helpers were combined. Reassigning here,
# centrally, means no caller has to remember - and the class of bug cannot come back
# through a new helper. Caught by a snapshot showing "No data" on a panel whose queries
# each returned data when run individually.
_REFS = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"


def _renumber(queries):
    for i, pq in enumerate(queries):
        pq["spec"]["refId"] = _REFS[i % len(_REFS)]
        inner = pq["spec"]["query"]["spec"]
        if "refId" in inner:
            inner["refId"] = pq["spec"]["refId"]
    return queries


def _as_table(queries):
    """A Prometheus query feeding a table panel MUST ask for format: table.

    Without it the instant result arrives as a wide time-series frame - the whole
    label set collapsed into one column header, with a series picker at the bottom of
    the panel - instead of one column per label. It renders, it has a number in it, and
    it is not the table anyone asked for, which is why it survived six panels. Applied
    centrally so a new table panel cannot reintroduce it. Loki and Tempo carry their
    own shape and are left alone.
    """
    for pq in queries:
        if pq["spec"]["query"]["group"] == "prometheus":
            pq["spec"]["query"]["spec"]["format"] = "table"
    return queries


def panel(title, queries, viz="timeseries", desc="", unit=None, opts=None,
          fieldcfg=None, transforms=None, thresholds=None, maxv=None, minv=None,
          decimals=None, overrides=None):
    queries = _renumber(queries)
    if viz == "table":
        queries = _as_table(queries)
    _panel_id[0] += 1
    defaults = {}
    if unit:
        defaults["unit"] = unit
    if maxv is not None:
        defaults["max"] = maxv
    if minv is not None:
        defaults["min"] = minv
    if decimals is not None:
        defaults["decimals"] = decimals
    if thresholds:
        defaults["thresholds"] = {"mode": "absolute", "steps": thresholds}
        defaults.setdefault("custom", {})
    if fieldcfg:
        defaults.update(fieldcfg)
    return {"kind": "Panel", "spec": {
        "id": _panel_id[0],
        "title": title,
        "description": desc,
        "links": [],
        "data": {"kind": "QueryGroup", "spec": {
            "queries": queries, "queryOptions": {}, "transformations": transforms or []}},
        "vizConfig": {"kind": "VizConfig", "group": viz, "version": "v0", "spec": {
            "options": opts or {},
            "fieldConfig": {"defaults": defaults, "overrides": overrides or []}}},
    }}


def text_panel(title, content):
    _panel_id[0] += 1
    return {"kind": "Panel", "spec": {
        "id": _panel_id[0], "title": title, "description": "", "links": [],
        "data": {"kind": "QueryGroup", "spec": {"queries": [], "queryOptions": {},
                                                "transformations": []}},
        "vizConfig": {"kind": "VizConfig", "group": "text", "version": "v0", "spec": {
            "options": {"mode": "markdown", "content": content},
            "fieldConfig": {"defaults": {}, "overrides": []}}},
    }}


# Viz option presets. Grafana fills defaults for anything omitted, but legend and
# tooltip left unset render as a single-line legend with no values, which is useless
# on a panel carrying twenty series.
LEG = {"legend": {"displayMode": "table", "placement": "bottom", "showLegend": True,
                  "calcs": ["mean", "max", "lastNotNull"]},
       "tooltip": {"mode": "multi", "sort": "desc"}}
LEG_R = {"legend": {"displayMode": "list", "placement": "right", "showLegend": True,
                    "calcs": []},
         "tooltip": {"mode": "multi", "sort": "desc"}}
STACK = {"custom": {"stacking": {"mode": "normal", "group": "A"}, "fillOpacity": 40,
                    "lineWidth": 1}}
PCT = {"custom": {"stacking": {"mode": "percent", "group": "A"}, "fillOpacity": 70,
                  "lineWidth": 1}}
BARS = {"custom": {"drawStyle": "bars", "fillOpacity": 70, "lineWidth": 0}}


def stat_opts(graph="area", calc="lastNotNull", text_size=None):
    o = {"reduceOptions": {"calcs": [calc], "fields": "", "values": False},
         "graphMode": graph, "colorMode": "value", "justifyMode": "auto",
         "textMode": "auto", "orientation": "auto"}
    if text_size:
        o["text"] = {"valueSize": text_size}
    return o


TABLE_OPTS = {"showHeader": True, "cellHeight": "sm",
              "footer": {"show": False, "reducer": ["sum"], "countRows": False}}


def organize(exclude, rename):
    """A schema-v2 organize transformation.

    The wrapper is `kind: "Transformation"` with the transformation id in `group` and
    the payload under `spec.options`. Writing the id as `kind` - which mirrors the
    classic schema and looks right - validates, pushes, and is then discarded, so the
    table renders with raw label names and every excluded column still present.
    """
    return {"kind": "Transformation", "group": "organize",
            "spec": {"options": {"excludeByName": exclude, "renameByName": rename,
                                 "indexByName": {}}}}
LOGS_OPTS = {"showTime": True, "showLabels": False, "showCommonLabels": False,
             "wrapLogMessage": True, "prettifyLogMessage": False, "enableLogDetails": True,
             "dedupStrategy": "none", "sortOrder": "Descending"}
HEAT_OPTS = {"calculate": False, "cellGap": 1, "color": {"mode": "scheme",
             "scheme": "Turbo", "steps": 64, "exponent": 0.5, "fill": "dark-orange",
             "reverse": False}, "yAxis": {"unit": "s"}, "legend": {"show": True},
             "tooltip": {"mode": "single", "showColorScale": False, "yHistogram": False},
             "rowsFrame": {"layout": "auto"}, "exemplars": {"color": "rgba(255,0,255,0.7)"}}

GREEN_RED = [{"color": "green", "value": None}, {"color": "orange", "value": 70},
             {"color": "red", "value": 90}]
LAG_STEPS = [{"color": "green", "value": None}, {"color": "orange", "value": 60},
             {"color": "red", "value": 300}]
ZERO_OK = [{"color": "green", "value": None}, {"color": "red", "value": 1}]


def hist_quantiles(metric, by, legend, extra=None, filt=F_FULL):
    """p50/p95/p99 for a histogram, as three queries on one panel."""
    m = prom(metric, "_bucket")
    out = []
    # Label from an explicit pair, not from the numeral: "0.5".split(".")[1] is "5",
    # which rendered p50 as "p5" on every histogram panel in the first push.
    for ref, quant, name in (("A", "0.5", "p50"), ("B", "0.95", "p95"), ("C", "0.99", "p99")):
        out.append(q(f'histogram_quantile({quant}, sum by (le, {by}) '
                     f'(rate({m}{sel(extra, filt)}[$__rate_interval])))',
                     f"{name} {legend}", ref))
    return out


def hist_avg(metric, by, legend, filt=F_FULL):
    """Mean from _sum/_count - the one thing a quantile cannot tell you."""
    s, c = prom(metric, "_sum"), prom(metric, "_count")
    return [q(f'sum by ({by}) (rate({s}{sel(None, filt)}[$__rate_interval])) / '
              f'clamp_min(sum by ({by}) (rate({c}{sel(None, filt)}[$__rate_interval])), 0.001)',
              legend)]


def mean_over_range(metric, by, extra, filt=F_FULL):
    """Histogram mean over the selected range, preserving only comparison cohorts."""
    s, c = prom(metric, "_sum"), prom(metric, "_count")
    return (f'sum by ({by}) (increase({s}{sel(extra, filt)}[$__range])) / '
            f'clamp_min(sum by ({by}) (increase({c}{sel(extra, filt)}[$__range])), 1)')


def count_over_range(metric, by, extra, filt=F_FULL):
    """Histogram observations over the selected range, grouped for cohort masking."""
    c = prom(metric, "_count")
    return f'sum by ({by}) (increase({c}{sel(extra, filt)}[$__range]))'


def logq(expr, legend="", ref="A", fmt="range"):
    return q(expr, legend, ref, ds=LOKI, group="loki", fmt=fmt)


def rec(rt):
    _covered_records.add(rt)
    return rt


def span(name):
    _covered_spans.add(name)
    return name


def tempoq(expr, ref="A"):
    return q(expr, "", ref, ds=TEMPO, group="tempo")


# ---------------------------------------------------------------------------
# Tab 1 - Overview
# ---------------------------------------------------------------------------
def tab_overview():
    p = []
    p.append(text_panel("Reading this dashboard", """
**Metric timestamps are INGEST time, not event time.** The exporter tails an archive and records into
the OTel SDK as it reads, so replaying a backlog - after a restart, or on first deployment - lands hours
of traffic on a few minutes of wall clock. A tall narrow spike across every panel at the same instant is
that, not a traffic surge. Cross-check against **ingest lag**: if it was high and falling at that moment,
you are looking at catch-up.

The **logs** do not have this problem (they carry the record's own timestamp, and anything older than
3h is dropped rather than backdated), and neither do the **traces**. Only the metrics.

`request_kind` - turn, prewarm, compaction, memory - are **concurrent series on one thread**, not phases
of one another. Filtering to `turn` hides real spend.
"""))
    p.append(panel(
        "Ingest lag", [q(f'{prom("codexlb.selfobs.ingest_lag_seconds")}{{{JOB}}}', "lag", instant=True)],
        "stat", unit="s", thresholds=LAG_STEPS,
        opts=stat_opts("area", text_size=42),
        desc="Wall clock now minus the newest record timestamp the tailer has seen. The "
             "one number that tells you whether anything else on this dashboard is "
             "current. Healthy is roughly the poll interval (5s). This is the ONLY "
             "wall-clock subtraction anywhere in the pipeline - everything else is "
             "measured against the archive's own clock."))
    p.append(panel(
        "Turns", [q(f'round(sum(increase({prom("codexlb.turns")}{sel()}[$__range])))', "turns", instant=True)],
        "stat", opts=stat_opts("none", "lastNotNull", 42), unit="short",
        desc="Logical turns completed in the selected range. A turn is the whole "
             "user-visible exchange; a response is one model call within it."))
    p.append(panel(
        "Responses", [q(f'round(sum(increase({prom("codexlb.responses")}{sel()}[$__range])))', "responses", instant=True)],
        "stat", opts=stat_opts("none", "lastNotNull", 42), unit="short",
        desc="Model responses in range. Responses per turn above ~1 means the harness "
             "is looping - tool calls, retries, or continuation."))
    p.append(panel(
        "Tokens", [q(f'round(sum(increase({prom("codexlb.tokens")}'
                     f'{sel(ADDITIVE_TOKENS)}[$__range])))', "tokens", instant=True)],
        "stat", opts=stat_opts("none", "lastNotNull", 42), unit="short",
        desc="input + output ONLY. Summing every gen_ai.token.type would double-count: "
             "cache_read is a sub-bucket nested inside input, and reasoning inside output, "
             "not additive siblings of them. See the Tokens tab."))
    p.append(panel(
        "Errors", [q(f'round(sum(increase({prom("codexlb.errors")}{sel(filt=F_MODEL)}[$__range])))', "errors", instant=True)],
        "stat", opts=stat_opts("none", "lastNotNull", 42), unit="short",
        thresholds=ZERO_OK,
        desc="Error events in range, from the archive's own error records - not this "
             "exporter's health. Zero is the expected value."))
    p.append(panel(
        "Rate-limit headroom (worst account)",
        [q(f'100 - max({prom("codexlb.rate_limit.used_percent")}{{{JOB}}})', "headroom %", instant=True)],
        "gauge", unit="percent", maxv=100, minv=0,
        thresholds=[{"color": "red", "value": None}, {"color": "orange", "value": 20},
                    {"color": "green", "value": 40}],
        desc="100 minus the highest primary-window utilisation across accounts. This is "
             "the number that decides whether the next request gets served."))

    p.append(panel(
        "Turn and response rate", [
            q(f'sum(rate({prom("codexlb.turns")}{sel()}[$__rate_interval]))', "turns/s", "A"),
            q(f'sum(rate({prom("codexlb.responses")}{sel()}[$__rate_interval]))', "responses/s", "B"),
            q(f'sum(rate({prom("codexlb.engine_calls")}{sel()}[$__rate_interval]))', "engine calls/s", "C"),
        ], unit="reqps", opts=LEG,
        desc="Three levels of the same traffic: turns (user-visible), responses (model "
             "calls), engine calls (upstream inference invocations). The gaps between "
             "them are where tool loops and retries live."))
    p.append(panel(
        "Turn duration percentiles",
        hist_quantiles("codexlb.turn.duration", "gen_ai_request_model", "{{gen_ai_request_model}}",
                       filt=F_MODEL),
        unit="s", opts=LEG,
        desc="End-to-end logical turn latency by model. Decomposed on the Latency tab."))
    p.append(panel(
        "Token rate (additive: input + output)", [
            q(f'sum by (gen_ai_token_type) (rate({prom("codexlb.tokens")}'
              f'{sel(ADDITIVE_TOKENS)}[$__rate_interval]))', "{{gen_ai_token_type}}"),
        ], unit="short", opts=LEG, fieldcfg=STACK,
        desc="Only input and output are stacked, because only they are additive. cache_read "
             "and reasoning are breakdowns NESTED inside them and appear on the Tokens "
             "tab, unstacked - stacking a sub-bucket on its parent silently doubles the "
             "total."))
    p.append(panel(
        "Traffic by model", [
            q(f'sum by (gen_ai_request_model) (increase({prom("codexlb.responses")}'
              f'{sel(HAS_MODEL)}[$__range]))',
              "{{gen_ai_request_model}}", instant=True),
        ], "piechart", opts={"legend": {"displayMode": "table", "placement": "right",
                                        "showLegend": True, "values": ["value", "percent"]},
                             "pieType": "donut", "reduceOptions": {
                                 "calcs": ["lastNotNull"], "fields": "", "values": False}},
        desc="Which model is actually doing the work."))
    p.append(panel(
        "Pipeline and sink health", [
            q(f'{prom("codexlb.selfobs.files_watched")}{{{JOB}}}', "archive files watched", "A"),
            q(f'sum({prom("codexlb.selfobs.sink_pending")}{{{JOB}}})', "sink lines pending", "B"),
            q(f'{prom("codexlb.selfobs.open_responses")}{{{JOB}}}', "open (unreduced) responses", "C"),
        ], unit="short", opts=LEG,
        desc="Steady values are healthy. Open responses climbing without bound means "
             "completion frames are not arriving and the reducer is accumulating."))
    p.append(panel(
        "Log volume by record type", [
            logq('sum by (codexlb_record_type) (count_over_time({service_name="codexlb2otel", '
                 'codexlb_record_type=~"$record_type"}[$__auto]))', "{{codexlb_record_type}}"),
        ], unit="short", opts=LEG, fieldcfg=STACK,
        desc="Every line this exporter pushes to Loki, by kind. Backlog older than "
             "loki.max_line_age (3h) is dropped locally rather than silently discarded "
             "server-side, so a gap here during catch-up is expected, not a fault."))
    for rt in RECORD_TYPES:
        rec(rt)
    return p


# ---------------------------------------------------------------------------
# Tab 2 - Model usage
# ---------------------------------------------------------------------------
def tab_models():
    p = []
    p.append(text_panel("Which instrument can answer which question", """
Model usage over time, **grouped and ungrouped by reasoning effort**. Effort is not on every
instrument, and filtering by a label a series does not carry empties the panel silently rather than
erroring, so this tab is arranged around one fact:

**`codexlb.responses` and `codexlb.turns` carry BOTH `gen_ai.request.model` and
`gen_ai.request.reasoning.level`. No token, duration or tool-call instrument carries effort.**

So every effort-split panel here counts **requests**, never tokens. Token shape by effort is a
LogQL question and lives on the **Tokens & Cost** tab. The one token panel here is by model only,
and says so.

`responses` counts every model response, prewarm included; `turns` excludes prewarm and compaction.
Where the two disagree the gap is client-tagged prewarm - real billed traffic, not noise.
"""))
    p.append(panel(
        "Response rate by model", [
            q(f'sum by (gen_ai_request_model) (rate({prom("codexlb.responses")}'
              f'{sel(HAS_MODEL)}[$__rate_interval]))', "{{gen_ai_request_model}}"),
        ], unit="reqps", opts=LEG, fieldcfg=STACK,
        desc="Model usage over time, NOT split by effort. Stacked, so the top of the "
             "stack is total response rate."))
    p.append(panel(
        "Turn rate by model", [
            q(f'sum by (gen_ai_request_model) (rate({prom("codexlb.turns")}'
              f'{sel(HAS_MODEL)}[$__rate_interval]))', "{{gen_ai_request_model}}"),
        ], unit="reqps", opts=LEG, fieldcfg=STACK,
        desc="The same shape with prewarm and compaction excluded - real work only. A "
             "model whose turn rate sits far below its response rate is being prewarmed "
             "more than it is being used."))
    p.append(panel(
        "Response rate by model and reasoning effort", [
            q(f'sum by (gen_ai_request_model, gen_ai_request_reasoning_level) '
              f'(rate({prom("codexlb.responses")}{sel(HAS_MODEL)}[$__rate_interval]))',
              "{{gen_ai_request_model}} / {{gen_ai_request_reasoning_level}}"),
        ], unit="reqps", opts=LEG, fieldcfg=STACK,
        desc="The grouped view, and the reason this tab exists. It shows an effort shift "
             "WITHIN a single model, which the by-model panel above averages away - and "
             "effort moves reasoning-token spend far harder than request count does."))
    p.append(panel(
        "Model mix over time (% of responses)", [
            q(f'sum by (gen_ai_request_model) (rate({prom("codexlb.responses")}'
              f'{sel(HAS_MODEL)}[$__rate_interval]))', "{{gen_ai_request_model}}"),
        ], unit="percentunit", opts=LEG_R, fieldcfg=PCT,
        desc="The same series normalised to 100%, so a change in the mix stays readable "
             "while total volume moves. Normalisation is the panel's percent stacking, "
             "not the query - the underlying values are still rates."))
    p.append(panel(
        "Reasoning effort mix over time (%)", [
            q(f'sum by (gen_ai_request_reasoning_level) (rate({prom("codexlb.responses")}'
              f'{sel()}[$__rate_interval]))', "{{gen_ai_request_reasoning_level}}"),
        ], unit="percentunit", opts=LEG_R, fieldcfg=PCT,
        desc="Effort mix with the model collapsed. Read it against the panel to its left: "
             "the model split can hold steady while the effort mix drifts upward."))
    p.append(panel(
        "Responses by model", [
            q(f'sum by (gen_ai_request_model) (increase({prom("codexlb.responses")}'
              f'{sel(HAS_MODEL)}[$__range]))', "{{gen_ai_request_model}}", instant=True),
        ], "piechart", opts={"legend": {"displayMode": "table", "placement": "right",
                                        "showLegend": True, "values": ["value", "percent"]},
                             "pieType": "donut", "reduceOptions": {
                                 "calcs": ["lastNotNull"], "fields": "", "values": False}},
        desc="Whole-window totals per model, ungrouped."))
    p.append(panel(
        "Responses by reasoning effort", [
            q(f'sum by (gen_ai_request_reasoning_level) '
              f'(increase({prom("codexlb.responses")}{sel()}[$__range]))',
              "{{gen_ai_request_reasoning_level}}", instant=True),
        ], "piechart", opts={"legend": {"displayMode": "table", "placement": "right",
                                        "showLegend": True, "values": ["value", "percent"]},
                             "pieType": "pie", "reduceOptions": {
                                 "calcs": ["lastNotNull"], "fields": "", "values": False}},
        desc="Whole-window totals per reasoning level, across every model."))
    p.append(panel(
        "Responses by model and effort", [
            q(f'sum by (gen_ai_request_model, gen_ai_request_reasoning_level) '
              f'(increase({prom("codexlb.responses")}{sel(HAS_MODEL)}[$__range]))',
              "{{gen_ai_request_model}} / {{gen_ai_request_reasoning_level}}", instant=True),
        ], "bargauge", opts={"displayMode": "gradient", "orientation": "horizontal",
                             "reduceOptions": {"calcs": ["lastNotNull"], "fields": "",
                                               "values": False}, "showUnfilled": True},
        desc="The two dimensions crossed and ranked - the flat ordering behind the "
             "stacked time series above."))
    p.append(panel(
        "Model x effort totals", [
            q(f'sum by (gen_ai_request_model, gen_ai_request_reasoning_level, '
              f'codexlb_request_kind) (increase({prom("codexlb.responses")}'
              f'{sel(HAS_MODEL)}[$__range]))', "", instant=True),
        ], "table", opts=TABLE_OPTS,
        transforms=[organize({"Time": True, "job": True, "service_name": True,
                              "deployment_environment": True, "instance": True},
                             {"Value": "responses", "gen_ai_request_model": "model",
                              "gen_ai_request_reasoning_level": "effort",
                              "codexlb_request_kind": "kind"})],
        desc="The numbers behind the tab, with request kind as a third column so prewarm "
             "is visible rather than folded into the model total. Sort any column."))
    p.append(panel(
        "Response rate by reasoning effort", [
            q(f'sum by (gen_ai_request_reasoning_level) (rate({prom("codexlb.responses")}'
              f'{sel()}[$__rate_interval]))', "{{gen_ai_request_reasoning_level}}"),
        ], unit="reqps", opts=LEG, fieldcfg=BARS,
        desc="Absolute effort volume over time, drawn as bars so each bucket reads as a "
             "discrete count rather than a smoothed area."))
    p.append(panel(
        "Token rate by model", [
            q(f'sum by (gen_ai_request_model) (rate({prom("codexlb.tokens")}'
              f'{sel(ADDITIVE_TOKENS, F_MODEL)}[$__rate_interval]))',
              "{{gen_ai_request_model}}"),
        ], unit="short", opts=LEG, fieldcfg=STACK,
        desc="What the model mix costs in tokens. BY MODEL ONLY - codexlb.tokens does not "
             "carry reasoning effort, so there is no effort split to be had here at any "
             "price. Input + output only; cache_read and reasoning are sub-buckets nested "
             "inside those two and stacking them on their own parents double-counts."))
    return p


# ---------------------------------------------------------------------------
# Tab 3 - Turns and responses
# ---------------------------------------------------------------------------
def tab_turns():
    p = []
    p.append(panel(
        "Responses by status", [
            q(f'sum by (codexlb_status) (rate({prom("codexlb.responses")}{sel()}[$__rate_interval]))',
              "{{codexlb_status}}"),
        ], unit="reqps", opts=LEG, fieldcfg=STACK,
        desc="completed is the happy path. Anything else is worth a look on the Errors tab."))
    p.append(panel(
        "Responses by request kind", [
            q(f'sum by (codexlb_request_kind) (rate({prom("codexlb.responses")}{sel()}[$__rate_interval]))',
              "{{codexlb_request_kind}}"),
        ], unit="reqps", opts=LEG, fieldcfg=STACK,
        desc="turn, prewarm, compaction and memory run CONCURRENTLY on the same thread - "
             "they are separate metric series, not phases of one another. Prewarm and "
             "compaction volume is invisible to the user but not to the bill."))
    p.append(panel(
        "Turns by originator and thread source", [
            q(f'sum by (codexlb_originator, codexlb_thread_source) '
              f'(increase({prom("codexlb.turns")}{sel()}[$__range]))',
              "{{codexlb_originator}} / {{codexlb_thread_source}}", instant=True),
        ], "barchart", opts={"orientation": "horizontal", "xTickLabelRotation": 0,
                             "showValue": "auto", "stacking": "none",
                             "legend": {"showLegend": False}},
        desc="Which client produced the traffic and how the thread began."))
    p.append(panel(
        "Response outcome matrix", [
            q(f'sum by (gen_ai_request_model, codexlb_status, codexlb_request_kind) '
              f'(increase({prom("codexlb.responses")}{sel()}[$__range]))', "", instant=True),
        ], "table", opts=TABLE_OPTS,
        transforms=[organize({"Time": True, "job": True, "service_name": True,
                              "deployment_environment": True, "instance": True},
                             {"Value": "count", "gen_ai_request_model": "model",
                              "codexlb_status": "status",
                              "codexlb_request_kind": "kind"})],
        desc="The three labels that matter, crossed. Read it as: which model, doing what, "
             "ended how."))
    p.append(panel(
        "Reasoning level and service tier", [
            q(f'sum by (gen_ai_request_reasoning_level) (increase({prom("codexlb.responses")}{sel()}[$__range]))',
              "reasoning={{gen_ai_request_reasoning_level}}", "A", instant=True),
            q(f'sum by (codexlb_service_tier) (increase({prom("codexlb.responses")}{sel()}[$__range]))',
              "tier={{codexlb_service_tier}}", "B", instant=True),
        ], "bargauge", opts={"displayMode": "gradient", "orientation": "horizontal",
                             "reduceOptions": {"calcs": ["lastNotNull"], "fields": "",
                                               "values": False}, "showUnfilled": True},
        desc="Reasoning level drives both latency and reasoning-token spend; service tier "
             "drives queueing."))
    p.append(panel(
        "Request vs response model (substitution)", [
            q(f'sum by (gen_ai_request_model, gen_ai_response_model) '
              f'(increase({prom("codexlb.responses")}{sel()}[$__range]))', "", instant=True),
        ], "table", opts=TABLE_OPTS,
        desc="These differ when the service substitutes a model. A row where they do not "
             "match is not an error, but it changes what you are actually paying for and "
             "what latency to expect."),)
    p.append(panel(
        "Baseline resets", [
            q(f'sum by (codexlb_family, codexlb_request_kind) '
              f'(rate({prom("codexlb.baseline_resets")}{sel(filt=None)}[$__rate_interval]))',
              "{{codexlb_family}} / {{codexlb_request_kind}}"),
        ], unit="short", opts=LEG, fieldcfg=BARS,
        desc="A cumulative counter on a thread went backwards, so the reducer rebased it. "
             "Expected at thread start and after a reconnect; a sustained rate means "
             "sequence tracking is losing frames and per-response deltas may be wrong."))
    p.append(panel(
        "Attributes rejected by the cardinality guard", [
            q(f'sum(rate({prom("codexlb.attributes_rejected")}{{{JOB}}}[$__rate_interval]))', "rejected/s"),
        ], unit="short", opts=LEG, fieldcfg=BARS, thresholds=ZERO_OK,
        desc="One guard per process caps distinct values per attribute. A non-zero rate "
             "means real values are being dropped to protect cardinality - the metric is "
             "still correct in aggregate but a dimension has gone lossy. Investigate "
             "before raising the cap."))
    p.append(panel(
        "Recent turn records", [
            logq(f'{{service_name="codexlb2otel", codexlb_record_type="{rec("turn")}"}} '
                 f'| codexlb_request_kind=~"$kind"'),
        ], "logs", opts=LOGS_OPTS,
        desc="One line per completed logical turn, carrying the full structured metadata "
             "set - thread, session, window, account, plan, token usage. Expand a line to "
             "pivot into any of it."))
    return p


# ---------------------------------------------------------------------------
# Tab 3 - Tokens and cost shape
# ---------------------------------------------------------------------------
def tab_tokens():
    p = []
    p.append(text_panel("gen_ai.token.type is NOT a flat enum", """
`input` and `output` are additive siblings. **`cache_read` is nested inside `input`** and **`reasoning`
is nested inside `output`** - they are breakdowns of their parent, not peers of it. Summing or stacking
all four double-counts, which is why the panels below keep the additive view and the nested view apart,
and why the ratio panel divides `cache_read` by `input` rather than by `input + cache_read`.

That nesting is also what `gen_ai_token_semantics="inclusive"` declares on these series, so a consumer
never has to infer it from the provider name.

The same trap is why `engine_uncached_prompt_tokens` is deliberately a separate metric rather than a
fifth `gen_ai.token.type` value: `uncached = input - cache_read` is another nested breakdown, and putting
it on the same axis would double-count against `input` for anyone who did not know to exclude it.
"""))
    p.append(panel(
        "Additive token volume (input + output)", [
            q(f'sum by (gen_ai_token_type) (rate({prom("codexlb.tokens")}'
              f'{sel(ADDITIVE_TOKENS)}[$__rate_interval]))', "{{gen_ai_token_type}}"),
        ], unit="short", opts=LEG, fieldcfg=STACK,
        desc="Safe to stack: these two do not overlap, so the stack height is the real "
             "total."))
    p.append(panel(
        "Nested breakdowns (cache_read within input, reasoning within output)", [
            q(f'sum by (gen_ai_token_type) (rate({prom("codexlb.tokens")}'
              f'{sel(NESTED_TOKENS)}[$__rate_interval]))', "{{gen_ai_token_type}}"),
        ], unit="short", opts=LEG,
        desc="Deliberately NOT stacked. Each line is a subset of one of the lines in the "
             "panel above, so stacking them together would be arithmetically meaningless."))
    tok = prom("codexlb.tokens")
    cached = sel('gen_ai_token_type="cache_read"')
    promptish = sel('gen_ai_token_type="input"')
    p.append(panel(
        "Cache read ratio", [
            q(f'100 * sum(rate({tok}{cached}[$__rate_interval])) '
              f'/ clamp_min(sum(rate({tok}{promptish}[$__rate_interval])), 0.001)',
              "cache read %"),
        ], "gauge", unit="percent", maxv=100, minv=0,
        thresholds=[{"color": "red", "value": None}, {"color": "orange", "value": 40},
                    {"color": "green", "value": 70}],
        desc="cache_read / input. The denominator is input alone, NOT input + cache_read, "
             "because cache_read is already counted inside input - dividing by the sum would "
             "understate the hit rate and can never reach 100%. This is the single "
             "biggest lever on spend, and it collapses whenever the prompt prefix "
             "changes: a system-prompt edit shows up here before it shows up on the bill."))
    p.append(panel(
        "Uncached prompt tokens", [
            q(f'sum by (gen_ai_request_model) '
              f'(rate({prom("codexlb.engine_uncached_prompt_tokens")}{sel()}[$__rate_interval]))',
              "{{gen_ai_request_model}}"),
        ], unit="short", opts=LEG, fieldcfg=STACK,
        desc="The engine's own count of prompt tokens it had to process rather than reuse. "
             "Cross-check against the cache ratio above: they should move inversely."))
    p.append(panel(
        "Token usage distribution (gen_ai semconv)",
        hist_quantiles("gen_ai.client.token.usage", "gen_ai_token_type", "{{gen_ai_token_type}}"),
        unit="short", opts=LEG,
        desc="The OTel GenAI standard histogram, kept alongside the codexlb counter "
             "deliberately: this one is per-request DISTRIBUTION, the counter is volume. "
             "A rising p99 with flat volume is prompt growth."))
    p.append(panel(
        "Tokens by model and kind", [
            q(f'sum by (gen_ai_request_model, codexlb_request_kind, gen_ai_token_type) '
              f'(increase({prom("codexlb.tokens")}{sel()}[$__range]))', "", instant=True),
        ], "table", opts=TABLE_OPTS,
        desc="Where the tokens actually went. Sort by value to find the expensive corner - "
             "it is often compaction or prewarm rather than user turns."))
    p.append(panel(
        "Image generation tokens", [
            q(f'sum by (gen_ai_request_model) (rate({prom("codexlb.image_gen_tokens")}{sel()}[$__rate_interval]))',
              "{{gen_ai_request_model}}"),
        ], unit="short", opts=LEG, fieldcfg=BARS,
        desc="NO DATA IS THE EXPECTED STATE on this capture - no image generation has "
             "occurred. The panel exists so the first image-generating turn is visible "
             "immediately rather than being invisible until someone thinks to look."))
    p.append(panel(
        "Web search requests", [
            q(f'sum by (gen_ai_request_model) (rate({prom("codexlb.web_search_requests")}{sel()}[$__rate_interval]))',
              "{{gen_ai_request_model}}"),
        ], unit="short", opts=LEG, fieldcfg=BARS,
        desc="Also expected empty on this capture. Web search is billed separately from "
             "tokens, so it is worth its own panel rather than being folded into tool calls."))
    p.append(panel(
        "Credits balance", [
            q(f'{prom("codexlb.credits.balance")}{{{JOB}}}', "{{codexlb_account_id}} ({{codexlb_plan_type}})"),
        ], unit="short", opts=LEG_R,
        desc="Remaining credit, per account. Flat at zero with unlimited=1 below is a "
             "plan without a balance, not an exhausted one."))
    p.append(panel(
        "Unlimited plan", [
            q(f'{prom("codexlb.credits.unlimited")}{{{JOB}}}',
              "{{codexlb_account_id}} ({{codexlb_plan_type}})", instant=True),
        ], "stat", opts=stat_opts("none"), unit="short",
        fieldcfg={"mappings": [{"type": "value", "options": {
            "0": {"text": "metered", "color": "blue", "index": 0},
            "1": {"text": "unlimited", "color": "green", "index": 1}}}]},
        desc="1 when the account's plan carries no credit limit. Reads the credits "
             "balance above for you - a zero balance means nothing on an unlimited plan."))
    return p


# ---------------------------------------------------------------------------
# Tab 4 - Fast mode
# ---------------------------------------------------------------------------
def tab_fast_mode():
    p = []
    p.append(text_panel("What fast mode can and cannot prove", """
**Fast mode is `codexlb_service_tier_requested="priority"`.** Normal traffic is the series where
that requested-tier label is absent; it is not a request for `default`. The separately captured
`codexlb_service_tier` is what the response reports, and responses can report `default` or `auto`
even when the request asked for `priority`.

The **reported priority rate** therefore answers whether response metadata acknowledges the request.
It does not prove how the request was queued. Latency comparisons answer whether fast traffic was
observably faster, but they are observational rather than an A/B experiment. Use the per-model and
request-kind effect panels with their sample counts: an overall mean would mostly measure a changing
mix of models and work, not the effect of fast mode.

Positive improvement means fast was faster; negative means it was slower. A blank comparison means
the selected window does not contain both fast and normal samples for the same cohort.
"""))
    p.append(panel(
        "Fast requests", [q(
            f'round(sum(increase({prom("codexlb.responses")}'
            f'{sel("codexlb_service_tier_requested=\"priority\"")}[$__range])))',
            "fast responses", instant=True)],
        "stat", unit="short", opts=stat_opts("none", "lastNotNull", 42),
        desc="Responses whose request explicitly asked for priority processing in the selected range."))
    p.append(panel(
        "Fast share", [q(
            f'100 * (sum(increase({prom("codexlb.responses")}'
            f'{sel("codexlb_service_tier_requested=\"priority\"")}[$__range])) or vector(0)) / '
            f'clamp_min(sum(increase({prom("codexlb.responses")}{sel()}[$__range])), 1)',
            "fast share", instant=True)],
        "stat", unit="percent", minv=0, maxv=100, decimals=1,
        opts=stat_opts("none", "lastNotNull", 42),
        desc="Share of selected responses that explicitly asked for priority. Normal is an absent "
             "requested-tier label, not a requested value called default."))
    p.append(panel(
        "Reported priority rate", [q(
            f'100 * (sum(increase({prom("codexlb.responses")}'
            f'{sel("codexlb_service_tier_requested=\"priority\", codexlb_service_tier=\"priority\"")}[$__range])) '
            f'or vector(0)) / '
            f'clamp_min(sum(increase({prom("codexlb.responses")}'
            f'{sel("codexlb_service_tier_requested=\"priority\"")}[$__range])), 1)',
            "reported priority", instant=True)],
        "stat", unit="percent", minv=0, maxv=100, decimals=1,
        opts=stat_opts("none", "lastNotNull", 42),
        desc="Of requests that asked for priority, the percentage whose response also reported "
             "priority. This is metadata agreement, not proof of queue treatment; default or auto "
             "may mean the request was declined or that the response does not echo the applied tier."))
    p.append(panel(
        "Requested fast traffic by reported tier", [q(
            f'sum by (codexlb_service_tier) (rate({prom("codexlb.responses")}'
            f'{sel("codexlb_service_tier_requested=\"priority\"")}[$__rate_interval]))',
            "reported={{codexlb_service_tier}}")],
        unit="reqps", opts=LEG, fieldcfg=STACK,
        desc="Only requests that asked for priority, split by the tier reported in each response. "
             "An empty reported value is preserved rather than rewritten."))
    p.append(panel(
        "TTFT sample sizes by cohort", [q(
            f'round(sum by (gen_ai_request_model, codexlb_request_kind, '
            f'codexlb_service_tier_requested) (increase('
            f'{prom("gen_ai.client.time_to_first_token", "_count")}'
            f'{sel()}[$__range])))', "", instant=True)],
        "table", opts=TABLE_OPTS,
        transforms=[organize({"Time": True, "job": True, "service_name": True,
                              "deployment_environment": True, "instance": True},
                             {"Value": "TTFT samples", "gen_ai_request_model": "model",
                              "codexlb_request_kind": "kind",
                              "codexlb_service_tier_requested": "requested tier"})],
        desc="The exact denominator for the TTFT comparison. A blank requested tier is normal "
             "traffic. Do not interpret a large percentage based on a tiny cohort."))
    p.append(panel(
        "Time to first token by requested mode",
        hist_quantiles("gen_ai.client.time_to_first_token", "codexlb_service_tier_requested",
                       "asked={{codexlb_service_tier_requested}}"),
        unit="s", opts=LEG,
        desc="Perceived responsiveness grouped by what Codex asked for. Blank is normal; priority "
             "is fast. This aggregate distribution shows shape; the cohort-normalized effect below "
             "is the fairer comparison."))

    fast_ttft = mean_over_range(
        "gen_ai.client.time_to_first_token", "gen_ai_request_model, codexlb_request_kind",
        'codexlb_service_tier_requested="priority"')
    normal_ttft = mean_over_range(
        "gen_ai.client.time_to_first_token", "gen_ai_request_model, codexlb_request_kind",
        'codexlb_service_tier_requested!~"priority"')
    fast_ttft_n = count_over_range(
        "gen_ai.client.time_to_first_token", "gen_ai_request_model, codexlb_request_kind",
        'codexlb_service_tier_requested="priority"')
    normal_ttft_n = count_over_range(
        "gen_ai.client.time_to_first_token", "gen_ai_request_model, codexlb_request_kind",
        'codexlb_service_tier_requested!~"priority"')
    p.append(panel(
        "Fast TTFT improvement by model and kind", [q(
            f'(100 * (({normal_ttft}) - ({fast_ttft})) / clamp_min(({normal_ttft}), 0.001)) '
            f'and on (gen_ai_request_model, codexlb_request_kind) ({fast_ttft_n} > 0) '
            f'and on (gen_ai_request_model, codexlb_request_kind) ({normal_ttft_n} > 0)',
            "{{gen_ai_request_model}} / {{codexlb_request_kind}}", instant=True)],
        "bargauge", unit="percent", decimals=1,
        opts={"displayMode": "gradient", "orientation": "horizontal",
              "reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False},
              "showUnfilled": True},
        thresholds=[{"color": "red", "value": None}, {"color": "green", "value": 0}],
        desc="Mean TTFT improvement within the same model and request kind: "
             "100 × (normal - fast) / normal. Positive is faster. Cohorts without both modes "
             "are absent, avoiding a misleading cross-model aggregate."))

    fast_turn = mean_over_range("codexlb.turn.duration", "gen_ai_request_model",
                                'codexlb_service_tier_requested="priority"', F_MODEL)
    normal_turn = mean_over_range("codexlb.turn.duration", "gen_ai_request_model",
                                  'codexlb_service_tier_requested!~"priority"', F_MODEL)
    fast_turn_n = count_over_range("codexlb.turn.duration", "gen_ai_request_model",
                                   'codexlb_service_tier_requested="priority"', F_MODEL)
    normal_turn_n = count_over_range("codexlb.turn.duration", "gen_ai_request_model",
                                     'codexlb_service_tier_requested!~"priority"', F_MODEL)
    p.append(panel(
        "Turn-duration sample sizes by model", [q(
            f'round(sum by (gen_ai_request_model, codexlb_service_tier_requested) '
            f'(increase({prom("codexlb.turn.duration", "_count")}{sel(None, F_MODEL)}[$__range])))',
            "", instant=True)],
        "table", opts=TABLE_OPTS,
        transforms=[organize({"Time": True, "job": True, "service_name": True,
                              "deployment_environment": True, "instance": True},
                             {"Value": "turn samples", "gen_ai_request_model": "model",
                              "codexlb_service_tier_requested": "requested tier"})],
        desc="The exact denominator for the end-to-end comparison below. Blank is normal and "
             "priority is fast; only real user-visible turns enter this histogram."))
    p.append(panel(
        "Fast end-to-end turn improvement by model", [q(
            f'(100 * (({normal_turn}) - ({fast_turn})) / clamp_min(({normal_turn}), 0.001)) '
            f'and on (gen_ai_request_model) ({fast_turn_n} > 0) '
            f'and on (gen_ai_request_model) ({normal_turn_n} > 0)',
            "{{gen_ai_request_model}}", instant=True)],
        "bargauge", unit="percent", decimals=1,
        opts={"displayMode": "gradient", "orientation": "horizontal",
              "reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False},
              "showUnfilled": True},
        thresholds=[{"color": "red", "value": None}, {"color": "green", "value": 0}],
        desc="Mean user-visible turn-duration improvement within each model. Positive is faster. "
             "Turn duration only records real turns, so request kind is not a dimension here."))
    return p


# ---------------------------------------------------------------------------
# Tab 5 - Latency and the critical path
# ---------------------------------------------------------------------------
def tab_latency():
    p = []
    p.append(text_panel("How to read this tab", """
The critical path decomposes one response into **pre-inference → engine wall → sampling and stream**,
plus whatever is left over. Each stage is a histogram in its own right, so a slow turn can be attributed
rather than guessed at. `critical_path_coverage` on the stage metrics tells you whether the stages
actually account for the whole response - when it says partial, the decomposition is missing a piece and
the leftovers land in *other*.

The `engine_*` family is the upstream provider's own timing, reported back in the frames: **service**
timings are what the serving layer measured, **iapi** timings are what the inner inference API measured.
`service_minus_iapi_tbt` is the gap between them - queueing and transport that belongs to neither model.

The `responses_excl_*` family is the same response measured with different things subtracted, so you can
isolate what a stage costs by comparing two of them rather than trusting a single attribution.
"""))
    p.append(panel(
        "Critical path stages (p95)", [
            q(f'histogram_quantile(0.95, sum by (le) (rate({prom("codexlb.pre_inference", "_bucket")}'
              f'{sel()}[$__rate_interval])))', "pre_inference", "A"),
            q(f'histogram_quantile(0.95, sum by (le) (rate({prom("codexlb.engine_wall", "_bucket")}'
              f'{sel()}[$__rate_interval])))', "engine_wall", "B"),
            q(f'histogram_quantile(0.95, sum by (le) (rate({prom("codexlb.sampling_and_stream", "_bucket")}'
              f'{sel()}[$__rate_interval])))', "sampling_and_stream", "C"),
            q(f'histogram_quantile(0.95, sum by (le) (rate({prom("codexlb.harness_unblocked", "_bucket")}'
              f'{sel()}[$__rate_interval])))', "harness_unblocked", "D"),
        ], unit="s", opts=LEG, fieldcfg=STACK,
        desc="Stacked p95 by stage. Stacking quantiles is not arithmetically exact - "
             "p95s do not sum - but it is the fastest way to see WHICH stage moved, "
             "which is the question this panel exists to answer. Use the mean panel "
             "below when you need the sum to be honest."))
    p.append(panel(
        "Critical path stages (mean, additive)",
        hist_avg("codexlb.pre_inference", "codexlb_critical_path_coverage", "pre_inference {{codexlb_critical_path_coverage}}")
        + [hist_avg("codexlb.engine_wall", "codexlb_critical_path_coverage", "engine_wall {{codexlb_critical_path_coverage}}")[0]]
        + [hist_avg("codexlb.sampling_and_stream", "codexlb_critical_path_coverage", "sampling_and_stream {{codexlb_critical_path_coverage}}")[0]],
        unit="s", opts=LEG, fieldcfg=STACK,
        desc="Means DO sum, so this stack is a true decomposition. Split by "
             "critical_path_coverage so a partial attribution cannot masquerade as a "
             "complete one."))
    p.append(panel(
        "Engine wall by model",
        hist_quantiles("codexlb.engine_wall", "gen_ai_request_model", "{{gen_ai_request_model}}"),
        unit="s", opts=LEG,
        desc="Time the upstream engine was working, as it reported it. This is the part "
             "no amount of local tuning changes."))
    p.append(panel(
        "Service vs IAPI inference",
        hist_quantiles("codexlb.engine_service_inference", "gen_ai_request_model", "service {{gen_ai_request_model}}")
        + [hist_quantiles("codexlb.engine_iapi_inference", "gen_ai_request_model", "iapi {{gen_ai_request_model}}")[1]],
        unit="s", opts=LEG,
        desc="Two measurements of the same inference from different layers. A widening "
             "gap is overhead between them, not a slower model."))
    p.append(panel(
        "Service vs IAPI sampling",
        hist_quantiles("codexlb.engine_service_sampling", "gen_ai_request_model", "service {{gen_ai_request_model}}")
        + [hist_quantiles("codexlb.engine_iapi_sampling", "gen_ai_request_model", "iapi {{gen_ai_request_model}}")[1]],
        unit="s", opts=LEG,
        desc="Same pairing for the sampling phase - generating tokens once the prompt is "
             "processed."))
    p.append(panel(
        "Time between tokens",
        hist_quantiles("codexlb.engine_service_tbt", "gen_ai_request_model", "service TBT {{gen_ai_request_model}}")
        + [hist_quantiles("codexlb.engine_iapi_tbt", "gen_ai_request_model", "iapi TBT {{gen_ai_request_model}}")[1]]
        + [hist_quantiles("codexlb.engine_service_minus_iapi_tbt", "gen_ai_request_model", "gap {{gen_ai_request_model}}")[1]],
        unit="s", opts=LEG,
        desc="TBT is what streaming feels like. The service-minus-iapi gap is transport "
             "and queueing between the two layers - if that is what is large, the model "
             "is not the problem."))
    p.append(panel(
        "Time to first token (gen_ai semconv)",
        hist_quantiles("gen_ai.client.time_to_first_token", "gen_ai_request_model", "{{gen_ai_request_model}}"),
        unit="s", opts=LEG,
        desc="TTFT dominates perceived responsiveness far more than total duration does."))
    p.append(panel(
        "Time to first token by REQUESTED service tier",
        hist_quantiles("gen_ai.client.time_to_first_token", "codexlb_service_tier_requested",
                       "asked={{codexlb_service_tier_requested}}"),
        unit="s", opts=LEG,
        desc="Whether priority processing is worth having. Grouped by what the client "
             "ASKED for, because what the server reports having served is not a usable "
             "answer: across every response after the 2026-08-08 cutover the request said "
             "priority and the response said default, never once priority. So this panel, "
             "and the mean below, are the measurement - the served label is not. "
             "The series with no tier is the pre-cutover baseline, absent rather than "
             "empty; once every request asks for a tier it stops advancing and the "
             "comparison becomes historical rather than live."))
    p.append(panel(
        "Turn duration mean by requested service tier",
        hist_avg("codexlb.turn.duration", "codexlb_service_tier_requested",
                 "asked={{codexlb_service_tier_requested}}"),
        unit="s", opts=LEG,
        desc="The mean, not a quantile, and end-to-end rather than to-first-token: a "
             "tier change should move the whole distribution, and comparing two p95s "
             "across a changeover conflates the shift with whatever else moved in the "
             "window. Read it alongside the TTFT panel above - TTFT is where a queueing "
             "change shows up first and largest."))
    p.append(panel(
        "Response duration, with stages excluded", [
            q(f'histogram_quantile(0.95, sum by (le) (rate({prom("codexlb.responses_excl_engine_and_tool", "_bucket")}'
              f'{sel()}[$__rate_interval])))', "excl engine and tool", "A"),
            q(f'histogram_quantile(0.95, sum by (le) (rate({prom("codexlb.responses_excl_engine_wait_sampling", "_bucket")}'
              f'{sel()}[$__rate_interval])))', "excl engine wait + sampling", "B"),
            q(f'histogram_quantile(0.95, sum by (le) (rate({prom("codexlb.responses_excl_engine_wait_sampling_iapi", "_bucket")}'
              f'{sel()}[$__rate_interval])))', "excl engine wait + sampling (iapi)", "C"),
            q(f'histogram_quantile(0.95, sum by (le) (rate({prom("codexlb.responsesapi_excl_client_tools", "_bucket")}'
              f'{sel()}[$__rate_interval])))', "responses API excl client tools", "D"),
        ], unit="s", opts=LEG,
        desc="The same response with different components subtracted. Differences between "
             "these lines ARE the components - this is how you attribute latency without "
             "trusting a single computed breakdown."))
    p.append(panel(
        "gen_ai client operation duration (by status)",
        hist_quantiles("gen_ai.client.operation.duration", "status", "{{status}}"),
        unit="s", opts=LEG,
        desc="The OTel GenAI standard latency histogram, split by outcome. Errors that "
             "are FAST and errors that are SLOW are different failures."))
    p.append(panel(
        "Engine wall heatmap", [
            q(f'sum by (le) (rate({prom("codexlb.engine_wall", "_bucket")}{sel()}[$__rate_interval]))',
              "{{le}}"),
        ], "heatmap", opts=HEAT_OPTS, unit="s",
        fieldcfg={"custom": {"scaleDistribution": {"type": "linear"}, "hideFrom": {
            "tooltip": False, "viz": False, "legend": False}}},
        desc="The distribution rather than its summary. Bimodality - two bands - means "
             "two populations of request, which a p95 line hides completely. A single "
             "narrow vertical band is the backlog-replay artifact described on the "
             "Overview tab, not a real burst: every bucket was filled during one catch-up "
             "pass."))
    p.append(panel(
        "Client tool pause", [
            q(f'histogram_quantile(0.95, sum by (le, gen_ai_request_model) '
              f'(rate({prom("codexlb.client_tool_pause", "_bucket")}{sel()}[$__rate_interval])))',
              "p95 {{gen_ai_request_model}}"),
        ], unit="s", opts=LEG,
        desc="Time the model spent waiting on a CLIENT-side tool - the harness, not the "
             "provider. Expected empty on this capture. When it is not empty, this is "
             "latency you own and can actually fix."))
    return p


# ---------------------------------------------------------------------------
# Tab 5 - Tools and agents
# ---------------------------------------------------------------------------
def tab_tools():
    p = []
    p.append(panel(
        "Tool call rate by tool", [
            q(f'sum by (gen_ai_tool_name) (rate({prom("codexlb.tool_calls")}{sel()}[$__rate_interval]))',
              "{{gen_ai_tool_name}}"),
        ], unit="reqps", opts=LEG, fieldcfg=STACK,
        desc="Which tools the agent actually reaches for."))
    p.append(panel(
        "Tool calls in range", [
            q(f'sum by (gen_ai_tool_name) (increase({prom("codexlb.tool_calls")}{sel()}[$__range]))',
              "{{gen_ai_tool_name}}", instant=True),
        ], "barchart", opts={"orientation": "horizontal", "showValue": "auto",
                             "legend": {"showLegend": False}, "xTickLabelRotation": 0},
        desc="Same data ranked. The long tail is usually more interesting than the head."))
    p.append(panel(
        "Tool calls per operation",
        hist_quantiles("codexlb.tool_calls_per_operation", "gen_ai_request_model", "{{gen_ai_request_model}}"),
        unit="short", opts=LEG,
        desc="How many tools one response invokes. A rising p99 is an agent looping - it "
             "costs tokens and latency without necessarily failing, so nothing else alerts."))
    p.append(panel(
        "Tool call records", [
            logq(f'{{service_name="codexlb2otel", codexlb_record_type="{rec("tool_call")}"}} '
                 f'| gen_ai_tool_name!=""'),
        ], "logs", opts=LOGS_OPTS,
        desc="Individual tool invocations with arguments, bounded at 16KB per call. "
             "Truncation is marked inline with the original length."))
    p.append(panel(
        "Tool output records", [
            logq(f'{{service_name="codexlb2otel", codexlb_record_type="{rec("tool_output")}"}}'),
        ], "logs", opts=LOGS_OPTS,
        desc="What came back. Paired with the call above by turn and thread id."))
    p.append(panel(
        "Subagent activity", [
            q(f'sum by (codexlb_subagent_kind) (increase({prom("codexlb.turns")}{sel()}[$__range]))',
              "{{codexlb_subagent_kind}}", instant=True),
        ], "piechart", opts={"legend": {"displayMode": "table", "placement": "right",
                                        "showLegend": True, "values": ["value", "percent"]},
                             "pieType": "pie", "reduceOptions": {"calcs": ["lastNotNull"],
                                                                 "fields": "", "values": False}},
        desc="Turns attributable to a subagent rather than the top-level thread. Only the "
             "instructions record carries subagent lineage, which is why this is derived "
             "from turns rather than counted directly."))
    p.append(panel(
        "Instructions records (agent lineage)", [
            logq(f'{{service_name="codexlb2otel", codexlb_record_type="{rec("instructions")}"}}'),
        ], "logs", opts=LOGS_OPTS,
        desc="The only record carrying parent_thread_id, parent_turn_id, "
             "forked_from_thread_id and subagent_kind - the agent tree lives here. "
             "instructions_hash lets you tell a prompt change from a prompt reuse."))
    p.append(panel(
        "Agent messages", [
            logq(f'{{service_name="codexlb2otel", codexlb_record_type="{rec("agent_message")}"}}'),
        ], "logs", opts=LOGS_OPTS,
        desc="What the agent said, as opposed to what it did."))
    return p


# ---------------------------------------------------------------------------
# Tab 6 - Rate limits and accounts
# ---------------------------------------------------------------------------
def tab_limits():
    p = []
    p.append(panel(
        "Primary window utilisation", [
            q(f'{prom("codexlb.rate_limit.used_percent")}{{{JOB}}}',
              "{{codexlb_account_id}} ({{codexlb_plan_type}})"),
        ], unit="percent", maxv=100, minv=0, opts=LEG, thresholds=GREEN_RED,
        desc="Percent of the account's primary rate-limit window consumed. This is "
             "reported BY the provider in the frames, not inferred here."))
    p.append(panel(
        "Per-model window utilisation", [
            q(f'{prom("codexlb.rate_limit.model_used_percent")}{{{JOB}}}',
              "{{codexlb_account_id}} / {{gen_ai_request_model}}"),
        ], unit="percent", maxv=100, minv=0, opts=LEG, thresholds=GREEN_RED,
        desc="Some models carry their own window. This one can be exhausted while the "
             "primary window above still looks healthy - that mismatch is exactly the "
             "case worth watching."))
    p.append(panel(
        "Time until window reset", [
            q(f'{prom("codexlb.rate_limit.reset_after")}{{{JOB}}}',
              "{{codexlb_account_id}} ({{codexlb_plan_type}})"),
        ], unit="s", opts=LEG,
        desc="Sawtooth is healthy - it should fall to zero and jump back. A flat line "
             "means the value has stopped being refreshed and everything above it is stale."))
    p.append(panel(
        "Current utilisation", [
            q(f'{prom("codexlb.rate_limit.used_percent")}{{{JOB}}}',
              "{{codexlb_account_id}}", instant=True),
        ], "bargauge", unit="percent", maxv=100, minv=0, thresholds=GREEN_RED,
        opts={"displayMode": "lcd", "orientation": "horizontal", "showUnfilled": True,
              "reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False}},
        desc="At-a-glance headroom per account."))
    p.append(panel(
        "Traffic by plan type and service tier", [
            q(f'sum by (codexlb_plan_type, codexlb_service_tier) '
              f'(increase({prom("codexlb.responses")}{sel()}[$__range]))', "", instant=True),
        ], "table", opts=TABLE_OPTS,
        desc="Plan governs the limits; tier governs the queue. Both change what a given "
             "latency number means."))
    p.append(panel(
        "Requested vs served service tier", [
            q(f'sum by (codexlb_service_tier_requested, codexlb_service_tier) '
              f'(increase({prom("codexlb.responses")}{sel()}[$__range]))', "", instant=True),
        ], "table", opts=TABLE_OPTS,
        desc="What was asked for against what the platform says it served. These do NOT "
             "agree and are not expected to: measured over the 332 responses following "
             "the 2026-08-08 priority cutover, every request asked priority and every "
             "response reported default (321) or auto (9). Whether that is the platform "
             "declining to serve priority or simply not reporting it is not answerable "
             "from the capture, so both sides are carried and this panel shows the "
             "disagreement rather than resolving it. The latency tab is where the "
             "question of whether it MATTERS gets answered."))
    p.append(panel(
        "Engine calls by account", [
            q(f'sum by (codexlb_account_id) (rate({prom("codexlb.engine_calls")}{sel()}[$__rate_interval]))',
              "{{codexlb_account_id}}"),
        ], unit="reqps", opts=LEG, fieldcfg=STACK,
        desc="Upstream invocations attributed to each account - the thing the rate limits "
             "above are actually counting."))
    return p


# ---------------------------------------------------------------------------
# Tab 7 - Errors and transport
# ---------------------------------------------------------------------------
def tab_errors():
    p = []
    p.append(panel(
        "Error rate by type and code", [
            q(f'sum by (codexlb_error_type, codexlb_error_code) '
              f'(rate({prom("codexlb.errors")}{sel(filt=F_MODEL)}[$__rate_interval]))',
              "{{codexlb_error_type}} / {{codexlb_error_code}}"),
        ], unit="reqps", opts=LEG, fieldcfg=BARS, thresholds=ZERO_OK,
        desc="Errors carried in the archive - the provider's and the harness's, not this "
             "exporter's. For the exporter's own health use the Pipeline tab."))
    p.append(panel(
        "Errors by model and status", [
            q(f'sum by (gen_ai_request_model, codexlb_status, codexlb_error_type) '
              f'(increase({prom("codexlb.errors")}{sel(filt=F_MODEL)}[$__range]))', "", instant=True),
        ], "table", opts=TABLE_OPTS,
        desc="Errors concentrated on one model are a different problem from errors spread "
             "across all of them."))
    p.append(panel(
        "Transport events", [
            q(f'sum by (codexlb_frame_type, codexlb_family) '
              f'(rate({prom("codexlb.transport_events")}{sel(filt=F_ACCT)}[$__rate_interval]))',
              "{{codexlb_family}} / {{codexlb_frame_type}}"),
        ], unit="short", opts=LEG, fieldcfg=BARS,
        desc="WebSocket-level events - connection open, close, error. A close-code storm "
             "here explains latency and error spikes that look inexplicable at the "
             "response layer."))
    p.append(panel(
        "Safety buffering events", [
            q(f'sum by (gen_ai_request_model, gen_ai_response_model) '
              f'(rate({prom("codexlb.safety_buffering_events")}{sel(filt=F_ACCT)}[$__rate_interval]))',
              "{{gen_ai_request_model}} -> {{gen_ai_response_model}}"),
        ], unit="short", opts=LEG, fieldcfg=BARS,
        desc="The provider held the stream back for a safety check. Directly visible to "
             "the user as a stall, and invisible in every latency histogram that measures "
             "the engine rather than the stream."))
    p.append(panel(
        "Error records", [
            logq(f'{{service_name="codexlb2otel", codexlb_record_type="{rec("error")}"}}'),
        ], "logs", opts=LOGS_OPTS,
        desc="Full error records including error_message and error_code, with the thread "
             "and turn ids needed to find what the user was doing at the time."))
    p.append(panel(
        "Transport records", [
            logq(f'{{service_name="codexlb2otel", codexlb_record_type="{rec("transport")}", '
                 f'codexlb_family=~"$family"}}'),
        ], "logs", opts=LOGS_OPTS,
        desc="Individual transport frames with transport_event and frame_type."))
    return p


# ---------------------------------------------------------------------------
# Tab 8 - Conversation logs
# ---------------------------------------------------------------------------
def tab_logs():
    p = []
    p.append(text_panel("Conversation records", f"""
Nine record types reach Loki: **{', '.join(RECORD_TYPES)}**. Only three fields are stream
**labels** (`service_name`, `codexlb_family`, `codexlb_record_type`) - everything else rides as
**structured metadata**, which is queryable without opening a new stream per value. That split is the
entire reason this pipeline pushes to Loki natively instead of using OTLP logs.

Lines older than `loki.max_line_age` (3h) are dropped **here**, deliberately, rather than being handed
to Loki to discard silently behind a 204. Catching up on a backlog therefore produces a
`too_old` rejection count on the Pipeline tab and a gap here - that is the design working, not a fault.
"""))
    p.append(panel(
        "Record volume by type", [
            logq('sum by (codexlb_record_type) (count_over_time({service_name="codexlb2otel", '
                 'codexlb_record_type=~"$record_type"}[$__auto]))', "{{codexlb_record_type}}"),
        ], unit="short", opts=LEG, fieldcfg=STACK,
        desc="All nine types stacked. Ratios between them are stable in normal operation, "
             "so a change in shape is a change in how the agent is being used."))
    p.append(panel(
        "Record volume by family", [
            logq('sum by (codexlb_family) (count_over_time({service_name="codexlb2otel", '
                 'codexlb_family=~"$family"}[$__auto]))', "{{codexlb_family}}"),
        ], unit="short", opts=LEG, fieldcfg=BARS,
        desc="websocket vs unknown. A rising unknown share means frames are arriving in a "
             "shape the classifier does not recognise - worth investigating before it "
             "becomes a silent gap."))
    p.append(panel(
        "User prompts", [
            logq(f'{{service_name="codexlb2otel", codexlb_record_type="{rec("prompt")}"}}'),
        ], "logs", opts=LOGS_OPTS,
        desc="What was actually asked. Carries the same thread/session/window metadata as "
             "everything else, so any line here pivots straight into the matching turn."))
    p.append(panel(
        "Messages", [
            logq(f'{{service_name="codexlb2otel", codexlb_record_type="{rec("message")}"}}'),
        ], "logs", opts=LOGS_OPTS,
        desc="Conversation messages carrying full token accounting per line."))
    p.append(panel(
        "All records, live", [
            logq('{service_name="codexlb2otel", codexlb_record_type=~"$record_type", '
                 'codexlb_family=~"$family"}'),
        ], "logs", opts=LOGS_OPTS,
        desc="Everything, filtered by the tab variables. The catch-all view for when you "
             "do not yet know which record type holds the answer."))
    p.append(panel(
        "Search all record content", [
            logq('{service_name="codexlb2otel", codexlb_record_type=~"$record_type"} '
                 '|~ "(?i)$search"'),
        ], "logs", opts=LOGS_OPTS,
        desc="Case-insensitive regex over line content, driven by the $search variable at "
             "the top. Leave it as . to match everything."))
    return p


# ---------------------------------------------------------------------------
# Tab 9 - Traces
# ---------------------------------------------------------------------------
def tab_traces():
    p = []
    p.append(text_panel("Trace structure", f"""
One trace per **logical turn**. The root span is `{span('turn')}`; under it sit the critical-path
children - `{span('critical_path.pre_inference')}`, `{span('critical_path.engine_wall')}`,
`{span('critical_path.sampling_and_stream')}` and `{span('critical_path.other')}` - plus a
`{span('generateText')}`/`{span('streamText')} <model>` span per model call and an
`{span('execute_tool')} <tool>` span per tool
invocation. Span names follow the OTel GenAI convention of `<operation> <target>`, so the model and
tool name are in the span name as well as in attributes.

The same decomposition appears as histograms on the **Latency** tab. Use these when you need one
specific slow turn; use the histograms when you need the shape across all of them.
""".strip()))
    p.append(panel(
        "Slowest turns", [tempoq('{resource.service.name="codexlb2otel" && name="turn"} '
                                 '| by(trace:id) | max(duration) > 30s')],
        "table", opts=TABLE_OPTS,
        desc="Root spans over 30 seconds. Click a trace id to open the waterfall and see "
             "which critical-path child owns the time."))
    p.append(panel(
        "Recent turns", [tempoq('{resource.service.name="codexlb2otel" && name="turn"}')],
        "table", opts=TABLE_OPTS,
        desc="Every logical turn as a trace, newest first."))
    p.append(panel(
        "Tool execution spans", [tempoq('{resource.service.name="codexlb2otel" && '
                                        'name=~"execute_tool.*"}')],
        "table", opts=TABLE_OPTS,
        desc="One span per tool call, carrying the bounded arguments. The 16KB bound is "
             "applied at a rune boundary, so a truncated argument is still valid UTF-8."))
    p.append(panel(
        "Model call spans", [tempoq('{resource.service.name="codexlb2otel" && '
                                    'name=~"(generateText|streamText).*"}')],
        "table", opts=TABLE_OPTS,
        desc="The inference spans. Span name is `<operation> <model>`, so the model is "
             "greppable without opening the span, and the operation says whether the "
             "response streamed. Named generateText/streamText rather than the GenAI "
             "convention's `chat` because that is the vocabulary Grafana agent "
             "observability recognises - see attr's GenAIOperationGenerateText (#32)."))
    p.append(panel(
        "Critical path spans", [tempoq('{resource.service.name="codexlb2otel" && '
                                       'name=~"critical_path.*"}')],
        "table", opts=TABLE_OPTS,
        desc="The stage spans on their own, for when you want to compare one stage across "
             "many turns rather than all stages within one turn."))
    p.append(panel(
        "Errored turns", [tempoq('{resource.service.name="codexlb2otel" && '
                                 'name="turn" && status=error}')],
        "table", opts=TABLE_OPTS,
        desc="Traces whose root span carries error status. Empty is the expected state."))
    return p


# ---------------------------------------------------------------------------
# Tab 10 - Pipeline health (the exporter observing itself)
# ---------------------------------------------------------------------------
def tab_pipeline():
    p = []
    p.append(text_panel("This tab is about the exporter, not the agent", """
Everything on the other tabs describes codex-lb's traffic. This tab describes **codexlb2otel itself** -
whether it is keeping up, whether it is losing data, and whether its sinks are accepting what it sends.

**Ingest lag is the metric that catches every other failure first.** It is the only wall-clock
subtraction in the pipeline; all other timing is measured against the archive's own clock so that
catching up on a backlog does not look like an outage.

One trap worth stating: `files_reclaimed` staying flat while retention is enabled means deletion is
**failing**, not that nothing was due - a read-only archive mount returns EROFS on every attempt.
"""))
    p.append(panel(
        "Ingest lag", [q(f'{prom("codexlb.selfobs.ingest_lag_seconds")}{{{JOB}}}', "lag")],
        unit="s", opts=LEG, thresholds=LAG_STEPS,
        desc="Sustained growth means the tailer is falling behind the writer. Healthy is "
             "around the poll interval."))
    p.append(panel(
        "Read throughput", [
            q(f'rate({prom("codexlb.selfobs.bytes_read")}{{{JOB}}}[$__rate_interval])', "bytes/s", "A"),
        ], unit="Bps", opts=LEG,
        desc="Compressed archive bytes consumed per second. Spikes on startup as the "
             "backlog is worked through, then settles to the write rate."))
    p.append(panel(
        "Decode pipeline", [
            q(f'rate({prom("codexlb.selfobs.gzip_members_decoded")}{{{JOB}}}[$__rate_interval])',
              "gzip members/s", "A"),
            q(f'rate({prom("codexlb.selfobs.lines_decoded")}{{{JOB}}}[$__rate_interval])',
              "lines/s", "B"),
            q(f'rate({prom("codexlb.selfobs.turns_emitted")}{{{JOB}}}[$__rate_interval])',
              "turns emitted/s", "C"),
        ], unit="short", opts=LEG,
        desc="The three stages of reduction. Lines far exceeding turns is normal - many "
             "frames reduce to one turn."))
    p.append(panel(
        "Partial member reads", [
            q(f'rate({prom("codexlb.selfobs.partial_member_reads")}{{{JOB}}}[$__rate_interval])',
              "partial reads/s"),
        ], unit="short", opts=LEG, fieldcfg=BARS,
        desc="A gzip member cut off mid-write at the tail of the file. This is the NORMAL "
             "steady state of tailing a file being appended to, and a healthy rate here is "
             "the signal that ingestion is keeping up. Deliberately never conflated with "
             "decode errors below."))
    p.append(panel(
        "Data faults", [
            q(f'rate({prom("codexlb.selfobs.decode_errors")}{{{JOB}}}[$__rate_interval])',
              "decode errors/s", "A"),
            q(f'rate({prom("codexlb.selfobs.undecodable_lines")}{{{JOB}}}[$__rate_interval])',
              "undecodable lines/s", "B"),
        ], unit="short", opts=LEG, fieldcfg=BARS, thresholds=ZERO_OK,
        desc="A complete-but-corrupt gzip member, or a line that would not parse. Unlike "
             "partial reads above, these are real faults: counted, dropped, and the "
             "checkpoint moves past them so one bad byte cannot wedge the pipeline."))
    p.append(panel(
        "Archive files", [
            q(f'{prom("codexlb.selfobs.files_watched")}{{{JOB}}}', "watched", "A"),
            q(f'{prom("codexlb.selfobs.files_reclaimed")}{{{JOB}}}', "reclaimed (cumulative)", "B"),
            q(f'rate({prom("codexlb.selfobs.file_replacements")}{{{JOB}}}[$__rate_interval])',
              "replacements/s", "C"),
        ], unit="short", opts=LEG,
        desc="Watched should track the retention window. Replacements count a file "
             "recreated at the same name as different content - real behaviour, caught by "
             "fingerprinting the head rather than trusting the path."))
    p.append(panel(
        "Current file offset", [
            q(f'{prom("codexlb.selfobs.current_file_offset")}{{{JOB}}}', "{{codexlb_selfobs_file}}"),
        ], unit="bytes", opts=LEG,
        desc="How far into the live archive file the checkpoint has advanced. Sawtooth as "
             "each hour's file rolls over."))
    p.append(panel(
        "Reducer state", [
            q(f'{prom("codexlb.selfobs.open_responses")}{{{JOB}}}', "open responses", "A"),
            q(f'{prom("codexlb.selfobs.reducer_series")}{{{JOB}}}', "persisted series", "B"),
            q(f'{prom("codexlb.selfobs.reducer_threads")}{{{JOB}}}', "persisted threads", "C"),
            q(f'rate({prom("codexlb.selfobs.turns_evicted")}{{{JOB}}}[$__rate_interval])',
              "evicted/s", "D"),
        ], unit="short", opts=LEG,
        desc="Open responses growing without bound means completion frames are not "
             "arriving. Persisted series and threads are never pruned by design, so slow "
             "growth is expected and fast growth is not. Evictions are in-flight responses "
             "given up on - measured against the archive's watermark, not the wall clock, "
             "so a backlog does not evict everything on the first pass."))
    p.append(panel(
        "Sink backlog", [
            q(f'{prom("codexlb.selfobs.sink_pending")}{{{JOB}}}', "{{codexlb_selfobs_sink}}"),
        ], unit="short", opts=LEG,
        desc="Lines buffered and not yet pushed, per sink. Should hover near zero and "
             "return to it."))
    rej = prom("codexlb.selfobs.sink_rejections")
    rej_by = "{{codexlb_selfobs_sink}} / {{codexlb_selfobs_reason}}"
    p.append(panel(
        "Sink rejections (rate)", [
            q(f'sum by (codexlb_selfobs_sink, codexlb_selfobs_reason) '
              f'(rate({rej}{{{JOB}}}[$__rate_interval]))', rej_by),
        ], unit="short", opts=LEG, fieldcfg=BARS,
        desc="Deliveries refused, BY REASON - the reason matters far more than the count. "
             "too_old is the Loki max_line_age guard dropping backlog locally rather than "
             "letting Loki discard it behind a 204, and is expected while catching up. "
             "unauthorized or bad_request is a config fault that will NOT fix itself and "
             "holds the checkpoint until someone intervenes."))
    p.append(panel(
        "Sink rejections in range (cumulative)", [
            q(f'sum by (codexlb_selfobs_sink, codexlb_selfobs_reason) '
              f'(max_over_time({rej}{{{JOB}}}[$__range]))', rej_by, instant=True),
        ], "bargauge", unit="short",
        opts={"displayMode": "gradient", "orientation": "horizontal", "showUnfilled": True,
              "reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False}},
        thresholds=ZERO_OK,
        desc="The rate panel goes flat once rejections STOP, which reads identically to "
             "never having had any. This one keeps the count visible after the fact, "
             "which is what you actually want when asking 'did we lose anything today?'. "
             "A cumulative counter, so read it as a total rather than a rate."))
    p.append(panel(
        "Exporter log stream", [
            logq('{service_name="codexlb2otel", job="integrations/docker"} |~ "(?i)error|warn"'),
        ], "logs", opts=LOGS_OPTS,
        desc="codexlb2otel's OWN stdout, collected by the docker integration - a different "
             "stream from the conversation records it pushes. This is where a flush failure "
             "or a config fault says so in words."))
    return p


# ---------------------------------------------------------------------------
# Assembly
# ---------------------------------------------------------------------------
def grid(panels, widths=None, heights=None):
    """Lay panels out left-to-right, wrapping at 24 columns."""
    items, x, y, rowh = [], 0, 0, 0
    for i, pn in enumerate(panels):
        w = widths[i] if widths and i < len(widths) else 12
        h = heights[i] if heights and i < len(heights) else 8
        if x + w > 24:
            x, y = 0, y + rowh
            rowh = 0
        items.append({"kind": "GridLayoutItem", "spec": {
            "x": x, "y": y, "width": w, "height": h,
            "element": {"kind": "ElementReference", "name": f"panel-{pn['spec']['id']}"}}})
        x += w
        rowh = max(rowh, h)
    return {"kind": "GridLayout", "spec": {"items": items}}


def qvar(name, label, metric, tag, desc, allv=".*"):
    return {"kind": "QueryVariable", "spec": {
        "name": name, "label": label, "description": desc,
        "query": {"kind": "DataQuery", "group": "prometheus", "version": "v0",
                  "datasource": {"name": PROM},
                  "spec": {"qryType": 1, "query": f"label_values({metric}, {tag})",
                           "refId": f"var-{name}"}},
        "regex": "", "sort": "alphabeticalAsc", "refresh": "onTimeRangeChanged",
        "multi": True, "includeAll": True, "allValue": allv, "allowCustomValue": False,
        "skipUrlSync": False, "hide": "dontHide",
        "current": {"text": ["All"], "value": ["$__all"]}, "options": [],
    }}


def logvar(name, label, tag, desc):
    return {"kind": "QueryVariable", "spec": {
        "name": name, "label": label, "description": desc,
        "query": {"kind": "DataQuery", "group": "loki", "version": "v0",
                  "datasource": {"name": LOKI},
                  "spec": {"label": tag, "refId": f"var-{name}",
                           "stream": '{service_name="codexlb2otel"}', "type": 1}},
        "regex": "", "sort": "alphabeticalAsc", "refresh": "onTimeRangeChanged",
        "multi": True, "includeAll": True, "allValue": ".*", "allowCustomValue": False,
        "skipUrlSync": False, "hide": "dontHide",
        "current": {"text": ["All"], "value": ["$__all"]}, "options": [],
    }}


def build():
    tabs_def = [
        ("Overview", tab_overview(),
         [24, 4, 4, 4, 4, 4, 4, 12, 12, 12, 12, 12, 12],
         [5, 5, 5, 5, 5, 5, 5, 8, 8, 8, 8, 8, 9]),
        ("Model Usage", tab_models(),
         [24, 12, 12, 24, 12, 12, 8, 8, 8, 24, 12, 12],
         [7, 9, 9, 10, 9, 9, 9, 9, 9, 10, 9, 9]),
        ("Turns & Responses", tab_turns(),
         [12, 12, 12, 12, 12, 12, 12, 12, 24],
         [8, 8, 8, 9, 8, 9, 7, 7, 11]),
        ("Tokens & Cost", tab_tokens(),
         [24, 16, 8, 12, 12, 12, 24, 8, 8, 8, 24],
         [6, 9, 9, 9, 8, 8, 10, 7, 7, 7, 6]),
        ("Fast Mode", tab_fast_mode(),
         [24, 8, 8, 8, 12, 12, 12, 12, 12, 12],
         [7, 7, 7, 7, 9, 10, 10, 10, 9, 10]),
        ("Latency & Critical Path", tab_latency(),
         [24, 12, 12, 12, 12, 12, 12, 12, 12, 12, 12, 12],
         [7, 9, 9, 8, 8, 8, 9, 8, 9, 8, 9, 8]),
        ("Tools & Agents", tab_tools(),
         [12, 12, 12, 12, 12, 12, 12, 12],
         [8, 8, 8, 10, 10, 8, 11, 10]),
        ("Rate Limits & Accounts", tab_limits(),
         [12, 12, 12, 12, 12, 12],
         [8, 8, 8, 8, 8, 8]),
        ("Errors & Transport", tab_errors(),
         [12, 12, 12, 12, 12, 12],
         [8, 8, 8, 8, 11, 11]),
        ("Conversation Logs", tab_logs(),
         [24, 12, 12, 12, 12, 24, 24],
         [7, 8, 8, 10, 10, 12, 10]),
        ("Traces", tab_traces(),
         [24, 12, 12, 12, 12, 12, 12],
         [7, 9, 9, 9, 9, 9, 9]),
        ("Pipeline Health", tab_pipeline(),
         [24, 8, 8, 8, 12, 12, 12, 12, 12, 12, 12, 12, 24],
         [7, 7, 7, 7, 7, 7, 8, 7, 9, 7, 8, 8, 10]),
    ]

    elements, tabs = {}, []
    for title, panels, widths, heights in tabs_def:
        for pn in panels:
            elements[f"panel-{pn['spec']['id']}"] = pn
        tabs.append({"kind": "TabsLayoutTab", "spec": {
            "title": title, "layout": grid(panels, widths, heights)}})

    spec = {
        "title": "codexlb2otel - Full Telemetry",
        "description": (
            "Every signal codexlb2otel emits: all 57 metrics, all 9 Loki record types, and "
            "the trace tree. Twelve tabs, from agent behaviour through to the exporter's "
            "own health. Generated by dashboards/v2/generate.py, which fails the build if "
            "any declared metric or record type loses its last panel."),
        "tags": ["codexlb2otel", "codex-lb", "genai", "generated"],
        "editable": True,
        "preload": False,
        "liveNow": False,
        "cursorSync": "Crosshair",
        "links": [],
        "annotations": [{"kind": "AnnotationQuery", "spec": {
            "builtIn": True, "enable": True, "hide": True,
            "iconColor": "rgba(0, 211, 255, 1)", "name": "Annotations & Alerts",
            "legacyOptions": {"type": "dashboard"},
            "query": {"kind": "DataQuery", "group": "", "version": "v0", "spec": {}}}}],
        "timeSettings": {
            "from": "now-6h", "to": "now", "autoRefresh": "1m",
            "autoRefreshIntervals": ["30s", "1m", "5m", "15m", "1h"],
            "hideTimepicker": False, "fiscalYearStartMonth": 0, "timezone": "browser",
            "weekStart": "monday",
        },
        "variables": [
            qvar("model", "Model", "codexlb_responses_total", "gen_ai_request_model",
                 "Filters every panel whose metric carries gen_ai_request_model. Metrics "
                 "without that label (the selfobs family) deliberately ignore it."),
            qvar("account", "Account", "codexlb_responses_total", "codexlb_account_id",
                 "Grafana Cloud-style account id from the archive. Rate limits and credits "
                 "are per account."),
            qvar("kind", "Request kind", "codexlb_responses_total", "codexlb_request_kind",
                 "turn, prewarm, compaction, memory. These run CONCURRENTLY on one thread - "
                 "filtering to turn hides real spend."),
            logvar("record_type", "Log record type", "codexlb_record_type",
                   "Loki stream label. One of nine record kinds."),
            logvar("family", "Transport family", "codexlb_family",
                   "Loki stream label: websocket or unknown."),
            {"kind": "TextVariable", "spec": {
                "name": "search", "label": "Log search", "skipUrlSync": False,
                "hide": "dontHide",
                "description": "Case-insensitive regex applied to log line content on the "
                               "Conversation Logs tab. Leave as . to match everything.",
                "query": ".", "current": {"text": ".", "value": "."}}},
        ],
        "elements": elements,
        "layout": {"kind": "TabsLayout", "spec": {"tabs": tabs}},
    }
    return spec


def verify(spec):
    """Fail rather than ship a dashboard that quietly dropped a signal."""
    problems = []

    declared = set(ALL_METRICS)
    if declared != set(PROM_NAME):
        problems.append(f"ALL_METRICS and PROM_NAME disagree: "
                        f"{declared ^ set(PROM_NAME)}")

    # ALL_METRICS above is hand-maintained so this file runs standalone. The list
    # extracted from internal/attr/names.go is the actual source of truth, so diff the
    # two: a metric added to the exporter and not to this dashboard fails here rather
    # than silently never being plotted. Regenerate the sidecar with:
    #   rg -n '^\s+Metric[A-Za-z]+\s+=\s+"' internal/attr/names.go \
    #     | sed -E 's/.*= *"([^"]+)".*/\1/' | sort -u > dashboards/v2/.metrics_from_code.txt
    try:
        with open(os.path.join(os.path.dirname(os.path.abspath(__file__)),
                               ".metrics_from_code.txt")) as fh:
            from_code = {ln.strip() for ln in fh if ln.strip()}
    except OSError:
        from_code = None
    if from_code and from_code != declared:
        problems.append(f"drift vs internal/attr: only in code={sorted(from_code - declared)}, "
                        f"only in dashboard={sorted(declared - from_code)}")

    missing = declared - _covered_metrics
    if missing:
        problems.append(f"{len(missing)} metric(s) have no panel: {sorted(missing)}")

    missing_rt = set(RECORD_TYPES) - _covered_records
    if missing_rt:
        problems.append(f"record type(s) with no panel: {sorted(missing_rt)}")

    missing_sp = set(SPAN_NAMES) - _covered_spans
    if missing_sp:
        problems.append(f"span name(s) never referenced: {sorted(missing_sp)}")

    # Every element must be reachable from a tab, or it renders nowhere.
    referenced = set()
    for tab in spec["layout"]["spec"]["tabs"]:
        for item in tab["spec"]["layout"]["spec"]["items"]:
            referenced.add(item["spec"]["element"]["name"])
    orphans = set(spec["elements"]) - referenced
    if orphans:
        problems.append(f"elements not placed in any tab: {sorted(orphans)}")
    dangling = referenced - set(spec["elements"])
    if dangling:
        problems.append(f"layout references missing elements: {sorted(dangling)}")

    if problems:
        for pr in problems:
            print("COVERAGE FAILURE: " + pr, file=sys.stderr)
        raise SystemExit(1)

    print(f"coverage OK: {len(_covered_metrics)}/{len(declared)} metrics, "
          f"{len(_covered_records)}/{len(RECORD_TYPES)} record types, "
          f"{len(_covered_spans)}/{len(SPAN_NAMES)} span names, "
          f"{len(spec['elements'])} panels across "
          f"{len(spec['layout']['spec']['tabs'])} tabs", file=sys.stderr)


if __name__ == "__main__":
    dashboard = build()
    verify(dashboard)
    manifest = {
        "apiVersion": "dashboard.grafana.app/v2",
        "kind": "Dashboard",
        # The folder annotation must be in the manifest. Without it a push is free to
        # land the dashboard in General, silently detaching it from the codexlb2otel
        # folder it has always lived in.
        "metadata": {"name": "codexlb2otel-full",
                     "annotations": {"grafana.app/folder": "codexlb2otel"}},
        "spec": dashboard,
    }
    json.dump(manifest, sys.stdout, indent=2)
    sys.stdout.write("\n")
