---
id: CXO-0047
title: >-
  Bound the account poller quota queries: both statements scan the whole
  relation every two minutes
status: To Do
assignee: []
created_date: '2026-09-12 16:54'
updated_date: '2026-09-12 17:00'
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

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
## Live EXPLAIN, 2026-09-12, deployed database, read-only

Measured, not estimated. Full plans in the gitignored codex/wave5-poller-explain-2026-09-12.md.

Row counts: accounts 5, api_keys 2, usage_history 69,729, additional_usage_history 75,045.

usageSQL: 326.6 ms. Parallel seq scan over the whole relation, external merge sort spilling
3,136 kB to disk, to return 8 rows. Separately, the correlated credits_balance subquery runs
once per history row - 69,729 index searches, 278,916 shared buffer hits - to produce at most
one value per account. Roughly 2.2 GB of buffer traffic every two minutes.

modelQuotaSQL: 204.0 ms. Seq scan, external merge sort spilling 9,544 kB, to return 9 rows.

REJECTED, tested: dropping the COALESCE changes nothing. additional_usage_history.window is
NOT NULL with zero null rows, so its COALESCE is dead weight, but removing it still gave
205.9 ms with the same seq scan and the same 9,544 kB sort. Postgres has no loose index scan,
so DISTINCT ON over an unbounded relation reads everything whatever the index. The index
choice is not the defect; the unbounded DISTINCT ON is.

usage_history.window IS nullable in the schema, currently zero null rows, and has a matching
expression index on COALESCE(window,'primary'). Its COALESCE must stay. Wave 4 justified the
COALESCE on BOTH tables as matching the covering indexes - correct for usage_history, wrong
for additional_usage_history, whose indexes are all on the raw column.

WORKS, tested: driving the probe off the small accounts relation with CROSS JOIN LATERAL and
ORDER BY recorded_at DESC LIMIT 1 gives 26.2 ms, same 9 rows, 15 index searches over 54
buffers instead of 75,045 scanned rows. Residual defect in that plan: 25.9 of the 26.2 ms is
the SELECT DISTINCT quota_key, window driving-set discovery, still a growing seq scan.
Bounding that is part of this task.

Every index the rewrite needs already exists. This task creates no index and issues no DDL.

Urgency: query_timeout is 2s and usageSQL is at 326 ms on a base growing ~120 rows/hour, so
it doubles in roughly 24 days. Two to three doublings reach the timeout, at which point the
poll fails, every account gauge goes stale, archive ingestion stays green and nothing pages.
<!-- SECTION:NOTES:END -->
