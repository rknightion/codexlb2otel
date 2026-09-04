---
id: CXO-0007
title: >-
  Refresh the stale drift baseline: reasoning_effort "max" and request_kind
  "memory" are now in the capture
status: Done
assignee:
  - '@codex'
created_date: '2026-08-14 17:00'
updated_date: '2026-09-04 19:41'
labels:
  - enhancement
  - from-gh-issue
dependencies: []
priority: high
type: chore
ordinal: 7000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
Migrated from the former GitHub issue tracker.\n
Found incidentally while syncing the corpus for the summariser work, not by looking for it.
`corpus/sync` ran the drift probe over 10 new archives (2026-08-09T00 through T09, 740 MB):

```
5 finding(s): 0 breaking, 5 new, 0 info

  NEW  event.path.value  response.completed  response.reasoning.effort      new value "max"
  NEW  event.path.value  response.create     client_metadata.x-codex-turn-metadata
                                             new value "{\"request_kind\":\"memory\"}"
  NEW  event.path.value  response.create     reasoning.effort               new value "max"
  NEW  event.path.value  response.created    response.reasoning.effort      new value "max"
  NEW  event.path.value  response.in_progress response.reasoning.effort     new value "max"
```

**Neither needs a code change - checked, not assumed:**
- `request_kind: "memory"` is already handled. `internal/attr/attr.go:140` lists it in
  `Observed: []string{"turn", "prewarm", "compaction", "memory"}`, and `internal/turn/reducer.go:933`
  documents memory-consolidation responses running concurrently with the user's own turns, which is
  why `RequestKind` is half of the counter-diffing series key.
- `reasoning_effort: "max"` is a passthrough string (`reducer.go:441`, `t.Effort = c.Reasoning.Effort`),
  so a new enum value flows through as a new metric attribute value with no decoding change.

So this is a stale baseline and nothing else. The probe did its job: flagged a real wire-format
change as `new` rather than `breaking`, and stayed quiet about everything else.

Deliberately not folded into the summariser commit: refreshing the baseline is a full-corpus scan
over 3.9 GB and an unrelated change, so it gets its own commit rather than riding along where nobody
would look for it.

Second useful catch by the probe, and both times only because someone happened to run `corpus/sync`
rather than because anything watched for it - which is the argument for the scheduled-probe task.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 make baseline run as a FULL scan per the Makefile - a sampled scan must never set the baseline
- [x] #2 Refreshed corpus.sig.json committed
- [x] #3 TestSignature_CarriesNoConversationContent still passes: the new enum values were recorded as enums and no identifier- or content-shaped value came with them
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 make check passes: gofmt -l . reports nothing, go vet ./... clean, go test ./... green
- [x] #2 go build ./... succeeds
<!-- DOD:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
Wave 1 first lane: add the opaque attribution-items profiler rule, perform the authorised full baseline scan, and prove the privacy and zero-breaking gates.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
2026-09-03: this is no longer a plain refresh. After syncing today's 9 archive hours (corpus now 58 files, 7.2 GB, with a gap 2026-08-10 to 09-02 because the host keeps one day), just check is RED at probe-ci: 584 findings, 7 breaking, 576 new, 1 info (full output: codex/assessment-2026-09-03-drift-probe.txt). Live selfobs shows the decoder is NOT broken by them (830 undecodable of 18.2M lines in 7 days, 0 decode errors, http family still emitting), so the breaking findings are baseline staleness plus profiler gaps: sse-event-stream framing, the http request payload_shape, safety_buffering type change (reducer already handles it as RawMessage). 288+ of the new findings are response.usage.attribution.items.<id>.* where the keys are identifiers (at_<uuid>, ctc_<hex>), which exhausted the profiler's path budget and would put ids into corpus.sig.json. SEQUENCE: (1) teach internal/profile to record response.usage.attribution.items as an opaque keyed map (same mechanism as workspaces, opaqueKind), (2) full scan, (3) accept the baseline, (4) TestSignature_CarriesNoConversationContent must still pass. This lane must land FIRST in the wave because every other lane's just check fails at probe-ci until it does. Valuable new wire data surfaced by the same run is tracked separately (x-codex-turn-metadata agent_name/root_turn_id/sandbox_mode/window_number, prompt_cache_options, prompt_cache_diagnostics, safety_buffering.retry_model, additional_rate_limits secondary window).

L1 complete: added opaqueKind handling for response.usage.attribution.items as identifier_map with regression coverage, refreshed corpus.sig.json using ./bin/clbprobe -update corpus.sig.json corpus/processed from a full scan only, coverage sampled false, 58 files, 7.1GB read of 7.1GB. Before probe: 584 findings, 7 breaking, 576 new, 1 info. After full just probe and just check: clean, zero findings. Privacy proven by TestSignature_CarriesNoConversationContent and signature check showing zero response.usage.attribution.items child paths. Validation passed: gofmt check on owned Go files, go test -race ./internal/profile -run TestOpaque-or-TestSignature_CarriesNoConversationContent, go test -race ./internal/profile -run TestSignature_CarriesNoConversationContent, just lint, just build, just test-short, just probe, just check. Commit 1ad2396. Did not run a complete package race test because it stayed silent for several minutes after focused race coverage passed.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Treated attribution item IDs as an opaque identifier map, refreshed the embedded baseline from a full 7.1 GB scan, and proved zero drift and no conversation content. Final just check passed; the separate full corpus application suite remains unproven.
<!-- SECTION:FINAL_SUMMARY:END -->
