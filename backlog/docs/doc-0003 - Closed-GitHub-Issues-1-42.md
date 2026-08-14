---
id: doc-0003
title: Closed GitHub Issues (#1-#42)
type: other
created_date: '2026-08-14 17:02'
updated_date: '2026-08-14 17:02'
---
This repo tracked its work in GitHub Issues from 2026-08-07 until 2026-08-14, when it moved to
Backlog.md. **31 issues were closed by then; this is the index of them.** The 8 that were still open
became tasks `CXO-0001` through `CXO-0008`.

They were imported as one index rather than as `Done` tasks on purpose. Task IDs follow creation
order, so a task ID could never be made to match the `#NNN` already cited in commit messages, code
comments and the issues themselves - importing them would have created **a second ID space over the
same history**. And 31 `Done` rows would compete with the board's only real signal, which is what is
left. `#NNN` stays the only way to refer to this history.

**The issues themselves were deleted from GitHub on 2026-08-14, so `gh issue view <N>` will not
resolve them.** The full bodies and all 52 comments live in `archive/issues-dump.json`, which is
redacted with stable placeholders - see `archive/README.md`. To read one:

```bash
jq '.[] | select(.number == 32)' archive/issues-dump.json
```

The SHA column is best-effort: it lists commits **cited inside the issue** that resolve in this
repo, not a `closed by` link - GitHub's close events were not captured, and the commit messages do
not carry issue numbers. Treat it as a starting point, not as provenance.

| # | Closed | Title | Commits cited |
| --- | --- | --- | --- |
| #1 | 2026-08-07 | codexlb2otel: tail codex-lb conversation archives, emit OTLP metrics + Loki logs | the whole P1-P7 build; 26 commits cited in the issue |
| #3 | 2026-08-07 | Freeze the Turn -> telemetry attribute contract | `7f57ff8` |
| #4 | 2026-08-07 | Service skeleton: cmd/codexlb2otel, config, lifecycle | - |
| #5 | 2026-08-07 | P3 - Loki emit | - |
| #6 | 2026-08-07 | Never silently lose a Loki line to max_line_size or a rate limit | - |
| #7 | 2026-08-07 | P4 - OTLP metrics | - |
| #8 | 2026-08-07 | Self-observability, and ingest lag above all | - |
| #10 | 2026-08-07 | P6 - packaging and release | `cfc27d5`, `eb373a1`, `fc7d893`, `d331267` +2 |
| #11 | 2026-08-07 | P7 - traces to Tempo: the agent tree and where a turn's time went | - |
| #12 | 2026-08-07 | Adopt the timing_metrics domains still being dropped | `4968921` |
| #14 | 2026-08-07 | Dashboards and alerts as code | `7f57ff8` |
| #15 | 2026-08-07 | Look up a codex-lb response id in Grafana and get the why plus the conversation | `014b04b` |
| #16 | 2026-08-07 | clbprobe: three defects found while explaining a false BREAKING finding | `a7464ee` |
| #17 | 2026-08-07 | Surface websocket connection health: control frames and connection-limit errors | `7f57ff8` |
| #18 | 2026-08-07 | GenAI semconv compliance: several codexlb.* names have standard equivalents | `228c717`, `954bd03` |
| #19 | 2026-08-07 | Grafana sigil / agento11y support: emit Generations, not just traces | `228c717`, `d05c1b9`, `d4cfe31`, `c06983e` |
| #20 | 2026-08-07 | Corpus drift 2026-08-07: HTTP family, safety_buffering object form, interrupt_agent, images | `0cada84`, `a052040` |
| #21 | 2026-08-07 | clbprobe is blind to embedded JSON documents, and missed the drift that mattered most | `a052040`, `4d94522` |
| #22 | 2026-08-07 | Follow-ups from the sigil and semconv wave: tool catalogue, request params, batch splitting | `954bd03`, `d4cfe31`, `a13a574`, `4968921` |
| #23 | 2026-08-07 | Emit the timing domains #12 captured: they reach Loki but no metric or span | `4968921` |
| #28 | 2026-08-07 | Move the conversation archive to tmpfs and prune it, and decide who owns the deletion | `f937dee` |
| #31 | 2026-08-07 | Full-telemetry v2 dashboard: every metric, log record type and span in one place | `1a4ded3` |
| #32 | 2026-08-08 | agento11y shows us as anonymous: the gen_ai.client.* metrics carry no agent name, and six other sigil contract gaps | `ab78f29` |
| #33 | 2026-08-08 | Carry the REQUESTED service tier, so priority processing can be verified | `fa0c997`, `1d841d7` |
| #34 | 2026-08-08 | The corpus test suite assumes the cheapest archives are a rich sample; two quiet hours broke seven tests | `ab78f29`, `1d841d7`, `fa0c997` |
| #35 | 2026-08-08 | Live conversation view: web UI over the thread + subagent tree, completed and in-flight | `ff5d86d`, `eeceb5a` |
| #36 | 2026-08-08 | Live view headlines: root threads lose theirs to ring eviction, and harness scaffolding surfaces as one | `1964fff` |
| #37 | 2026-08-09 | clbsum: summarise what work agent sessions actually accomplished, over a chosen time window | - |
| #38 | 2026-08-09 | clbsum: pin the latest DeepSeek, set reasoning effort explicitly, and stop throwing away 86 good calls when the 87th times out | `419499b` |
| #40 | 2026-08-09 | clbsum: default config path is cwd-relative, so it never finds the config in its own container image | - |
| #42 | 2026-08-09 | clbsum: a long run looks hung, and an interrupted one reads as broken | `6193bbf` |
