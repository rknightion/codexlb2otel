---
id: CXO-0029
title: Preserve source ordering and capture times for conversation content
status: Done
assignee:
  - '@codex'
created_date: '2026-09-05 16:57'
updated_date: '2026-09-05 21:46'
labels: []
dependencies: []
references:
  - internal/turn/turn.go
  - internal/turn/reducer.go
  - internal/sink/loki/timestamps.go
  - internal/sink/agento11y/generation.go
priority: medium
type: enhancement
ordinal: 28000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
Conversation exports retain text but lose ordering evidence already present in the archive. The reducer splits input items and output items into separate typed slices without per-item ordering metadata. Agent Observability inputMessages explicitly emits every prompt before every tool result (generation.go:310-318), and Loki assigns one response-level timestamp to all output messages and calls (timestamps.go:29-43). Users reviewing an interleaved exchange cannot reliably establish which visible message or tool call came first.

In a targeted read of corpus/processed/2026-08-09T00.jsonl.gz (23,242 records), 29 input-message occurrences appeared after a tool-output item in the same input array. These include replayed history and are not a count of newly emitted ordering defects. Input array order and output frame timestamps/sequence are nevertheless available source evidence that the current Turn content types discard.

Scope is the observable conversation timeline. An archive capture timestamp is not the original authorship time of replayed history, and a tool-call frame timestamp is not proof of actual tool execution start.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 Exported content has deterministic source-order information sufficient to interleave messages, tool calls, tool results and agent messages within the source request/response rather than grouping solely by type.
- [x] #2 Input capture order is preserved in Agent Observability where its supported message roles allow it; Loki consumers have an explicit tie-breaker when content shares a timestamp.
- [x] #3 Output item capture timestamps and available item identifiers are retained as log/span metadata, with timestamp provenance distinguishing observation time from server response completion and historical authorship.
- [x] #4 Replayed history and file-boundary recovery preserve the existing deduplication contract without fabricating original times; identifiers and ordinals never become metric dimensions.
- [x] #5 A documented example and focused regression evidence demonstrate interleaved input and multiple output items without leaking real conversation bodies.
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [x] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
<!-- DOD:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
Wave 2: commit root-owned seams first; frozen owned lanes implement with synthetic tests; integrate, just check, one corpus confidence gate and CodeRabbit; one push; watchtower-only deploy observation and m7kni proof; reconcile acceptance by evidence layer.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Clean main e8e97fd directly descends from c567894 and equals origin/main. CI 33988760737 and release-please 33988761017 succeeded. D8 holds; traces and agento11y remain disabled.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Wave 2 delivered at aca5e5de0bcd4ba6f5fd72dfaa6caef3a6c7fb71. Synthetic reducer and sink tests verify exact ItemID-to-ordinal mapping, capture times, replay and supported role interleaving. Loki tie-break metadata and documented scoped query verified; disabled sink behavior source-level only. just check passed at that source SHA; CI 33993480890 success. Watchtower deployed that SHA healthy, restart count 0. Publish 33993481221 failed signing after successful manifest push; run-level publication completion remains open. The single D20 corpus gate was canceled (exit 143), so corpus confidence is not proven. Final tracker closeout is local under the one-push contract.
<!-- SECTION:FINAL_SUMMARY:END -->
