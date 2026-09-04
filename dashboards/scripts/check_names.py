#!/usr/bin/env python3
"""Cross-checks every metric name, attribute/label key, and Loki JSON field path
referenced in dashboards/*.json and dashboards/alerts/*.yaml against the authoritative
source: internal/attr/names.go (attribute keys, metric instrument names),
internal/sink/otlpmetric/instruments.go (instrument type + unit, which decides the
Prometheus wire suffix), and internal/turn/turn.go (json tags, for the Loki-side
`| json alias="field.path"` extractions the lookup dashboard uses).

This does NOT talk to Grafana, Prometheus or Loki. It only proves internal consistency:
that every name this repo's dashboards/alerts reference is one internal/attr or
internal/turn actually defines, under the exact wire spelling those packages produce.
It cannot prove a panel renders, and does not try to.

Usage: python3 dashboards/scripts/check_names.py [repo_root]
Exit status: 0 if every reference resolves, 1 otherwise (with a listing of what did not).
"""
import json
import os
import re
import sys
import glob

try:
    import yaml
except ImportError:
    yaml = None


# ---------------------------------------------------------------------------
# 1. Parse internal/attr/names.go: attribute key constants and Metric* constants.
# ---------------------------------------------------------------------------

def parse_names_go(path):
    src = open(path).read()
    # Every `Identifier = "dotted.or.plain.name"` assignment inside the const blocks.
    # Deliberately broad (not scoped to a specific const block) - a false positive here
    # only means over-accepting a name, which this script's job is to catch as a
    # DASHBOARD-side problem, not to be a strict Go parser.
    assignments = re.findall(r'^\s*(\w+)\s*=\s*"([^"]*)"', src, re.MULTILINE)
    attr_consts = {}   # Go identifier -> dotted/plain wire value
    for ident, value in assignments:
        if value == "":
            continue
        attr_consts[ident] = value

    metric_names = {ident: value for ident, value in attr_consts.items() if ident.startswith("Metric")}
    return attr_consts, metric_names


def loki_key(dotted):
    return dotted.replace(".", "_")


# ---------------------------------------------------------------------------
# 2. Parse instruments.go: for every attr.MetricXxx used, what OTel instrument kind
#    and unit it was created with, which decides the Prometheus suffix.
# ---------------------------------------------------------------------------

INSTRUMENT_KIND_RE = re.compile(
    r'meter\.(Int64Counter|Float64Counter|Int64ObservableCounter|Int64Histogram|Float64Histogram|'
    r'Float64Gauge|Float64ObservableGauge|Int64ObservableGauge)\(\s*attr\.(\w+),'
)
UNIT_RE = re.compile(r'otelmetric\.WithUnit\("([^"]*)"\)')

# Standard-unit UCUM -> Prometheus suffix table (pkg/translator/prometheus's own
# mapping), restricted to the units this codebase actually declares.
UNIT_SUFFIX = {"s": "seconds", "%": "percent", "1": "ratio", "By": "bytes", "count": "count"}
# "_ratio" is appended for GAUGES ONLY per the translator's own comment
# (normalize_name.go: "Until these issues have been fixed, we're appending `_ratio`
# for gauges ONLY") - verified via context7 2026-08-07, not assumed. A hypothetical
# unit-"1" counter in this codebase would therefore get NO unit suffix at all; none
# exists today (MetricCreditsUnlimited, the only unit-"1" instrument, is a Gauge).
RATIO_GAUGE_ONLY_KINDS = {"Float64Gauge", "Float64ObservableGauge", "Int64ObservableGauge"}


def parse_instruments_go(path):
    src = open(path).read()
    # Split into per-call chunks: each `meter.Xxx(attr.MetricYyy, ...` up to the next
    # `must(attr.MetricYyy, err)` (or `must(attr.MetricAttrsRejected, err)` for the
    # observable-counter special case), so WithUnit is matched to the RIGHT instrument
    # instead of the next one in file order.
    results = {}
    for m in INSTRUMENT_KIND_RE.finditer(src):
        kind, metric_ident = m.groups()
        # Look for the next `must(` call after this match to bound the chunk, or the
        # observable-counter's own inline WithUnit before its callback.
        chunk_end = src.find("must(attr." + metric_ident, m.end())
        if chunk_end == -1:
            chunk_end = m.end() + 2000
        chunk = src[m.end():chunk_end]
        unit_m = UNIT_RE.search(chunk)
        unit = unit_m.group(1) if unit_m else ""
        results[metric_ident] = {"kind": kind, "unit": unit}
    return results


