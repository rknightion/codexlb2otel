---
id: CXO-0022
title: >-
  Adopt the September wire-format additions: turn metadata, prompt-cache
  options, usage attribution
status: To Do
assignee: []
created_date: '2026-09-03 14:11'
labels:
  - enhancement
dependencies:
  - CXO-0007
priority: medium
type: enhancement
ordinal: 22000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
The 2026-09-03 drift probe over the synced corpus (codex/assessment-2026-09-03-drift-probe.txt) surfaced wire data the reducer does not read yet. The decoder is not broken by any of it (live selfobs: 0 decode errors), so this is adoption, not repair.

New on the wire since 2026-08-09, with what it answers:
- x-codex-turn-metadata gained agent_name, root_turn_id, sandbox_mode, window_number, context_window_id, auto_review_enabled, node_repl_disabled, node_repl_auto_review_required. agent_name names the subagent a child thread runs as; root_turn_id groups every response of one logical user turn across subagents; sandbox_mode is bounded.
- response.completed/created/in_progress gained prompt_cache_options{mode, ttl, comparison_response_id} and prompt_cache_diagnostics{type}: the server-declared prompt-cache mode per response, which is the missing half of the fast-mode and cache-hit story.
- response.completed response.usage.attribution.items.<id>.{input_tokens, output_tokens, cached_tokens, cache_write_tokens}: per-attribution-item token split, keyed by identifier (at_<uuid>, ctc_<hex>). Sum by kind only; never emit the keys.
- response.output_text.delta safety_buffering{retry_model, reasons, use_cases}: the re-run model is now explicit rather than inferred from response.model.
- codex.rate_limits additional_rate_limits.<limit>.secondary{used_percent, reset_at, reset_after_seconds, window_minutes}: a secondary window on the Spark quota (always 0 so far in Postgres).
- headers x-models-etag and x-codex-turn-state on response metadata; header value additions on originator and content-type.

Constraints: every new attribute key is a contract decision recorded in internal/attr with its class (Bounded with a cap, or Identity) and covered by TestEveryEmittedAttributeKeyIsOnContract; nothing new becomes a metric dimension in this task without a corpus-measured cardinality in the notes; gen_ai.agent.name keeps its Agent Observability contract (codex | codex/subagent) and the turn-metadata agent_name lands under a codexlb.* key; identifiers stay off metrics and labels.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 Turn carries root_turn_id, agent_name, sandbox_mode, window_number, prompt-cache mode/ttl/diagnostic, the safety-buffering retry model and per-kind attribution token sums, decoded from corpus fixtures with tests
- [ ] #2 Each field is present on the Loki turn body and the response span under a key registered in internal/attr with an explicit class; identifiers are Identity class
- [ ] #3 corpus.sig.json baseline accepted from a full scan after the profiler treats attribution.items as an opaque keyed map, and TestSignature_CarriesNoConversationContent passes
- [ ] #4 docs/signals.md lists the new fields and which are candidates for metric dimensions with their measured cardinality
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
<!-- DOD:END -->
