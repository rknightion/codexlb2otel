---
id: CXO-0040
title: >-
  Fix archive error-event decoding: the wrong key, the dead field, and the
  always-empty upstream status
status: To Do
assignee: []
created_date: '2026-09-12 10:07'
labels:
  - wire
  - telemetry
dependencies: []
priority: high
type: bug
ordinal: 39000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
A full-corpus drift scan on 2026-09-12 (74 files, 10,244,207 lines, 0 breaking findings) showed the error event is the one terminal frame the reducer barely reads, and three separate defects meet there.

The wire carries TWO error shapes. The rate-limit shape has `status_code` (number) alongside an `error` object and a `headers` object; the request-rejection shape has `status` (number) and neither. In the live capture the split was 9 of the first shape to 2 of the second. `internal/turn/reducer.go` `errorEvent` declares only `Status int json:"status"`, so the majority shape decodes to zero, and the field is never assigned to the Turn either way.

`turn.Turn.UpstreamStatusCode` is therefore fed exclusively by Postgres enrichment, and that column is null on essentially every row on this deployment (88 non-null out of 565,376 all-time; 0 of 6,309 in a 24h window). The attribute `codexlb.upstream.status_code` is consequently empty in production while the archive has been carrying the number all along.

`frame.Record.StatusCode` is decoded from the record envelope and read by nothing. It is null on every websocket record, which is 553,888 of 565,376 rows all-time.

`error.code` is absent from every error event in the corpus, so `Turn.ErrorCode` is only ever populated from the database. That is correct but undocumented, and it reads like a bug.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 errorEvent accepts both wire shapes: status_code and status, whichever is present, with status_code winning when both appear
- [ ] #2 The decoded HTTP status reaches turn.Turn and is emitted as codexlb.upstream.status_code when the archive supplies it
- [ ] #3 Postgres request_logs.upstream_status_code remains a fallback and never overwrites a value the archive supplied
- [ ] #4 frame.Record.StatusCode is either consumed or removed, and the choice is stated in a comment naming the observed null rate on the websocket family
- [ ] #5 A table-driven test covers both error shapes plus an error event with neither key, asserting the resulting Turn fields
- [ ] #6 A comment on Turn.ErrorCode records that the archive never carries error.code and that the field is database-sourced
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
<!-- DOD:END -->
