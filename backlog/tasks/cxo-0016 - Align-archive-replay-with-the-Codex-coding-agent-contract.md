---
id: CXO-0016
title: Align archive replay with the Codex coding-agent contract
status: Done
assignee:
  - '@codex'
created_date: '2026-08-23 16:37'
updated_date: '2026-08-23 16:58'
labels: []
dependencies: []
references:
  - /Users/rob/repos/agento11y/plugins/codex/README.md
  - >-
    /Users/rob/repos/agento11y/plugins/agento11y/internal/agents/codex/mapper/mapper.go
priority: high
type: bug
ordinal: 16000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
The live Agent Observability UI exposes archive-replay records as application-style generations rather than the coding-agent shape emitted by agento11y/plugins/codex. This fragments Codex into originator-specific agents, creates sparse or misleading agent versions because prompt and tool bodies are deduplicated across responses, hides executed tools from the selected version, and drops model-less error or transport turns. Reconcile only fields for which the archive has truthful evidence, preserving the richer per-response timing and deterministic trace correlation.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 Main Codex traffic uses the coding-agent identity and only proven subagent traffic receives the slash subagent marker; originator remains queryable as metadata
- [x] #2 Every generation with stable identity has one stable effective version derived from the source instruction and tool fingerprints, so sparse prompt and tool bodies accumulate under the same catalog version
- [x] #3 Generation tool definitions include tools actually called during the response even when the deduplicated request catalog body is absent
- [x] #4 Model-less error and transport records remain visible under an explicit unknown model instead of being silently skipped
- [x] #5 Successful completed responses carry the coding-agent completion outcome while source-derived stream mode and error evidence stay truthful
- [x] #6 Focused regressions cover the previous coding-agent incompatibilities and the repository gate passes
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [x] #1 make check passes: gofmt -l . reports nothing, go vet ./... clean, go test ./... green
- [x] #2 go build ./... succeeds
<!-- DOD:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Pin the Codex plugin identity, subagent, model fallback, completion, tool-definition, and effective-version behavior with fail-first sink and attribute tests. 2. Introduce a stable effective-version digest over the archive instruction and tool fingerprints, and align the shared agent identity without weakening originator metadata. 3. Merge executed tools into Generation tool definitions, retain model-less identified turns as unknown, and set completed only from successful source status. 4. Run focused packages, review the staged code with CodeRabbit, then run make check and go build ./.... 5. Re-query a fresh deployed conversation and agent catalog if exact-head deployment is available; report live verification separately from source and test evidence.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Live m7kni evidence before the fix: the last-hour Analytics view showed 920 requests split across five agent rows; codexlb-codex-tui carried 773 requests while codexlb-codex_exec appeared as a separate zero-request agent. A 23-call Codex conversation initially rendered its direct Generation as a separate 0 ms tail node until Tempo caught up; a later gcx read returned 25 joined generations with matching trace_id/span_id, proving eventual-consistency lag rather than a remaining correlation mismatch. The selected agent version showed 0 tools although the conversation repeatedly called exec. Catalog read-back found 987 generations under an empty-prompt namespace-only effective version, six under an instructions-hash version with zero tools, and one prompt-bearing version: sparse bodies were fragmenting one logical coding-agent version.

Fail-first evidence: TestAgentNameMatchesCodexCodingAgentIdentity failed for all ordinary originators and a proven subagent against the old codexlb-<originator> mapping. After implementation, focused agento11y, OTLP trace, and OTLP metric packages pass. The implementation now uses codex/codex-subagent identity by source thread type, a full SHA-256 effective version over repeated instruction and tool fingerprints, executed-tool fallback definitions, codex/unknown for model-less identified records, and completed only for successful source outcomes.

CodeRabbit used the rknightion organisation plan. Its Major finding was valid: executed-tool fallback definitions must retain a non-empty source ToolCall.Kind (including custom) and use function only when absent. Fixed with a regression expectation. Its tracker-only Minor request to replace the two absolute cross-repository agento11y references was dismissed: these are deliberately external to this repository and the user supplied the exact absolute checkout path as the authoritative source.

Final verification: the focused agento11y, OTLP trace, and OTLP metric packages pass; the second CodeRabbit review reported zero findings; make check passed with gofmt -l . empty, go vet ./... clean, and go test ./... green including internal/attr in 211.772s and internal/sink/loki in 215.805s; the chained go build ./... succeeded. Live post-deploy behavior remains unproven until this exact head is deployed and fresh data ages through both Generation and Tempo ingestion.

Post-deploy validation: publish workflow 32652897866 succeeded at exact head ceb2d05399f9c902a8cdff8e72fe567bf96a15da. Watchtower replaced Camden's stale f18d7f78 image with that revision at 2026-08-23T16:55:05Z; the replacement container was healthy and all four sinks initialized without startup errors.

Fresh m7kni read-back after the restart showed ordinary generations under codex and source-proven child traffic under codex/subagent. Three active conversations held 12, 5, and 14 post-deploy ordinary generations on the same effective version sha256:1bb61e77...; a separate sha256:66a820f9... version carried a genuinely different three-tool catalog rather than sparse-body churn. The selected versions exposed executed exec with its custom type, and the latter also retained collaboration/functions namespace definitions. Three checked direct Generation span IDs resolved to the exact SPAN_KIND_CLIENT streamText spans with OK status in Tempo.

Deployed-person UI validation used the signed-in Grafana Agent Observability app: Analytics displayed codex and codex/subagent as fresh rows while legacy codexlb-* rows remained historical; conversation 01a02f78-c7e0-7193-8b49-2b9ef8e344ca grouped the post-restart tail under agent codex with succeeded streamText and nested execute_tool exec nodes; the codex agent Tools tab displayed collaboration, exec, and functions for the selected version. A fresh blank-model failure did not occur during the window, so codex/unknown is regression-tested and deployed but remains unexercised live.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Aligned archive replay with the coding-agent-specific Codex plugin contract: unified ordinary traffic under codex, marked only proven child threads as codex/subagent, stabilized catalog versions from repeated instruction/tool fingerprints, included actually executed tools, retained identified unknown-model failures, and reported successful completion without fabricating failure stop reasons. Preserved originator metadata, source-derived stream mode, deterministic span correlation, and richer timing. Verified by fail-first regressions, focused sink/trace/metric packages, a zero-finding second CodeRabbit review, make check, and go build ./....

Deployed exact head ceb2d05399f9c902a8cdff8e72fe567bf96a15da. Fresh m7kni API, Tempo, and signed-in UI evidence confirms codex/codex-subagent identity, stable per-conversation versions, visible executed tools, and exact Generation-to-span joins. Blank-model codex/unknown remains source-tested and deployed but was not exercised live because no fresh qualifying failure occurred.
<!-- SECTION:FINAL_SUMMARY:END -->
