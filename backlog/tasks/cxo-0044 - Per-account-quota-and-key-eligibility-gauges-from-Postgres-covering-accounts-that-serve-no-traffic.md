---
id: CXO-0044
title: >-
  Per-account quota and key-eligibility gauges from Postgres, covering accounts
  that serve no traffic
status: Parked
assignee:
  - '@codex'
created_date: '2026-09-12 10:09'
updated_date: '2026-09-12 12:27'
labels:
  - enrichment
  - metrics
dependencies: []
priority: high
type: feature
ordinal: 43000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
Every quota gauge this service emits is derived from codex.rate_limits events in the archive, so an account only has a series while it is serving traffic. On 2026-09-12 the deployment held five accounts and Grafana carried codexlb_rate_limit_used_percent for two of them. The other three - including one configured burn_first with a completely fresh quota - were invisible.

That blind spot is not theoretical. A burn_first account added that morning was never routed to, because both API keys have account_assignment_scope_enabled true and the account was in neither keys api_key_accounts rows. Account scoping is applied to the candidate pool BEFORE routing policy is consulted, so the policy never got a look in. Nothing in telemetry could show it: the account had zero requests, therefore zero events, therefore no series. Diagnosis required a direct database session.

Postgres already holds the complete picture, refreshed roughly every two minutes for every account regardless of traffic.

  usage_history: account_id, recorded_at, window, used_percent, reset_at, window_minutes,
    credits_has, credits_unlimited, credits_balance. 68,891 rows, about 120 per hour.
    window is primary or secondary; input_tokens and output_tokens are null on every row.
    reset_at and credits_balance are null on the secondary rows.
  additional_usage_history: account_id, recorded_at, quota_key, limit_name, metered_feature,
    window, used_percent, reset_at, window_minutes. 73,157 rows, about 270 per hour.
    quota_key takes two values, limit_name two, metered_feature two, window primary or secondary.
  accounts: id, plan_type, status, routing_policy, limit_warmup_enabled, security_work_authorized,
    deactivation_reason. status is a Postgres enum with six labels: active, rate_limited,
    quota_exceeded, paused, reauth_required, deactivated. routing_policy observed as
    burn_first, normal, preserve.
  api_keys and api_key_accounts: give the eligibility join. An account is reachable by a key
    when the key is active AND (the keys account_assignment_scope_enabled is false OR a
    matching api_key_accounts row exists).

The read-only role codexlb2otel_ro was granted SELECT on usage_history, additional_usage_history and api_key_accounts on 2026-09-12; it already had request_logs, api_keys and accounts. Six tables total. The service still never creates roles or changes grants.

Traps, all measured:
- window is a RESERVED WORD in Postgres and must be quoted as "window" in every statement. An unquoted reference is a syntax error, not a silent wrong answer, so it fails loudly - but it fails.
- usage_history.window is nullable; COALESCE("window", primary) is what the tables own covering indexes are built on, so the query should match that expression or lose the index.
- accounts.id is shaped <uuid>_<8 hex>, while the archive account_id and therefore every existing codexlb.account.id attribute value is the BARE UUID. The database-sourced gauges must emit the uuid prefix, or they will not join with the archive-derived series and the two views will silently describe different things.
- accounts.email SHOULD be emitted, as a bounded attribute on the info gauge. Five accounts means five values, and the cap for every new bounded key this wave adds is 100 on the operators instruction of 2026-09-12, against a project cardinality budget of roughly 100,000 series. Personal data reaching the operators own Grafana stack is not this exporters concern, and an email is what makes an account identifiable in a dashboard without opening a database session. It must still never be written into the Git repository - not into a task, a test fixture, a comment or a commit message.
- The obvious implementation - query inside the async observable callback - is the deadlock shape this codebase has already shipped once. A callback is code the metrics library calls at a moment of its choosing, and the PeriodicReader default 30s timeout is what expires, not otlp.timeout. The poller must run on its own schedule and publish a snapshot; the callback may only read that snapshot under a short lock and must never perform IO.
- A database fault must disable these gauges and leave archive ingestion and every other sink running, exactly as the existing enrichment path does.

The new metrics are a separate family from the archive-derived codexlb.rate_limit.* gauges and do not replace them. Different source, different coverage, different freshness: merging them would make an absent series ambiguous. Frozen 2026-09-12 by the operator.

  codexlb.account.quota_used_percent        gauge, %,  by account id, window, plan type
  codexlb.account.quota_reset_after         gauge, s,  by account id, window
  codexlb.account.model_quota_used_percent  gauge, %,  by account id, quota key, window
  codexlb.account.credits_balance           gauge,     by account id
  codexlb.account.info                      gauge 1,   by account id, status, plan type, routing policy
  codexlb.account.api_key_eligible          gauge 0|1, by account id, api key name

Measured query cost against the live database: the latest-per-account reads plan on the recorded_at indexes and execute in under 1 ms each; the eligibility join is a five-by-two cross join executing in 0.11 ms.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 A poller reads the five account and quota tables on its own schedule and publishes an immutable snapshot
- [ ] #2 The observable callbacks read only that snapshot and perform no database IO, and a test proves a callback invoked while the poller holds its lock returns within a bounded time rather than blocking
- [x] #3 Every account in the accounts table produces a codexlb.account.info series whether or not it has served traffic
- [x] #4 codexlb.account.api_key_eligible is 0 for an account that no active key can reach and 1 otherwise, and a test covers the scope-enabled-but-unassigned case
- [x] #5 The account id attribute value is the bare uuid prefix and joins with the archive-derived rate limit series
- [x] #6 Every quoted identifier for the window column is present, and a test executes the statements against a stub rejecting an unquoted reserved word
- [ ] #7 A database fault disables only these gauges; archive ingestion, Loki and the existing metric path keep running, with the outcome visible in the self-observability counters
- [x] #8 The feature is disabled by default in config.example.yaml and TestLoad_ExampleConfigIsDeployable still passes
- [x] #9 accounts.email is selected and emitted as a bounded attribute on codexlb.account.info, so an account is identifiable in a dashboard without a database session
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
<!-- DOD:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Freeze account-poller config, snapshot and metric contracts.
2. Implement a scheduled read-only poller publishing immutable snapshots.
3. Wire non-blocking observable gauges and verify focused, integration and live evidence.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Park boundary: add an end-to-end test that publishes a real Poller snapshot and collects it through the registered observable callbacks while proving collection cannot wait on poller database I/O or locks; add the missing frozen poller self-observability counter contract; then enable account_poller in Camden configuration under deployment authority and verify codexlb_account_info live. The current code, read-only Postgres statements, disabled-by-default config, and dashboard panels are present, but AC2 and AC7 remain unproven and Camden configuration is intentionally unchanged.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Implemented the scheduled read-only account poller, immutable snapshots, account and eligibility gauges, disabled-by-default configuration, wiring, tests, dashboards, and live SQL proof. Parked because the required real Poller-to-registered-callback non-blocking seam test and a frozen poller self-observability counter are absent; Camden account_poller is also not enabled, so account gauges cannot be verified live.
<!-- SECTION:FINAL_SUMMARY:END -->
