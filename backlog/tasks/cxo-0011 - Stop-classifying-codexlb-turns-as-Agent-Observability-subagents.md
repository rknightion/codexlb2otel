---
id: CXO-0011
title: Stop classifying codexlb turns as Agent Observability subagents
status: Done
assignee:
  - '@codex'
created_date: '2026-08-23 11:16'
updated_date: '2026-08-23 11:23'
labels: []
dependencies: []
references:
  - >-
    https://github.com/grafana/agento11y/blob/main/plugins/copilot/README.md#all-options
modified_files:
  - internal/attr/attr.go
  - internal/attr/attr_test.go
  - internal/attr/names.go
  - internal/sink/agento11y/generation_test.go
  - internal/sink/otlpmetric/sink_test.go
  - internal/sink/otlptrace/spans_semconv_test.go
priority: high
type: bug
ordinal: 11000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
codexlb2otel emits agent names such as codexlb/codex-tui. OpenTelemetry permits the value, but Grafana Agent Observability interprets a slash as a subagent marker, so normal turns are misclassified and agent filters and counts break.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 All emitted generation, metric, and trace agent names for known originators contain no slash and preserve distinct originator identity
- [x] #2 An absent originator still emits the stable codexlb fallback rather than becoming anonymous
- [x] #3 Targeted tests and the repository gate pass
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [x] #1 make check passes: gofmt -l . reports nothing, go vet ./... clean, go test ./... green
- [x] #2 go build ./... succeeds
<!-- DOD:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Change the pinned agent-name expectations to slash-free codexlb-<originator> values and observe the targeted failure.
2. Change the shared AgentName prefix and update its contract comments and bounded registry catalogue.
3. Run targeted tests, make check, and go build ./...; review the staged code; then finalize, commit, and push.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Added a focused AgentName regression test covering all three known originators and the missing-originator fallback. Observed it fail against the slash prefix, then changed the shared prefix to a hyphen and updated generation, metric, trace, and bounded-registry expectations. Targeted tests pass.

Verification: the focused regression test failed before the production change and passed after it; generation wire-format, OTLP metric, and OTLP trace contract tests passed; make check passed including the 206-second corpus-backed internal/attr package; go build ./... passed; CodeRabbit reviewed all seven staged files with zero findings.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Changed the shared agent-name prefix from codexlb/ to codexlb- so Grafana Agent Observability no longer classifies ordinary turns as subagents. Preserved distinct originator identities and the non-anonymous codexlb fallback. Verified every sink contract, the full repository gate, the full build, and a zero-finding CodeRabbit review.
<!-- SECTION:FINAL_SUMMARY:END -->
