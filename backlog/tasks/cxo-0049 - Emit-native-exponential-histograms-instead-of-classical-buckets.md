---
id: CXO-0049
title: Emit native (exponential) histograms instead of classical buckets
status: To Do
assignee: []
created_date: '2026-09-15 13:00'
labels:
  - observability
  - cardinality
dependencies: []
priority: high
type: chore
ordinal: 48000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
codexlb2otel pushes 4,768 classical histogram bucket series into the m7kni stack (stack 1217581, Mimir tenant 2359401) across 23 _bucket families, measured 2026-09-15 with job="codexlb2otel".

Worst families:
  2010  gen_ai_client_token_usage_bucket
   480  gen_ai_client_time_to_first_token_seconds_bucket
   450  gen_ai_client_operation_duration_seconds_bucket
   336  gen_ai_client_tool_calls_per_operation_count_bucket
   324  codexlb_proxy_wait_seconds_bucket
   224  codexlb_turn_duration_seconds_bucket
   128  codexlb_proxy_first_token_seconds_bucket
   128  codexlb_proxy_latency_seconds_bucket

A classical histogram costs one series per bucket plus _sum plus _count. A native (base2 exponential) histogram is one series for the whole distribution at better resolution. The m7kni stack already carries ~9,000 native-histogram series so the ingest path is proven.

This exporter pushes OTLP straight to the gateway, so this is an SDK-side change only. No Alloy involvement and no remote-write protocol change: the OTLP gateway takes exponential histograms natively. (Alloy's convert_classic_histograms_to_nhcb is a different mechanism, only available on otelcol.exporter.prometheus, and is not relevant here.)

Cheapest route is the env var, no code change:
  OTEL_EXPORTER_OTLP_METRICS_DEFAULT_HISTOGRAM_AGGREGATION=base2_exponential_bucket_histogram

Explicit route, if per-instrument control is wanted, is a metric.View with metric.AggregationBase2ExponentialHistogram{MaxSize: 160, MaxScale: 20}.

Check whether any dashboard or alert uses histogram_quantile over a le label from these families before switching: native histograms need histogram_quantile(0.95, rate(metric[5m])) with no le, and the classical form breaks.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 Histogram instruments emit base2 exponential histograms by default, verified in the OTLP payload or via gcx metrics query returning no _bucket families for job=codexlb2otel
- [ ] #2 The aggregation choice is documented in the repo config reference, with the env var named
- [ ] #3 Any dashboard or alert querying these families with a le label is updated to the native histogram_quantile form
- [ ] #4 Post-change bucket series for job=codexlb2otel is 0 and native histogram series are present
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
<!-- DOD:END -->
