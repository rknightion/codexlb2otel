---
id: CXO-0013
title: Align emitted signals with Agent Observability SDK contracts
status: Done
assignee:
  - '@codex'
created_date: '2026-08-23 11:36'
updated_date: '2026-08-23 12:01'
labels: []
dependencies:
  - CXO-0012
references:
  - github.com/grafana/agento11y/go@v0.16.0
  - 'https://github.com/grafana/agento11y'
modified_files:
  - internal/correlation/ids.go
  - internal/correlation/ids_test.go
  - internal/attr/attr.go
  - internal/attr/names.go
  - internal/sink/agento11y/agento11y.go
  - internal/sink/agento11y/agento11y_test.go
  - internal/sink/agento11y/generation.go
  - internal/sink/agento11y/generation_test.go
  - internal/sink/otlpmetric/instruments.go
  - internal/sink/otlpmetric/record.go
  - internal/sink/otlpmetric/sink_test.go
  - internal/sink/otlptrace/ids.go
  - internal/sink/otlptrace/otlptrace.go
  - internal/sink/otlptrace/otlptrace_test.go
  - internal/sink/otlptrace/spans.go
priority: high
type: bug
ordinal: 13000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
Implement the confirmed compatibility fixes from CXO-0012 so Grafana Agent Observability can join direct Generation records to exported Tempo spans and populate its standard metrics without ingesting invalid model-less records. Preserve truthful source-data omissions and the current OTel-semconv token unit where it is more correct than the released SDK.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 Every exported Generation trace_id and span_id identify the corresponding exported Tempo response span
- [x] #2 Model-less transport/error turns are not submitted as invalid Generations, and zero usage fields are omitted
- [x] #3 Generation timestamps retain source fractional precision and unsupported prompt roles are excluded rather than emitted as UNSPECIFIED
- [x] #4 Response spans use CLIENT kind and carry SDK-compatible OK or ERROR status and exception evidence
- [x] #5 Agent Observability standard duration, token, TTFT, and tool-call histograms use the expected names, types, dimensions, and explicit boundaries where the SDK defines them
- [x] #6 Focused regression tests fail on the old behavior and pass on the fixed behavior
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [x] #1 make check passes: gofmt -l . reports nothing, go vet ./... clean, go test ./... green
- [x] #2 go build ./... succeeds
<!-- DOD:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Add failing generation tests for shared exported trace identity, fractional timestamps, sparse usage, invalid-record filtering, and unsupported-role filtering.
2. Introduce one shared deterministic correlation helper used by both direct Generations and Tempo response spans.
3. Add failing trace tests for CLIENT kind and success/error status, then implement SDK-compatible response-span completion.
4. Add failing metric contract tests for the standard tool-call instrument, dimensions, and SDK bucket advice; update instruments without weakening the semconv-correct token unit.
5. Run targeted packages, stage code for CodeRabbit review, fix Critical/Warning findings, then run make check and go build ./....
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Regression evidence: focused tests first failed on captured-vs-exported IDs, second-only RFC3339 timestamps, explicit zero usage members, unsupported-role messages, model-less buffering, INTERNAL/UNSET response spans, the custom tool-call metric name, default histogram buckets, and missing tool-call agent dimensions. After implementation, go test ./internal/correlation ./internal/sink/agento11y ./internal/sink/otlptrace ./internal/sink/otlpmetric passes. The slow corpus-backed internal/attr test also passed in 294.790s after adding error.category to the bounded contract.

CodeRabbit used the m7kni organisation plan and reported two valid Major findings plus one incorrect Minor. Fixed whitespace-only model filtering consistently across generation and tool metrics, and added the server TurnID as the final shared response-key fallback with a focused test. Dismissed the unit finding because both pinned SDK v0.16.0 and clone HEAD configure gen_ai.client.tool_calls_per_operation with unit count, not {tool_calls}. Focused tests pass after the review fixes.

Final verification: make check passed (gofmt -l . empty, go vet ./... clean, go test ./... green including internal/attr 202.473s and internal/sink/loki 206.294s); go build ./... succeeded. Final staged diff review and git diff --cached --check are clean. The second CodeRabbit review had no code findings; both tracker-only Info findings were fixed through the Backlog CLI.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Aligned direct Generations, Tempo response spans, and Agent Observability metrics with the released Grafana SDK contract. Generations now identify the exact exported response span, retain fractional timestamps, omit zero usage fields, exclude unsupported roles, and skip records without a model or stable identity. Response spans are CLIENT spans with bounded OK/ERROR and exception evidence. Standard app-facing histograms now use the SDK name, dimensions, error category, and explicit bucket advice while retaining the semconv-correct {token} unit. Verified through fail-first regressions, focused packages, two CodeRabbit reviews, make check, and go build ./....
<!-- SECTION:FINAL_SUMMARY:END -->