def prometheus_names(metric_ident, dotted_name, instrument_info):
    """Returns the set of concrete Prometheus metric names this instrument produces."""
    base = loki_key(dotted_name)
    info = instrument_info.get(metric_ident)
    if info is None:
        # Declared in names.go but never wired to an instrument (e.g. purely
        # documentation, or wiring this script's regex missed) - still register the
        # bare mangled name so a dashboard referencing it isn't falsely flagged, but
        # note it separately as unconfirmed.
        return {base}, False
    kind = info["kind"]
    unit = info["unit"]
    suffix = ""
    if unit == "1":
        if kind in RATIO_GAUGE_ONLY_KINDS:
            suffix = "ratio"
    else:
        suffix = UNIT_SUFFIX.get(unit, "")
    # Do not double a suffix the dotted name already ends with (e.g.
    # codexlb.selfobs.ingest_lag_seconds, unit "s", must stay *_seconds, not
    # *_seconds_seconds) - the real translator dedups this; verified against
    # opentelemetry-collector-contrib's pkg/translator/prometheus README via context7
    # 2026-08-07 (the standard-unit row: "Convert the unit ... and append") together
    # with the counter-example gen_ai_client_operation_duration_seconds_bucket this
    # lane confirmed LIVE against grafanacloud-prom, which never doubles.
    if suffix and base.endswith("_" + suffix):
        named = base
    else:
        named = base + ("_" + suffix if suffix else "")
    if kind in ("Int64Counter", "Float64Counter", "Int64ObservableCounter"):
        return {named + "_total"}, True
    if kind in ("Int64Histogram", "Float64Histogram"):
        return {named + "_bucket", named + "_sum", named + "_count"}, True
    # Gauges (Float64Gauge, Float64ObservableGauge, Int64ObservableGauge): unit suffix
    # only, no _total/_bucket family.
    return {named}, True


# ---------------------------------------------------------------------------
# 3. Parse internal/turn/turn.go: json tag names, one level of nesting for
#    critical_path.* (the only nested object the lookup dashboard's `| json`
#    extraction reaches into).
# ---------------------------------------------------------------------------

def parse_turn_json_tags(path):
    src = open(path).read()
    top_level = set(re.findall(r'json:"([a-zA-Z0-9_]+)', src))
    # CriticalPath struct fields specifically, for the "critical_path.X" paths the
    # lookup dashboard's timing-breakdown panel extracts.
    cp_block_m = re.search(r'type CriticalPath struct \{(.*?)\n\}', src, re.DOTALL)
    cp_fields = set()
    if cp_block_m:
        cp_fields = set(re.findall(r'json:"([a-zA-Z0-9_]+)', cp_block_m.group(1)))
    nested = {"critical_path." + f for f in cp_fields}
    return top_level, nested


# ---------------------------------------------------------------------------
# 4. Scan dashboards/*.json and dashboards/alerts/*.yaml for referenced names.
# ---------------------------------------------------------------------------

TOKEN_RE = re.compile(r'\b(codexlb_[a-zA-Z0-9_]+|gen_ai_[a-zA-Z0-9_]+|error_type|service_name)\b')
JSON_EXTRACT_RE = re.compile(r'\b\w+\s*=\s*"([a-zA-Z0-9_.]+)"')

# Tokens that are legitimately not attr/instrument names: PromQL/Loki structural
# vocabulary, or values (not keys) that happen to match the regex.
BUILTIN_ALLOW = {"service_name"}  # a real Loki/Prometheus label, not dotted in attr - handled specially


def extract_expr_strings(obj, out):
    if isinstance(obj, dict):
        if "expr" in obj and isinstance(obj["expr"], str):
            out.append(obj["expr"])
        for v in obj.values():
            extract_expr_strings(v, out)
    elif isinstance(obj, list):
        for v in obj:
            extract_expr_strings(v, out)


def collect_dashboard_exprs(path):
    d = json.load(open(path))
    out = []
    extract_expr_strings(d, out)
    return out


def collect_alert_exprs(path):
    if yaml is None:
        return []
    d = yaml.safe_load(open(path))
    out = []
    extract_expr_strings(d, out)
    return out


