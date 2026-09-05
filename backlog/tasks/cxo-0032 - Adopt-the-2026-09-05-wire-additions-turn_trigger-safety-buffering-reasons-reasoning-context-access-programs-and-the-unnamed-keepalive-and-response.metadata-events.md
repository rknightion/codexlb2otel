---
id: CXO-0032
title: >-
  Adopt the 2026-09-05 wire additions: turn_trigger, safety-buffering reasons,
  reasoning context, access programs, and the unnamed keepalive and
  response.metadata events
status: To Do
assignee: []
created_date: '2026-09-05 17:24'
labels: []
dependencies: []
references:
  - internal/frame/frame.go
  - internal/turn/reducer.go
  - internal/attr/attr.go
  - internal/profile/baseline/corpus.sig.json
  - docs/signals.md
priority: medium
type: enhancement
ordinal: 31000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
A full unsampled drift scan on 2026-09-05 (70 archives, 9.4 GB compressed, 25.0 GB decompressed, 9,962,295 lines, 0 undecodable) against the 2026-09-03 baseline produced 0 breaking and 215 new findings. The baseline was refreshed from that scan the same day (Rob authorised it), so the live drift gauge no longer reports these; this task carries the shapes that are worth adopting or naming so they are not lost in the refresh.

Shape additions worth adopting (all content-free field names; values are protocol enums, never conversation text):

- x-codex-turn-metadata gained turn_trigger (string; value set not yet inventoried) and node_repl_auto_review_required (seen as the string true). turn_trigger is the candidate: what caused the turn (a user message, an automation, a follow-up) is a bounded cohort dimension the current thread_source label cannot express. Inventory its distinct values over the corpus first, content-free, before deciding Bounded vs Identity.
- safety_buffering.reasons[] (string array) beside the retry_model that CXO-0022 adopted.
- response.reasoning.context with value current_turn, and response.parallel_tool_calls now observed true.
- response.access_programs.cyber (string) on created/in_progress/completed.
- Two protocol event types are not named by internal/frame and count as UNHANDLED in the profiler: keepalive (1 frame) and a bare response.metadata (6 frames, carrying headers.x-models-etag) distinct from the codex.response.metadata the frame package knows. Name both so the scan stops reporting them; keepalive needs no reduction.
- error events now carry sequence_number and the values error.code=server_is_overloaded / error.type=service_unavailable_error; confirm they flow through the existing error path and bounded ErrorCode label (cap 64).

Cardinality checks the same scan makes necessary:

- New request/response model values gpt-5.5 and gpt-6-astra. GenAIRequestModel is Bounded cap 32 with an Observed list of six models in internal/attr/attr.go:126; nothing in dashboards hard-codes model names (grep on 2026-09-05), so this is an Observed-list update plus confirmation the cap holds.
- Tool catalogue growth: response.create now carries a tools[] array (tool definitions with json-schema parameters) and tools[].tools[] for MCP namespaces. New top-level tool names: apply_patch, clock, create_goal, get_goal, update_goal, exec_command, write_stdin, view_image, list_mcp_resources, list_mcp_resource_templates, read_mcp_resource, mcp__codex_app; tool types tool_search and web_search; ~24 namespaced codex-app tools (create_thread, fork_thread, handoff_thread, wait_threads, set_thread_title, ...); output items named request_user_input_async and sleep with namespace clock. ToolName is Bounded cap 64 (attr.go:261, nine Observed values). Measure distinct emitted tool names on the corpus and decide whether the cap holds or MCP tools should carry the namespace as a separate bounded key; a silently capped tool label is the recurring-defect shape this repo already had once.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 turn_trigger's distinct values are inventoried content-free over the synced corpus and the field is adopted with the class that inventory justifies; the attribute is registered in internal/attr before any sink uses it and appears on the turn Loki record and span
- [ ] #2 keepalive and the bare response.metadata event are named in internal/frame and no longer reported UNHANDLED by a full scan; safety_buffering.reasons, reasoning.context, parallel_tool_calls and access_programs are either adopted as span/Loki fields or explicitly listed as deliberate omissions in docs/signals.md
- [ ] #3 Distinct tool names emitted from the corpus are measured; ToolName's cap and Observed list, and GenAIRequestModel's Observed list (gpt-5.5, gpt-6-astra), are updated so no live label is silently capped, with the measurement recorded in the notes
- [ ] #4 The embedded baseline is refreshed from a full unsampled scan only if this task changes what the decoder handles, and TestSignature_CarriesNoConversationContent passes
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
<!-- DOD:END -->
