---
id: CXO-0029
title: Preserve source ordering and capture times for conversation content
status: To Do
assignee: []
created_date: '2026-09-05 16:57'
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
- [ ] #1 Exported content has deterministic source-order information sufficient to interleave messages, tool calls, tool results and agent messages within the source request/response rather than grouping solely by type.
- [ ] #2 Input capture order is preserved in Agent Observability where its supported message roles allow it; Loki consumers have an explicit tie-breaker when content shares a timestamp.
- [ ] #3 Output item capture timestamps and available item identifiers are retained as log/span metadata, with timestamp provenance distinguishing observation time from server response completion and historical authorship.
- [ ] #4 Replayed history and file-boundary recovery preserve the existing deduplication contract without fabricating original times; identifiers and ordinals never become metric dimensions.
- [ ] #5 A documented example and focused regression evidence demonstrate interleaved input and multiple output items without leaking real conversation bodies.
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
<!-- DOD:END -->
