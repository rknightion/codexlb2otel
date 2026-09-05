---
id: CXO-0027
title: Preserve plaintext function-call arguments across content sinks
status: Done
assignee:
  - '@codex'
created_date: '2026-09-05 16:57'
updated_date: '2026-09-05 21:46'
labels: []
dependencies: []
references:
  - internal/turn/reducer.go
  - internal/sink/loki/record.go
  - internal/sink/otlptrace/spans.go
  - internal/sink/agento11y/generation.go
  - docs/signals.md
priority: high
type: bug
ordinal: 26000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
Function calls lose their argument body between archive reduction and export, so users cannot reconstruct what the model asked a tool to do. internal/turn/reducer.go:895-904 records arguments length and selected spawn fields but never sets ToolCall.Input; custom_tool_call does set Input. Loki record.go:103-113 and Tempo spans.go:503-508 consume that empty field, while docs/signals.md describes tool_call as tool name and arguments.

Evidence from the 2026-09-05 investigation: all 58 processed archive files contained 5,798 completed function calls with JSON argument objects, including 5,281 calls other than spawn_agent. A read-only Loki sample of 10 function-call records on 2026-09-05 14:00-15:00 UTC had nonzero input_chars and absent input. Reproduce the shape check with {codexlb_record_type="tool_call"} | json | kind="function", inspecting presence and lengths without publishing bodies.

Some nested argument values are opaque encrypted messages, including collaboration calls; preserving useful plaintext must not turn into exporting those blobs. Existing content-capture settings, privacy rules and sink size bounds remain the contract.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 Plaintext JSON arguments from completed function_call items appear in enabled Loki, Tempo and Agent Observability content paths wherever those sinks already expose tool inputs; existing custom-tool behavior remains covered.
- [x] #2 Opaque encrypted argument values remain excluded, including values nested in collaboration calls; the exported representation makes any omission explicit and remains valid JSON where a sink requires JSON.
- [x] #3 Capture limits and truncation or omission indicators remain explicit; original input length is retained and argument bodies never become metric labels.
- [x] #4 Focused regression coverage distinguishes ordinary function calls, mixed plaintext/encrypted arguments and custom tools; signal documentation accurately states content coverage.
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
Wave 2 delivered at aca5e5de0bcd4ba6f5fd72dfaa6caef3a6c7fb71. Synthetic reducer and sink race tests cover recursive encrypted-value omission, valid JSON, original byte length, bounds and custom tools. Post-deploy Loki function input observed. Tempo and Agent Observability proof is source-level; both remain disabled. just check passed at that source SHA; CI 33993480890 success. Watchtower deployed that SHA healthy, restart count 0. Publish 33993481221 failed signing after successful manifest push; run-level publication completion remains open. The single D20 corpus gate was canceled (exit 143), so corpus confidence is not proven. Final tracker closeout is local under the one-push contract.
<!-- SECTION:FINAL_SUMMARY:END -->
