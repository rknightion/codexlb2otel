# codexlb2otel

Tails codex-lb's conversation archives and emits OTLP metrics, Loki logs, Tempo traces and
agento11y generations. `CLAUDE.md` imports this file, so Claude Code and Codex read the same
instructions and cannot drift apart. Put project instructions here, never there.

## Task interface

This repo's task surface is a `justfile`. Discover it, don't guess it:

    just --list                        # human-readable
    just --dump --dump-format json     # machine-readable
    just --show <recipe>               # what a recipe actually runs

- `just check` is the toolchain-only pre-commit gate and exactly what CI's `build-test` job enforces.
  CI separately runs `just vuln` plus the Docker- and cross-compilation-dependent `just ci` legs.
- Prefer `just <recipe>` over the underlying tool. If you are typing `go test`, you want `just test`.
- Run `just` with stdin from `/dev/null`. `just baseline` is `[confirm]`-gated — it overwrites the
  committed `corpus.sig.json`. Stop and ask before running it; never pass `--yes` or `JUST_YES=1`.
- If a task you need does not exist, add a recipe with a `#` doc comment and a `[group(...)]` rather
  than running a bare command.
- `just test` is the full local race-test suite (uses the corpus if you have synced one). `just check`
  runs `just test-short` instead, matching CI's non-corpus path. A green `just test-short` (or `just
  check`) is not proof the corpus-backed tests pass; that only happens locally with `just test` against
  a synced corpus.
- `just check` also requires Python 3 for dashboard artifact and coverage validation. Make sure
  `python3` is on `PATH` before running the gate.

## The archives are personal data

The conversation archives hold full prompts, tool output and assistant messages. They are gitignored
by tree and by extension, and `TestNoArchivesAreTracked` (`internal/fixture/tracked_test.go`) fails
the build if anything of that shape is ever staged. `corpus.sig.json` is content-free by
construction; `TestSignature_CarriesNoConversationContent` keeps it that way. Do not weaken either.

## Task tracking

Work lives in a Backlog.md board under `backlog/`, driven **through the CLI** - `backlog task list
--plain` is the queue, `backlog doc list --plain` the durable docs. `backlog/` is committed, so the
same personal-data bar applies to it: write the shape, not the instance.

Four rules, each for a specific silent failure:

- **Never `--notes` or `--plan` bare.** They *replace* the whole section, destroying another
  session's writes with no warning and exit 0. Use `--append-notes` / `--append-plan`. This is an
  open upstream bug, not a misunderstanding, and a global `PreToolUse` hook in the agent config denies the bare
  forms rather than trusting anyone to remember.
- **Never hand-edit task, draft, doc, decision or milestone markdown.** Section boundaries are
  HTML-comment markers; break one and the section is silently dropped at exit 0 - still in the file,
  invisible to the CLI, until the next write destroys it for real. There is no repair command;
  `backlog doctor` only fixes duplicate task IDs. `backlog/config.yml` is the deliberate exception:
  list-valued keys cannot be set through `backlog config set`, so it is edited by hand.
- **Finalize in one call**, so an interrupted session cannot leave finished work looking unfinished:
  `backlog task edit CXO-0007 --check-ac 1 --check-ac 2 -s Done`.
- **Never let two agents edit the same task.** The concurrent-write fix upstream covers the edit
  funnel but not reorder, draft saves, the TUI path, `doc update` or decision updates.

Read **"Agent fan-out protocol (canonical)"** before designing a wave, and **"Wave operating model"**
for this project's own rules - its recurring defects, exclusive resources and run-end contract.
`backlog doc list --plain` shows both.

## History before 2026-08-14

This repo tracked its work in GitHub Issues until 2026-08-14. The issues and their local export were
deleted before the repository became public. Existing `#NNN` references in commit messages and code
comments refer to that retired tracker; task IDs deliberately do not mirror it.

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