def source_contract(repo_root):
    """Return the metric and label names the checked-out exporter can emit."""
    names_go = os.path.join(repo_root, "internal", "attr", "names.go")
    instruments_go = os.path.join(repo_root, "internal", "sink", "otlpmetric", "instruments.go")
    selfobs_go = os.path.join(repo_root, "internal", "sink", "otlpmetric", "selfobs.go")
    attr_consts, metric_idents = parse_names_go(names_go)
    instrument_info = parse_instruments_go(instruments_go)
    if os.path.exists(selfobs_go):
        selfobs_attr_consts, _ = parse_names_go(selfobs_go)
        for ident, value in selfobs_attr_consts.items():
            if not ident.startswith("Metric"):
                attr_consts[ident] = value
        instrument_info.update(parse_instruments_go(selfobs_go))

    valid_attr_keys = {loki_key(dotted) for ident, dotted in attr_consts.items()
                       if not ident.startswith("Metric")}
    valid_attr_keys.update({"service_name", "codexlb_record_type"})
    valid_metric_names = set()
    for ident, dotted in metric_idents.items():
        names, _ = prometheus_names(ident, dotted, instrument_info)
        valid_metric_names |= names
    return valid_metric_names, valid_attr_keys


def validate_dashboard_object(repo_root, dashboard):
    """Return invalid emitted-name references in one generated dashboard object.

    This deliberately has no forward-reference allowlist: generated v2 dashboards
    must not claim a metric or label exists before its exporter does.
    """
    valid_metrics, valid_labels = source_contract(repo_root)
    exprs = []
    extract_expr_strings(dashboard, exprs)
    findings = []
    for expr in exprs:
        for tok in TOKEN_RE.findall(expr):
            if tok not in valid_metrics and tok not in valid_labels:
                findings.append(tok)
    return sorted(set(findings))


