---
id: CXO-0043
title: Widen Postgres enrichment to the populated request_logs columns it ignores
status: To Do
assignee: []
created_date: '2026-09-12 10:08'
labels:
  - enrichment
  - metrics
dependencies: []
priority: medium
type: enhancement
ordinal: 42000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
The enrichment query in internal/enrich/postgres.go selects 19 fields. Measured against the live schema on 2026-09-12, several of the columns it does select are structurally null on this deployment, and several columns it does not select are populated on nearly every row.

Fill rates over a 24h window, 6,236 rows, and all-time over 565,376 rows:

  Populated and NOT selected today:
    plan_type                 6,234 / 6,236
    conversation_id           6,174
    session_id                6,174
    service_tier              6,144      request-side tier reading
    actual_service_tier       6,144      server-side tier reading
    latency_ms                6,236      proxy-measured end to end
    latency_first_token_ms    5,805      proxy-measured TTFT
    transport                 6,236      websocket | http
    sticky_key_source           493      thread_header
    sticky_kind                 493      prompt_cache
    upstream_proxy_route_mode   297      direct

  Selected but effectively dead on this deployment, all-time non-null counts:
    latency_queue_ms                     7,819, last seen 2026-09-10
    latency_bridge_queue_wait_ms         3,354, none after 2026-08-29
    upstream_status_code                    88

  Never populated at all, all-time zero:
    bridge_stage, failure_exception_type, model_source_id, model_source_kind, source

Root cause for the dead ones, read from the codex-lb source at the deployed commit: prewarm_status, session_previous_gap_ms and latency_bridge_queue_wait_ms are only ever ASSIGNED in the http-bridge submit path. The websocket path reads request_state for them and nothing on that path ever sets them. 553,888 of 565,376 rows all-time are transport=websocket, and the residual http traffic is control-plane calls with an empty model. So they are structurally absent here rather than intermittently missing, and the existing codexlb.proxy.wait_coverage instrument already reports that honestly: over 24h it recorded response_create_gate present 6,043 and absent 143, with queue and bridge_queue absent 6,186 each.

requested_service_tier is a different case and must not be grouped with the dead ones. It was populated on 64,600 websocket rows and stopped on 2026-09-10 22:50, every value being the string priority. service_tier and actual_service_tier agree except on 168 rows in 30 days where service_tier was priority and actual_service_tier was empty. A requested-versus-granted tier reading is therefore real and is the metric that would show a priority downgrade.

Two correctness notes on the existing query. It does not filter deleted_at IS NULL, so a soft-deleted row would still join; there are none today, which is why this is latent. And request_id is NOT archive_request_id: they differed on 6,139 of 6,231 rows in 24h, request_id being resp-prefixed and archive_request_id ws-prefixed, so the point-query key must stay request_id.

Enum inventories measured over 24h, for capping in internal/attr:
  status: success, cancelled, error
  error_code: empty, client_disconnected, stream_incomplete, previous_response_owner_unavailable, upstream_unavailable, no_accounts, websocket_connection_limit_reached
  plan_type: prolite, business, pro
  useragent_group: codex-tui, Codex Desktop
  connection_request_kind: normal, prewarm, empty
  request_kind: normal, prewarm
  service_tier and actual_service_tier: default, auto, empty; historically priority
  upstream_transport: websocket, empty
  sticky_kind: prompt_cache; sticky_key_source: thread_header
  upstream_proxy_route_mode: direct
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 The enrichment row carries plan_type, conversation_id, session_id, service_tier, actual_service_tier, transport, sticky_kind, sticky_key_source and upstream_proxy_route_mode
- [ ] #2 requested_service_tier is selected and a requested-versus-granted tier outcome is emitted as a bounded metric attribute
- [ ] #3 Proxy-measured latency_ms and latency_first_token_ms are selected and emitted separately from the archive-derived timings, so the two can be compared rather than conflated
- [ ] #4 Both lookup and prefetch statements filter deleted_at IS NULL
- [ ] #5 The point-query key remains request_logs.request_id and a comment records that archive_request_id differs on the large majority of rows
- [ ] #6 Every newly attached bounded field has a capped internal/attr registry entry carrying its measured Observed set
- [ ] #7 A comment in internal/enrich names the columns that are structurally absent on a websocket-only deployment and why, so they are not re-added as a supposed fix
- [ ] #8 The widened query is proven to still use idx_logs_request_status_api_key_session_time rather than a sequential scan
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
<!-- DOD:END -->
