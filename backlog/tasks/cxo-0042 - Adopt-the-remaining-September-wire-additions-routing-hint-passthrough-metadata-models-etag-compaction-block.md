---
id: CXO-0042
title: >-
  Adopt the remaining September wire additions: routing hint, passthrough
  metadata, models etag, compaction block
status: To Do
assignee: []
created_date: '2026-09-12 10:08'
labels:
  - wire
  - telemetry
dependencies: []
priority: medium
type: enhancement
ordinal: 41000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
Four additive shapes appear in the current capture and in none of the embedded baseline. A full-corpus scan on 2026-09-12 found 72 new findings and 0 breaking, so every item here is a gain, not a migration.

1. Request header `x-codex-routing-hint`, present on 100 percent of records, value shaped `model=<name>`. It is the model the client asked codex-lb for at CONNECTION level, so it is fixed for the life of a websocket while response.create can name a different model per response. Four distinct values in the capture, each matching the response.create model exactly in every one of the 1,155 response.create events sampled. Its value is therefore not a new model dimension - it is the disagreement that matters, because a disagreement means codex-lb overrode the request (api_keys.enforced_model does exactly that). Emitting the hint as a per-turn attribute would duplicate an existing dimension at full cardinality and answer nothing.

2. `response.create input[].internal_chat_message_metadata_passthrough`, an object on input items carrying `turn_id` (string, 5,146 occurrences), `create_time` (float unix seconds, 3,822 occurrences) and `content_item_kinds` (array of string, 510 occurrences). The kinds are a bounded vocabulary: 18 distinct values in the capture, including a literal `unknown`. They name what each injected context block IS - base instructions, memories, host skills, permissions, collaboration mode, apps, plugin usage and recommendations, multi-agent hints and role instructions, agents-md instructions, environment context, hook additional context, model-switch instructions, and user text. That is a direct measurement of prompt composition, which nothing currently reports.

3. `codex.response.metadata` header `x-models-etag`, three distinct weak-etag values in the capture. It identifies the model-registry version the server answered from, so it dates a behaviour change to a registry roll rather than to anything local.

4. A `compaction` object inside `x-codex-turn-metadata`, on both the record header and response.create client_metadata, with keys `trigger`, `reason`, `implementation`, `phase`, `strategy` - all bounded, all short. It co-occurs with request_kind=compaction, which the code already knows. Nine occurrences in the capture, so it is rare and cheap. It answers why a compaction happened, which the existing request_kind cannot.

Also stale and cheap to fix while here: internal/attr `PlanType` records Observed as pro and business, but prolite is now live and appears in codex.rate_limits plan_type. Observed is documentation and the corpus test expectation, not a filter, so nothing broke - but the list is now wrong.

Deliberately EXCLUDED, and these exclusions must not be revisited by the implementer:
- `x-codex-turn-metadata.workspaces` is an object KEYED BY ABSOLUTE FILESYSTEM PATH of the operators local repositories. It is present on 187,430 records. It must never become a metric attribute, a Loki label, a span attribute or structured metadata. This is exactly the exposure class TestSignature_CarriesNoConversationContent exists to prevent.
- `auto_review_enabled`, `node_repl_disabled` and `node_repl_auto_review_required` are present on 248,526 records and take a single constant value. A constant attribute is pure cardinality cost and zero information.
- `context_window_id` is present on 248,526 records with 25 distinct values against 23 distinct thread ids, so it is thread-scoped and unbounded over time. Identity class only - never a metric attribute or a Loki label.
- `x-codex-turn-state` is a per-response encrypted blob and is already in the profile redaction list. It stays there.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 A counter records whether the connection-level routing hint agrees with the per-response model, as a bounded outcome attribute, without introducing the hint value itself as a dimension
- [ ] #2 Passthrough turn_id and create_time are reduced onto the Turn as Identity-class fields
- [ ] #3 content_item_kinds is counted per kind as a bounded attribute capped in internal/attr, with the observed vocabulary recorded
- [ ] #4 x-models-etag is reduced and emitted as an Identity-class field, not as a metric dimension
- [ ] #5 The compaction block trigger, reason, implementation, phase and strategy are reduced and emitted as bounded attributes
- [ ] #6 internal/attr PlanType Observed includes prolite
- [ ] #7 A test asserts that workspaces, auto_review_enabled and the node_repl fields produce no attribute on any sink
- [ ] #8 A test asserts context_window_id is classified Identity and is absent from metric attributes and Loki labels
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
<!-- DOD:END -->
