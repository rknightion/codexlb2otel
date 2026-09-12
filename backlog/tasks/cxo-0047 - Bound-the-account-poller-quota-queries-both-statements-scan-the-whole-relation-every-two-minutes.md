---
id: CXO-0047
title: >-
  Bound the account poller quota queries: both statements scan the whole
  relation every two minutes
status: To Do
assignee: []
created_date: '2026-09-12 16:54'
updated_date: '2026-09-12 16:54'
labels:
  - enrichment
  - metrics
dependencies: []
priority: high
type: bug
ordinal: 46000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
internal/accountpoll/poller.go's usageSQL and modelQuotaSQL carry no WHERE clause. Each is a DISTINCT ON over the entire relation, ordered by recorded_at DESC, executed on every poll interval (two minutes) under a two-second query timeout.

Postgres has no loose index scan, so a DISTINCT ON whose ORDER BY matches an index still walks the whole index and dedups. Cost therefore grows linearly with table size, and the tables only grow:

  usage_history               ~69k rows, ~120 new rows per hour
  additional_usage_history    ~74k rows, ~270 new rows per hour

At those rates the additional_usage_history walk roughly triples within a year. The failure mode is silent and gradual: the poll starts exceeding query_timeout, the optional signal degrades exactly as designed, and every account gauge goes stale while archive ingestion stays green. Nothing pages.

Wave 4 measured index use for the enrichment lookup and prefetch statements only. Neither poller quota statement has ever been EXPLAINed against the live database, before or after the to_timestamp repair. The repair itself is projection-only and does not affect the plan, but that is an argument about the repair, not evidence about the statement.

The bound must preserve two frozen properties: the latest-window expression stays COALESCE(\"window\", 'primary') so it matches the tables' own covering indexes, and every reference to the reserved word window stays quoted. A recorded_at lower bound must be wide enough that an account whose poller row is older than the window still produces a series, or this trades a slow query for a disappearing account - the exact blind spot CXO-0044 was opened to close.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 EXPLAIN (ANALYZE, BUFFERS) output for both usageSQL and modelQuotaSQL against the live read-only DSN at current table size is captured, showing the chosen plan node and actual rows scanned
- [ ] #2 Both statements are bounded so their cost does not grow with total table size, with the plan proving it
- [ ] #3 The latest-window expression is still COALESCE(quoted window, 'primary') and every reserved-word reference is still quoted, with the existing stub test that rejects an unquoted reference still passing
- [ ] #4 A test proves an account whose most recent quota row predates the bound still produces its codexlb.account.info series and is not silently dropped
- [ ] #5 just check passes and the deployed poller shows zero new failures over at least three poll intervals after rollout
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
<!-- DOD:END -->