def main():
    repo_root = sys.argv[1] if len(sys.argv) > 1 else os.path.normpath(
        os.path.join(os.path.dirname(__file__), "..", ".."))
    names_go = os.path.join(repo_root, "internal", "attr", "names.go")
    instruments_go = os.path.join(repo_root, "internal", "sink", "otlpmetric", "instruments.go")
    selfobs_go = os.path.join(repo_root, "internal", "sink", "otlpmetric", "selfobs.go")
    turn_go = os.path.join(repo_root, "internal", "turn", "turn.go")
    dash_dir = os.path.join(repo_root, "dashboards")

    attr_consts, metric_idents = parse_names_go(names_go)
    instrument_info = parse_instruments_go(instruments_go)
    turn_top, turn_nested = parse_turn_json_tags(turn_go)

    # Self-observability (issue #8): its own attribute keys (codexlb.selfobs.file/
    # sink/reason) live in selfobs.go, not names.go's registry - attr.go's own doc
    # comment on why (it is keyed by a Field.Of func(*turn.Turn) string, and these
    # describe a file or a sink, not a Turn). Its instruments are also wired in
    # selfobs.go, not instruments.go, and use the *ObservableGauge/*ObservableCounter
    # constructors instruments.go's own metrics never do - merge both in.
    if os.path.exists(selfobs_go):
        selfobs_attr_consts, _ = parse_names_go(selfobs_go)
        for ident, value in selfobs_attr_consts.items():
            if not ident.startswith("Metric"):
                attr_consts[ident] = value
        instrument_info.update(parse_instruments_go(selfobs_go))

    # Build the full valid-wire-name allowlist: every attribute key mangled (Loki +
    # Prometheus label form are identical mangling, dots->underscores, per
    # attr.LokiKey's own doc comment and the live cross-check against Grafana Cloud
    # this lane ran against grafanacloud-prom/-logs 2026-08-07), plus every concrete
    # metric name each instrument actually produces on the wire.
    valid_attr_keys = set()
    for ident, dotted in attr_consts.items():
        if ident.startswith("Metric"):
            continue
        valid_attr_keys.add(loki_key(dotted))
    valid_attr_keys.add("service_name")  # ServiceName const itself has no dot
    valid_attr_keys.add("codexlb_record_type")

    valid_metric_names = set()
    unconfirmed_metric_names = set()
    for ident, dotted in metric_idents.items():
        names, confirmed = prometheus_names(ident, dotted, instrument_info)
        valid_metric_names |= names
        if not confirmed:
            unconfirmed_metric_names |= names

    # PLACEHOLDER metric names this lane invented for issue #8/#13, deliberately
    # flagged as not-yet-existing in every panel/alert that references them - see
    # dashboards/README.md's "forward references" table. Excluded from "unknown name"
    # findings so the report is not 100% noise from panels that say so themselves.
    forward_refs = {
        "ingest_lag_seconds",
        "codexlb_archive_drift_findings",
        "codexlb_open_responses",
        "codexlb_ingest_decode_errors_total",
        "codexlb_ingest_partial_member_reads_total",
        "codexlb_ingest_bytes_read_total",
        "codexlb_ingest_lines_decoded_total",
        "codexlb_ingest_checkpoint_offset_bytes",
        "codexlb_sink_batches_total",
        "codexlb_sink_batch_failures_total",
        "codexlb_sink_dropped_records_total",
        "codexlb_sink_rejections_total",
    }
    # These carry a `severity`/`sink`/`reason` label that isn't in attr.go's registry
    # either (they don't exist yet, so there is nothing to check them against) -
    # allow them alongside the metric names above rather than flagging as unknown
    # attribute keys.
    forward_ref_labels = {"severity", "sink", "reason"}

    findings = []  # (file, kind, token)

    def check_expr(fname, expr):
        for tok in TOKEN_RE.findall(expr):
            if tok in valid_metric_names or tok in valid_attr_keys or tok in forward_refs or tok in forward_ref_labels:
                continue
            # A metric name with a Prometheus wire suffix stripped might still be
            # legitimate (e.g. this script's suffix table is incomplete) - but every
            # name actually used in this dashboard set was generated against the
            # table above, so treat anything left over as a genuine finding.
            findings.append((fname, "unknown identifier", tok))

    for path in sorted(glob.glob(os.path.join(dash_dir, "**", "*.json"), recursive=True)):
        for expr in collect_dashboard_exprs(path):
            check_expr(os.path.relpath(path, repo_root), expr)

    for path in sorted(glob.glob(os.path.join(dash_dir, "alerts", "*.yaml"))):
        for expr in collect_alert_exprs(path):
            check_expr(os.path.relpath(path, repo_root), expr)

    # Loki `| json alias="field.path"` extractions: check the field.path side against
    # turn.go's own json tags (top-level, plus one level of critical_path.* nesting).
    loki_field_findings = []
    all_exprs = []
    for path in sorted(glob.glob(os.path.join(dash_dir, "**", "*.json"), recursive=True)):
        d = json.load(open(path))
        tmp = []
        extract_expr_strings(d, tmp)
        for e in tmp:
            all_exprs.append((os.path.relpath(path, repo_root), e))
    for fname, expr in all_exprs:
        if "| json" not in expr:
            continue
        for m in re.finditer(r'\|\s*json\s+([^|]+)', expr):
            fields_part = m.group(1)
            for alias, fieldpath in re.findall(r'(\w+)\s*=\s*"([a-zA-Z0-9_.]+)"', fields_part):
                if fieldpath in turn_top or fieldpath in turn_nested:
                    continue
                loki_field_findings.append((fname, "unknown turn json field", fieldpath))

    print("=== attr.go / instruments.go summary ===")
    print(f"attribute keys registered:        {len(valid_attr_keys)}")
    print(f"metric identifiers in names.go:    {len(metric_idents)}")
    print(f"metric wire names computed:        {len(valid_metric_names)}")
    if unconfirmed_metric_names:
        print(f"  (unconfirmed - no instruments.go wiring found for: {sorted(unconfirmed_metric_names)})")
    print(f"turn.go top-level json fields:      {len(turn_top)}")
    print(f"turn.go critical_path.* fields:     {len(turn_nested)}")
    print()
    print("=== dashboards/alerts reference check ===")
    if not findings and not loki_field_findings:
        print("PASS: every codexlb_*/gen_ai_* identifier and every Loki json field path "
              "referenced in dashboards/*.json and dashboards/alerts/*.yaml resolves against "
              "internal/attr/names.go + internal/sink/otlpmetric/instruments.go "
              "(metric/label wire names) or internal/turn/turn.go (Loki json field paths), "
              "or is an explicitly flagged issue #8/#13 forward reference.")
    else:
        print(f"FAIL: {len(findings)} unknown metric/label identifier(s), "
              f"{len(loki_field_findings)} unknown Loki json field path(s)")
        for fname, kind, tok in findings + loki_field_findings:
            print(f"  {fname}: {kind}: {tok!r}")

    return 1 if (findings or loki_field_findings) else 0


if __name__ == "__main__":
    sys.exit(main())
