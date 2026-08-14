---
id: CXO-0007
title: >-
  Refresh the stale drift baseline: reasoning_effort "max" and request_kind
  "memory" are now in the capture
status: To Do
assignee: []
created_date: '2026-08-14 17:00'
labels:
  - enhancement
  - from-gh-issue
dependencies: []
references:
  - archive/issues-dump.json
priority: medium
type: chore
ordinal: 7000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
Migrated from GitHub issue #39 (full body in `archive/issues-dump.json`).

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
- [ ] #1 make baseline run as a FULL scan per the Makefile - a sampled scan must never set the baseline
- [ ] #2 Refreshed corpus.sig.json committed
- [ ] #3 TestSignature_CarriesNoConversationContent still passes: the new enum values were recorded as enums and no identifier- or content-shaped value came with them
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 make check passes: gofmt -l . reports nothing, go vet ./... clean, go test ./... green
- [ ] #2 go build ./... succeeds
<!-- DOD:END -->
