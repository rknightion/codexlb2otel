---
id: CXO-0027
title: Preserve plaintext function-call arguments across content sinks
status: To Do
assignee: []
created_date: '2026-09-05 16:57'
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
- [ ] #1 Plaintext JSON arguments from completed function_call items appear in enabled Loki, Tempo and Agent Observability content paths wherever those sinks already expose tool inputs; existing custom-tool behavior remains covered.
- [ ] #2 Opaque encrypted argument values remain excluded, including values nested in collaboration calls; the exported representation makes any omission explicit and remains valid JSON where a sink requires JSON.
- [ ] #3 Capture limits and truncation or omission indicators remain explicit; original input length is retained and argument bodies never become metric labels.
- [ ] #4 Focused regression coverage distinguishes ordinary function calls, mixed plaintext/encrypted arguments and custom tools; signal documentation accurately states content coverage.
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
<!-- DOD:END -->
