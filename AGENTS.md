# codexlb2otel

Tails codex-lb's conversation archives and emits OTLP metrics, Loki logs, Tempo traces and
agento11y generations.

## Task interface

`just check` is the gate, and CI's `build-test` job runs exactly that. `just vuln`, `just snapshot`
and `just image` are separate CI jobs; `just ci` is the local superset that covers them.

- Run `just` with stdin from `/dev/null`. `just baseline` is `[confirm]`-gated and overwrites the
  committed `internal/profile/baseline/corpus.sig.json`. Ask before running it, and never pass
  `--yes` or `JUST_YES=1`.
- `just check` needs `python3` on `PATH` for the dashboard-artifact and docs-link legs.
- `just test` and `just check` never read the local corpus. `just test-corpus` is the opt-in
  full-corpus gate against real captures and takes roughly 25-30 minutes. It is the only thing that
  proves caps, Loki sizing and drift, so run it after changing archive decoding, reduction, metric
  cardinality, Loki sizing or the embedded drift contract, and before a release whose evidence
  depends on real archive coverage. Race-detector concurrency coverage stays in `just test`.
- `just docs-links` validates every relative Markdown link and heading anchor in `README.md`,
  `AGENTS.md` and `docs/`, so a broken link in this file fails the gate.

## The archives are personal data

The conversation archives hold full prompts, tool output and assistant messages. They are gitignored
by tree and by extension, and `TestNoArchivesAreTracked` (`internal/fixture/tracked_test.go`) fails
the build if anything of that shape is ever staged. `corpus.sig.json` is content-free by
construction, and `TestSignature_CarriesNoConversationContent` (`internal/profile/signature_test.go`)
keeps it that way. Do not weaken either. `backlog/` is committed, so the same bar applies there:
write the shape, not the instance.

## Settled deployment decisions

- Tempo traces and Agent Observability generations are permanently disabled on the camden
  deployment; native per-profile Codex integration owns the generations path. Source-level trace
  tests are therefore not live delivery evidence.
- Postgres enrichment and the optional account poller run through the read-only `codexlb2otel_ro`
  role, which holds `SELECT` on `request_logs`, `api_keys`, `accounts`, `usage_history`,
  `additional_usage_history` and `api_key_accounts` only. The DSN lives in the deployment
  environment.

## Task tracking

- Never `--notes` or `--plan` bare. They replace the whole section and exit 0, destroying another
  session's writes with no warning. Use `--append-notes` and `--append-plan`; a global guard hook
  denies the bare forms.
- Never hand-edit task, draft, doc, decision or milestone markdown. Section boundaries are
  HTML-comment markers; break one and the section is silently dropped at exit 0, still in the file
  but invisible to the CLI until the next write destroys it for real. There is no repair command,
  and `backlog doctor` only fixes duplicate task IDs. `backlog/config.yml` is the deliberate
  exception: list-valued keys cannot be set through `backlog config set`, so it is hand-edited.
- Finalize in one call, so an interrupted session cannot leave finished work looking unfinished:
  `backlog task edit CXO-0007 --check-ac 1 --check-ac 2 -s Done`.
- Never let two agents edit the same task. The upstream concurrent-write fix covers the edit funnel
  but not reorder, draft saves, the TUI path, `doc update` or decision updates.
- `#NNN` in commit messages and code comments points at a deleted GitHub Issues tracker, not at a
  Backlog task ID.

Loops: the protocol lives in `~/repos/agent-docs/sources/loop/` (this board's "Loop protocol pointer" doc points there); this repository's loop facts are in `LOOP.md`.

## Recurring defects

What a review pass should look for:

- **Narrowing silently drops attributes.** `attr.Only(...)` builds every instrument's attribute set;
  an attribute absent from the list is gone with no error. `codexlb.family` was once dropped from
  every token counter and duration histogram, so synthetic probe traffic polluted every published
  cost and latency number. A new instrument states which attributes it drops and why.
- **A library callback can run re-entrantly, inside a call you made.** Any lock held across a
  library call is a lock its callbacks must not take: a self-observability callback taking the
  watcher lock inside `Poll` blocked `Collect` until the `PeriodicReader`'s default 30s timeout (not
  `otlp.timeout`), and nothing was ingested.
- **Test the seam itself.** That deadlock lived only where `internal/tail` meets
  `internal/sink/otlpmetric`; both halves passed alone. A regression test for a "never returns"
  failure bounds the call (`TestWatcher_ProgressIsSafeFromInsideEmit` bounds `Poll` at 10s).
- **A plain age cutoff evicts the wedged agent first.** Anything time-based needs a mid-turn
  exemption and a test for it.
- **A config fault on one sink stops every signal** (a 401 is never-retryable and holds the
  checkpoint). A new sink ships disabled in `config.example.yaml`;
  `TestLoad_ExampleConfigIsDeployable` keeps it so.
- **Sampling proves nothing about rare shapes** (the rarest occur about 10 times in 1.32M records),
  and a corpus test must select for the variety it needs rather than assume the newest capture has it.
- Sweep `backlog/` before committing: `grep -rniE "@gmail|@rob-knight|/Users/[[:alnum:]_.-]+|[0-9]{1,3}(\.[0-9]{1,3}){3}" backlog/`.

## Deeper references

- `docs/operations.md` - read before changing deployment configuration, enrichment, checkpointing or
  which signals are enabled.
- `docs/security.md` - read before touching archive handling, retention, or anything that could
  carry conversation content into telemetry.

<!-- BACKLOG.MD GUIDELINES START -->
<!-- backlog.md-instructions-version: 1.50.1 -->
<CRITICAL_INSTRUCTION>

## Backlog.md Workflow

This project uses Backlog.md for task and project management.

**For every user request in this project, run `backlog instructions overview` before answering or taking action.**

Use the overview to decide whether to search, read, create, or update Backlog tasks.

Before task lifecycle actions, read the matching detailed guide:
- `backlog instructions task-creation` before creating or splitting tasks
- `backlog instructions task-execution` before planning, changing status or assignee, adding a plan or implementation notes, or implementing task work
- `backlog instructions task-finalization` before checking acceptance criteria, writing final summaries, or moving tasks to terminal statuses

Use `backlog <command> --help` before running unfamiliar commands. Help shows options, fields, and examples.

Do not edit Backlog task, draft, document, decision, or milestone markdown files directly. Use the `backlog` CLI so metadata, relationships, and history stay consistent.

</CRITICAL_INSTRUCTION>
<!-- BACKLOG.MD GUIDELINES END -->
