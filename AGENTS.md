# codexlb2otel

Tails codex-lb's conversation archives and emits OTLP metrics, Loki logs, Tempo traces and
agento11y generations. `CLAUDE.md` imports this file, so Claude Code and Codex read the same
instructions and cannot drift apart. Put project instructions here, never there.

## The gate

```bash
make check        # gofmt -l . ; go vet ./... ; go test ./...
go build ./...
```

`make test-short` is the fast inner loop and skips the corpus tests. **A green `test-short` is not a
green gate.**

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

This repo tracked its work in GitHub Issues until 2026-08-14. The issues were archived and then
deleted, so `gh issue view <N>` no longer resolves: bodies and comments are in
`archive/issues-dump.json` (redacted - see `archive/README.md`), and the closed set is indexed in the
**"Closed GitHub Issues (#1-#42)"** document. `#NNN` references in commit messages and code comments
still point there and remain the only ID space for that history - task IDs deliberately do not
mirror it.

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
