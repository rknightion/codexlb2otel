---
id: CXO-0028
title: Correlate tool results with invocations across response boundaries
status: To Do
assignee: []
created_date: '2026-09-05 16:57'
labels: []
dependencies: []
references:
  - internal/turn/reducer.go
  - internal/sink/otlptrace/spans.go
  - internal/sink/loki/record.go
  - internal/sink/agento11y/generation.go
priority: medium
type: bug
ordinal: 27000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
A tool result usually enters the next response.create after its invocation has already been emitted in the preceding response. Tempo currently joins only the current Turn.ToolCalls and Turn.ToolOutputs (internal/sink/otlptrace/spans.go:483-508). The reducer closes and removes that Turn at response.completed (reducer.go:169-176), then captures later results into another Turn. As a result, a tool span can lack its result even though Loki and the archive contain it.

A content-free scan of corpus/processed/2026-08-09T00.jsonl.gz on 2026-09-05 read 23,242 physical records, found 147 completed calls and 151 distinct call-ID outputs, and matched 145 outputs to earlier calls in that file. All 145 matched outputs were in a different response.create cycle with a later archive timestamp. The unmatched six are boundary/coverage unknowns, not proven tool failures. This is a targeted-hour measurement, not a full-corpus count.

The useful outcome is navigation from a tool result to the originating call and response across the conversation. Preserve the result as input to the receiving model response as well. Existing tool timing values do not establish exact tool execution start times. Traces are currently disabled in the inspected deployment, so source delivery and live trace proof must be reported separately.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 For a call followed by a result in a later response, Loki result metadata identifies the originating invocation and response when the archive provides an unambiguous match.
- [ ] #2 When Tempo is enabled, users can follow the relationship from the invocation to its later result without incorrectly attaching the result to an unrelated current-response call or assuming an ended span can be amended.
- [ ] #3 Correlation is scoped to the conversation, bounded in time and memory, and handles file boundaries, replay, duplicate results and missing calls explicitly; missing results do not imply tool failure.
- [ ] #4 Result content remains available as input to the receiving response, under existing capture limits; no exact execution duration or success status is invented from capture timestamps or item status.
- [ ] #5 Focused evidence covers a normal cross-response pair, repeated history, reused call identifiers in different threads, and unmatched boundary records.
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
<!-- DOD:END -->
