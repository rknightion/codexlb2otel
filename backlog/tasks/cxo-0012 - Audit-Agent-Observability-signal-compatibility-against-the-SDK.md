---
id: CXO-0012
title: Audit Agent Observability signal compatibility against the SDK
status: Done
assignee:
  - '@codex'
created_date: '2026-08-23 11:26'
updated_date: '2026-08-23 11:35'
labels: []
dependencies: []
references:
  - /Users/<user>/repos/agento11y
  - github.com/grafana/agento11y/go@v0.16.0
priority: high
type: spike
ordinal: 12000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
Compare codexlb2otel generation exports and its OTLP span and metric signals with the pinned Grafana Agent Observability Go SDK and the current cloned SDK repository. Record exact matches, semantic divergences, unsupported fields, and boundaries that require live Grafana read-back. This is validation only; do not change emitter behavior.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 Generation payload fields, wire types, enum encodings, timestamps, identifiers, usage, content, and tool data are compared against the SDK contract
- [x] #2 OTLP span names, attributes, events, status, timestamps, and links are compared against SDK output
- [x] #3 OTLP metric names, types, units, attributes, token semantics, and error handling are compared against SDK output
- [x] #4 The result separates proven compatibility, confirmed divergences, intentional omissions, and live-only unknowns
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [x] #1 make check passes: gofmt -l . reports nothing, go vet ./... clean, go test ./... green
- [x] #2 go build ./... succeeds
<!-- DOD:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Inventory codexlb2otel generation, span, and metric emission paths and their existing contract tests.
2. Treat the cloned repository proto as the current wire source of truth, then compare its Go SDK implementation and conformance fixtures with both our pinned v0.16.0 dependency and our emitted shapes.
3. Run focused compatibility probes that build representative codexlb2otel output and compare decoded fields and OTLP records, without contacting or mutating Grafana Cloud.
4. Record proven matches, divergences, intentional omissions, version drift, and the exact live read-back needed for remaining unknowns; run the repository gate and finalize the audit.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Proven compatible: the hand-written generation JSON decodes with the pinned real SDK protojson unmarshaller; pinned v0.16.0 and clone-current generation protos are byte-identical (SHA-256 bc5c44e9c5fed43fb96f920b8ae1900c271505bdd6aeba93d1ee6bb1a7946033); model/provider, IDs, modes, enum values, int64 string encoding, content/tool shapes, and inclusive token semantics match. Core SDK-facing operation-duration, token-usage, and TTFT metric types broadly match.

Confirmed divergences: Generation trace_id/span_id use the captured traceparent while the emitted Tempo response span uses deterministic thread/response IDs, so the Agent Observability loader requests a different trace. RFC3339 formatting loses fractional seconds. Zero token members are emitted as strings rather than omitted. Model-less transport/error turns are submitted even though server validation rejects them. Unknown/developer prompt roles map to UNSPECIFIED, which the SDK model does not accept. Response spans use INTERNAL rather than CLIENT kind and do not set OK/ERROR status or exception events. The standard SDK tool-call histogram is absent under its expected name, SDK bucket boundaries are not set on the three app-facing histograms, and operation-duration lacks error.category. Released-SDK cache/reasoning attribute spellings and units also differ, although the server accepts both spelling families and current OTel semantic conventions support {token}.

Intentional omissions: max_tokens, tool_choice, thinking_enabled, metadata/raw artifacts, parent generation IDs, stop reason, full tool schemas, richer tool-result facts, inline image payloads, and exact interleaving are not emitted because the reduced Turn does not carry reliable source facts. Tools/system prompts are deliberately deduplicated.

Live-only unknown: no Grafana Cloud stack was queried. A deployed read-back still needs the target stack selected, then a known generation ID checked across generation ingest results, Tempo trace lookup, app conversation rendering, and the standard metric series.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Audited generation JSON, OTLP spans, and OTLP metrics against github.com/grafana/agento11y/go v0.16.0 and clone HEAD 5da14422459dfe624fc84269cb6103fff403a09f. The generation envelope is protobuf-compatible, but trace correlation, model-less record filtering, response-span semantics, timestamp/zero-field shape, and standard metric shape need fixes. Verified with SDK conformance tests, codexlb2otel contract tests, make check, and go build ./.... No live Grafana stack was queried.
<!-- SECTION:FINAL_SUMMARY:END -->
