---
id: CXO-0018
title: Migrate the repo task surface to just and retire Makefiles and ad-hoc scripts
status: To Do
assignee: []
created_date: '2026-08-28 19:19'
labels: []
dependencies: []
priority: medium
type: chore
ordinal: 18000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
# Migrate codexlb2otel's task surface to `just`

## 1. Outcome

`codexlb2otel` has a single top-level `justfile` (no submodules — the repo is small enough that
`mod`/`import` add nothing). It defines the seven mandatory recipes plus repo-specific ones for the
corpus-drift tooling (`probe`, `probe-sampled`, `baseline`, `probe-ci`), the multi-binary build
(`build`), the live-run helper (`run`) and the archive sync helper (`sync`). `just check` is exactly
what `ci.yml`'s `test` job enforces — gofmt, vet, all seven CLI binaries build, the no-corpus test
suite, and the CI-flavoured drift probe. `Makefile` is gone. `AGENTS.md`, `README.md` and
`backlog/config.yml` reference `just` recipes, never `make`. `ci.yml`'s `test` job collapses to one
`run: just check` step behind a `setup-just` step. No shell scripts exist in this repo today (verified
via `git ls-files`), so there is nothing to absorb or keep beyond the Makefile itself.

## 2. The complete justfile

Drop this in at `codexlb2otel/justfile`, adjust nothing unless a later step in this doc says to.

```just
set shell := ["bash", "-euo", "pipefail", "-c"]

# Go 1.27's json/v2-backed implementation regresses the multi-gigabyte corpus
# scans (see Makefile history / go.mod comment). Keep the established v1
# implementation until the archive decoder is migrated and measured against
# json/v2 explicitly. Exported so every recipe below inherits it.
export GOEXPERIMENT := env('GOEXPERIMENT', 'nojsonv2')

# Directory clbprobe/clbsync operate against. Override: `just probe corpus=other/dir`.
corpus := env('CORPUS', 'corpus/processed')

# show the task surface
default:
    @just --list

# install go module dependencies into the local module cache
setup:
    go mod download

# format all go source in place
[group('check')]
fmt:
    gofmt -w .

# verify formatting (go source + this justfile); never mutates
[group('check')]
fmt-check:
    test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
    just --fmt --check

# static analysis (go vet; no golangci-lint config exists in this repo)
[group('check')]
[no-exit-message]
lint:
    go vet ./...

# full go test suite (uses the local corpus if present); set filter="Name" for a subset
[group('check')]
[no-exit-message]
test filter="":
    go test {{ if filter != "" { "-run " + filter } else { "" } }} ./...

# fast inner loop: same suite, corpus-backed tests forced to skip cleanly (CI's exact invocation)
[group('check')]
[no-exit-message]
test-short:
    CLB_CORPUS=/nonexistent CLB_NO_CORPUS=1 go test ./...

# build all seven CLI tools into bin/ (gitignored)
[group('build')]
build:
    mkdir -p bin
    go build -o bin/codexlb2otel ./cmd/codexlb2otel
    go build -o bin/clbsync ./cmd/clbsync
    go build -o bin/clbfind ./cmd/clbfind
    go build -o bin/clbsum ./cmd/clbsum
    go build -o bin/clbprobe ./cmd/clbprobe
    go build -o bin/clbprofile ./cmd/clbprofile
    go build -o bin/clbstat ./cmd/clbstat

# remove bin/ (everything `setup` + `build` can reproduce)
[group('build')]
clean:
    rm -rf bin

# THE GATE. Exactly what ci.yml's `test` job enforces.
[group('check')]
check: fmt-check lint build test-short probe-ci

# run codexlb2otel against a config file (long-running; set config=path to override)
[group('dev')]
run config="config.yaml":
    mkdir -p bin
    go build -o bin/codexlb2otel ./cmd/codexlb2otel
    ./bin/codexlb2otel -config {{ config }}

# pull new archives off the codex-lb host
[group('dev')]
sync:
    mkdir -p bin
    go build -o bin/clbsync ./cmd/clbsync
    ./bin/clbsync

# full drift check against corpus.sig.json (set corpus=path to override; exits 1 on breaking drift)
[group('check')]
probe:
    mkdir -p bin
    go build -o bin/clbprobe ./cmd/clbprobe
    ./bin/clbprobe {{ corpus }}

# sampled drift check (faster; cannot prove a shape is absent)
[group('check')]
probe-sampled:
    mkdir -p bin
    go build -o bin/clbprobe ./cmd/clbprobe
    ./bin/clbprobe -sampled {{ corpus }}

# accept the current corpus shape as the baseline (always run from a FULL scan)
[group('gen')]
[confirm('This overwrites corpus.sig.json from a full scan of the local corpus. Continue?')]
baseline:
    mkdir -p bin
    go build -o bin/clbprobe ./cmd/clbprobe
    ./bin/clbprobe -update corpus.sig.json {{ corpus }}

# CI's exact drift-probe invocation: builds clbprobe, scans corpus/, and treats
# "nothing to scan" (exit 3) as a documented pass rather than a failure. There
# is never a corpus in CI (it's gitignored, personal data) so this always
# exercises the exit-3 branch there; it exercises the real scan on a machine
# that does have corpus/ populated.
[group('check')]
[script('bash')]
probe-ci:
    set -euo pipefail
    go build -o /tmp/clbprobe ./cmd/clbprobe
    set +e
    /tmp/clbprobe -fail-on breaking corpus
    status=$?
    set -e
    case "$status" in
      0) echo "clbprobe: clean, no drift against corpus.sig.json" ;;
      3) echo "clbprobe: nothing to scan - skipped" ;;
      1) echo "clbprobe: drift at or above 'breaking' against corpus.sig.json"; exit 1 ;;
      *) echo "clbprobe: exit $status (see output above)"; exit 1 ;;
    esac
```

Notes on choices baked into that file (do not re-litigate without a new fact):

- **No `typecheck` recipe.** Go's compiler is the type checker; `build` already exercises it across
  every `cmd/*` package. Adding a separate no-op `typecheck` would violate the "no-op recipes only
  when the vocabulary slot is genuinely absent AND still worth documenting" spirit for no benefit —
  `build` already covers it and is in `check`.
- **No `gen`/`gen-check`.** `corpus.sig.json` is a committed generated artifact in spirit, but
  regenerating it (`baseline`) requires the local, gitignored, personal-data corpus — it can never run
  in CI, so a `gen-check` drift gate is structurally impossible here. `baseline` (repo-specific name,
  `group('gen')`) stays a manual, `[confirm]`-gated developer action instead.
- **`check`'s test step is `test-short`, not `test`.** The old `make check` ran plain `go test ./...`
  (full suite, corpus-dependent if a corpus happens to be present locally) while CI always forces the
  no-corpus path — see `ci.yml`'s own comment: "mirrors the Makefile's `test-short` target". That
  asymmetry pre-exists this migration. Because the fleet contract requires `check` to equal CI exactly,
  `check` here depends on `test-short`. `test` (the mandatory recipe) keeps the Makefile's original
  full-suite contract for a developer who has synced the corpus locally — it is intentionally *not*
  wired into `check`. Preserve this distinction; don't silently swap `check` to depend on `test`.
- **`check`'s build step is `build` (bin/ per tool), not a bare `go build ./...`.** Building the seven
  named binaries compiles every buildable package the module needs; functionally equivalent to CI's
  plain `go build ./...` step, and it's the recipe developers actually run.

## 3. Makefile disposition

`Makefile` — absorb in full, then `git rm Makefile`.

| Make target | Replacement | Notes |
|---|---|---|
| `build` (+ `bin/%` pattern rule) | `just build` | Pattern rule → explicit line per binary (7 fixed tools, enumerated in §11 of the fleet standard's translation table: "no equivalent; write explicit rules"). Drops the `GO_FILES`-based smart-rebuild dependency tracking — `go build`'s own cache makes that redundant; the Makefile comment says as much ("go build caches, so the over-broad dependency costs nothing"). |
| `run` (`CONFIG ?= config.yaml`) | `just run` / `just run config=other.yaml` | `CONFIG ?=` → a `config=""` recipe parameter with a default, per §12 of the standard. |
| `clean` | `just clean` | Unchanged behavior. |
| `sync` | `just sync` | Unchanged behavior. |
| `probe` (`CORPUS ?= corpus/processed`) | `just probe` / `just probe corpus=other/dir` | `CORPUS ?=` → `corpus := env('CORPUS', 'corpus/processed')` plus a recipe parameter default of the same name, per §12. |
| `probe-sampled` | `just probe-sampled` | Unchanged behavior. |
| `baseline` | `just baseline` | Now `[confirm]`-gated (§5.4 of the standard: this is a deliberate, hard-to-undo overwrite of a committed baseline file — the Makefile's own comment already calls it "a deliberate act"). |
| `test` | `just test` | Unchanged behavior (full suite, optional `filter=` param added per the mandatory `test` contract). |
| `test-short` | `just test-short` | Unchanged behavior; also now the thing `just check` actually runs (see justfile notes above). |
| `check` (`gofmt -l . ; go vet ./... ; go test ./...`) | `just check` | Behavior changed deliberately: now runs `fmt-check` (which actually fails on unformatted files — the old `gofmt -l .` alone did not, see traps), `lint`, `build`, `test-short` (not `test` — see above), and `probe-ci`, to match what CI enforces exactly. |
| `GOEXPERIMENT ?= nojsonv2` (top-level export) | `export GOEXPERIMENT := env('GOEXPERIMENT', 'nojsonv2')` in the justfile | Applies to every recipe automatically, same as the Makefile's global export. |

**Instruction: `git rm Makefile` once the justfile is proven locally (§8, step order) and CI is green
on it.**

## 4. Script disposition

`git ls-files | grep -E '\.(sh|bash|zsh|ps1)$'` returns nothing. There are no shell scripts tracked in
this repo. Nothing to ABSORB, nothing to KEEP. (There is also no `scripts/` directory and no
non-trivial helper program in another language used as a dev/CI task — `cmd/*` are the CLI tools
themselves, not task scripts, and stay exactly as they are, invoked via the `justfile` recipes above.)

## 5. CI changes

### `.github/workflows/ci.yml`

Current `test` job runs five separate `run:` steps (gofmt, go vet, go build, go test, clbprobe) after
checkout + setup-go. Replace all five with a `setup-just` step and one `run: just check`.

Remove the job-level env block:

```yaml
env:
  # Go 1.27's json/v2-backed implementation regresses multi-gigabyte archive
  # scans; retain the established v1 behavior until that migration is measured.
  GOEXPERIMENT: nojsonv2
```

— it's redundant now: the justfile owns and exports `GOEXPERIMENT` itself (`env('GOEXPERIMENT',
'nojsonv2')`), so the workflow no longer needs to set it. (If you'd rather keep it as an explicit
belt-and-braces override, that's harmless too — the justfile's `env()` call still respects it. Default
recommendation: remove it, one source of truth.)

Steps section becomes:

```yaml
    steps:
      - name: checkout
        uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1

      - name: setup go
        uses: actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16 # v6.5.0
        with:
          go-version-file: go.mod
          cache: true
          cache-dependency-path: go.sum

      - name: setup just
        uses: extractions/setup-just@53165ef7e734c5c07cb06b3c8e7b647c5aa16db3 # v4.0.0
        with:
          just-version: '1.58.0'

      - name: just check
        run: just check
```

Delete the five old `run:` steps (`gofmt`, `go vet`, `go build`, `go test (non-corpus)`, `clbprobe
drift check`) entirely — `just check` now runs all of it (`fmt-check`, `lint`, `build`, `test-short`,
`probe-ci`, in that dependency order).

**Do NOT touch:**
- `name: CI`, the `on:` triggers, `permissions: { contents: read }`, the `concurrency:` block —
  unchanged.
- `timeout-minutes: 15` — unchanged.
- The job is named `test`; there is no separate `ci-success` aggregator job in this workflow today
  (single-job workflow) — nothing to preserve there beyond leaving the job name and structure alone.
- `actions/checkout` and `actions/setup-go` pins — unchanged, not part of this migration.

### `.github/workflows/publish.yml`

**Out of scope entirely.** No build/test/lint/format/generate/validate `run:` logic lives here — it
delegates to `rknightion/.github/.github/workflows/container-publish.yml` via `uses:` (a reusable
workflow call) and has one small inline step (`read the go directive from go.mod`, a two-line `awk`
that isn't a build/test/lint task). Per the fleet standard: never convert a `uses:` into `run: just`,
and this repo's own comment block already documents why this file is architecturally frozen (the
brewmdm/ARC blocker). Do not touch it.

### `.github/workflows/release-please.yml`

**Out of scope.** GitHub-native release-please orchestration plus a `uses:` call into `publish.yml`.
No shell logic to migrate.

### `.github/workflows/trigger-docs-sync.yml`

**Out of scope.** A broker-token mint (`uses:`) plus a `repository_dispatch` action (`uses:`). No
shell logic to migrate.

### `.github/workflows/scheduled-archive-probe.yml`

**Out of scope for CI wiring, but note it for later:** this workflow is explicitly documented in its
own header comment as NOT YET LIVE (no self-hosted runner registered, placeholder archive path). It
contains the same clbprobe build+run+case-statement pattern as `ci.yml`'s old drift-check step, just
against a live archive with `-sampled` instead of a full committed corpus. When this workflow is
eventually activated, point it at the justfile too — the recipe body would be
`clbprobe -sampled -fail-on breaking "$ARCHIVE_DIR"` instead of `probe-ci`'s hardcoded `corpus`. **Do
not build that recipe now** — it targets an unresolved runner label and an unresolved archive path;
inventing a recipe for infrastructure that doesn't exist yet is scope creep. Leave this workflow
exactly as-is; this is a note for whoever activates it later, not an action item for this task.

## 6. Docs and agent-contract changes

### `AGENTS.md` (imported by `CLAUDE.md` via `@AGENTS.md` — edit only `AGENTS.md`)

Replace the "## The gate" section:

```markdown
## The gate

```bash
make check        # gofmt -l . ; go vet ./... ; go test ./...
go build ./...
```

`make test-short` is the fast inner loop and skips the corpus tests. **A green `test-short` is not a
green gate.**
```

with:

```markdown
## Task interface

This repo's task surface is a `justfile`. Discover it, don't guess it:

    just --list                        # human-readable
    just --dump --dump-format json     # machine-readable
    just --show <recipe>               # what a recipe actually runs

- `just check` is the full gate and is exactly what CI enforces. It must pass before you commit.
- Prefer `just <recipe>` over the underlying tool. If you are typing `go test`, you want `just test`.
- Run `just` with stdin from /dev/null. `just baseline` is `[confirm]`-gated — it overwrites the
  committed `corpus.sig.json`. Stop and ask before running it; never pass `--yes` or `JUST_YES=1`.
- If a task you need does not exist, add a recipe with a `#` doc comment and a `[group(...)]` rather
  than running a bare command.
- `just test` is the full local suite (uses the corpus if you've synced one). `just check` runs
  `just test-short` instead, matching CI exactly — CI never has a corpus (it's gitignored, personal
  data). A green `just test-short` (or `just check`) is not proof the corpus-backed tests pass; that
  only happens locally with `just test` against a synced corpus.
```

(This preserves the original "green test-short is not a green gate" warning, restated for `just`
naming, and folds in the fleet-standard §9 boilerplate.)

Do not paste the recipe list itself into `AGENTS.md` — same rot risk the standard warns about.

### `CLAUDE.md`

No change — it's a two-line pointer (`@AGENTS.md`) with no `make` or script references.

### `README.md`

Replace lines 67, 74–82 (the `make`-based usage block):

Current (verbatim, includes a real discrepancy against the actual Makefile — `probe` is the full scan
and `probe-sampled` is the fast one, but this README text has the fast/full description backwards
relative to target names; see trap below):

```
make build && ./bin/clbfind resp_052b6a... # faster to re-run; bin/ is gitignored
...
| `make build` | build every tool into `bin/` |
| `make sync` | pull new archives off the codex-lb host |
| `make probe` | fast drift check against the baseline (exit 1 on anything new) |
| `make probe-full` | exhaustive drift check |
| `make baseline` | accept the current shape as the baseline (always from a full scan) |
| `make check` | gofmt + vet + tests |
| `make test-short` | tests without the corpus, the fast inner loop |

`make probe CORPUS=some/other/dir` overrides the directory.
```

Replace with (corrected to match actual behavior — `probe` is the full scan, `probe-sampled` is the
fast one):

```
just build && ./bin/clbfind resp_052b6a... # faster to re-run; bin/ is gitignored
...
| `just build` | build every tool into `bin/` |
| `just sync` | pull new archives off the codex-lb host |
| `just probe` | full drift check against the baseline (exit 1 on breaking drift) |
| `just probe-sampled` | faster sampled drift check; cannot prove a shape is absent |
| `just baseline` | accept the current shape as the baseline (always from a full scan; asks to confirm) |
| `just check` | the full gate: fmt-check, lint, build, test-short, probe-ci |
| `just test` | full test suite (uses the local corpus if synced) |
| `just test-short` | tests without the corpus, the fast inner loop |

`just probe corpus=some/other/dir` overrides the directory.
```

Search the rest of `README.md` for any other `make ` occurrence before finalizing this edit — only
the block above was found by `grep -n "make "`, but re-grep at implementation time in case the file
changed since this analysis.

## 7. `backlog/config.yml`

Current:

```yaml
definition_of_done: ["make check passes: gofmt -l . reports nothing, go vet ./... clean, go test ./... green", "go build ./... succeeds"]
```

New:

```yaml
definition_of_done: ["just check passes: fmt-check, lint, build, test-short and probe-ci all clean"]
```

Edit this file by hand — `backlog/config.yml`'s list-valued keys cannot be set through `backlog config
set` (per this repo's own `AGENTS.md`, which explicitly carves this file out as the one deliberate
hand-edit exception). Do not run this through the `backlog` CLI.

## 8. Order of work

1. Add `justfile` at repo root (content in §2 above). Do not touch anything else yet.
2. Run `just --fmt --check` — fix formatting if it fails, or run `just --fmt` once to auto-format,
   then re-check.
3. Prove every recipe locally: `just setup`, `just fmt-check`, `just lint`, `just build`, `just
   test-short`, `just probe-ci`, then `just check` end to end. Fix anything that doesn't match the old
   Makefile/CI behavior before moving on.
4. Update `.github/workflows/ci.yml` per §5. Push and confirm the `test` job is green on the real
   runner (not just local `just check` — CI's Go version, cache behavior, and the always-exit-3
   `probe-ci` branch all need to be observed passing in the real environment).
5. Update `AGENTS.md`, `README.md`, `backlog/config.yml` per §6–7.
6. Only once CI is green on the justfile-based workflow and nothing else references `make` or
   `Makefile` (re-grep the whole repo: `grep -rn "make " --include="*.md" --include="*.yml"
   --include="*.yaml" .` and `grep -rn "Makefile" .`), delete `Makefile`: `git rm Makefile`.
7. Final full-repo grep for stray `make` references (docs, comments, workflow files) before closing
   the task.

Justfile first and proven locally, CI switched second, deletion last — never delete `Makefile` before
CI is confirmed green on `just check`.

## 9. Traps specific to this repo

- **`gofmt -l .` exits 0 even when it lists files.** The old `make check` target ran `gofmt -l .`
  directly and would report unformatted files without ever failing the target — only `ci.yml`'s
  inline `run:` block (which captures the output and checks it's non-empty) actually enforced
  formatting. `fmt-check` in the new justfile replicates the CI logic
  (`test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }`), not the old Makefile's silently-toothless
  version. Don't "simplify" `fmt-check` back to bare `gofmt -l .` — that reintroduces a check that
  can never fail.
- **`go run` collapses clbprobe's exit codes.** `ci.yml`'s own inline comment measured this directly:
  `go run ./cmd/clbprobe` turns every non-zero exit into a flat `1`, which would make the exit-3
  ("nothing to scan", the *expected* CI outcome) indistinguishable from exit-1 ("breaking drift,
  fail the build") and turn a normal green CI run red on every single push. `probe-ci` and `probe`/
  `probe-sampled` all build the binary first (`go build -o ... ./cmd/clbprobe`) and run the binary
  directly — never `go run`. Do not "simplify" any of these to `go run`.
- **`probe-ci` needs a persistent shell for its `case` statement** — that's exactly why it's a
  `[script('bash')]` recipe rather than plain `just` lines; a line-based recipe would hit "extra
  leading whitespace" on the multi-line `case`.
- **`check`'s test dependency is `test-short`, not `test` — this is deliberate, not a bug.** See the
  justfile notes in §2 and the Makefile disposition table in §3. If a future editor "fixes" `check` to
  depend on `test` instead because it looks more consistent with the mandatory-vocabulary naming, they
  will silently make `just check` depend on the local corpus being present — breaking it in CI (no
  corpus ever exists there) and making local runs non-reproducible depending on whether a dev happens
  to have synced.
- **README's `make probe` / `make probe-full` table entries do not match the Makefile's real target
  names or behavior** (`probe-full` doesn't exist; `probe` is already the full scan, not the fast one;
  `probe-sampled` is the fast one). §6 above rewrites the table to match actual behavior while
  renaming to `just`. This is a genuine pre-existing doc bug, not introduced by the migration — fix it
  while you're in the file rather than porting the error forward under a new name.
- **`GOEXPERIMENT` must reach every recipe that touches the corpus** (`build`, `run`, `sync`, `probe`,
  `probe-sampled`, `baseline`, `probe-ci`, `test`, `test-short`) — the top-level `export GOEXPERIMENT
  := env(...)` line in the justfile handles this automatically for the whole file; don't re-add it
  per-recipe.
- **No golangci-lint config exists in this repo** (`.golangci.yml` absent, confirmed by `find`). The
  `lint` recipe is `go vet ./...` only — do not invent a golangci-lint invocation that isn't backed by
  a real config file; that would fail on a tool that was never installed or configured here.
- **`corpus/processed` and the corpus data itself are gitignored, personal-data archives** — never
  create, commit, or reference a stub/fixture corpus directory as part of this migration. The
  `probe`/`probe-sampled`/`baseline`/`test` recipes only do anything meaningful on a machine that has
  run `just sync` (or equivalent) against a real archive; that's expected and matches current
  behavior, not a regression to fix.
- **`corpus.sig.json` is a large (83KB) committed file `baseline` overwrites in place.** The
  `[confirm]` gate on `baseline` is there specifically because this is easy to run by accident and
  hard to notice went wrong (a sampled or partial corpus baked in as if it were confirmed-complete).
  Do not remove the `[confirm]` attribute.

## 10. Out of scope

- **Every KEEP script** — there are none; this repo has zero tracked shell scripts.
- **`.github/workflows/publish.yml`** — reusable-workflow delegation to
  `rknightion/.github/.github/workflows/container-publish.yml`; per fleet standard §8, never convert a
  `uses:` into `run: just`. Leave entirely as-is, including its `permissions:` blocks and the go-version
  resolution step (not build/test/lint logic).
- **`.github/workflows/release-please.yml`** — GitHub-native release-please orchestration. Leave as-is.
- **`.github/workflows/trigger-docs-sync.yml`** — broker-token mint + repository_dispatch. Leave as-is.
- **`.github/workflows/scheduled-archive-probe.yml`** — not yet live (documented placeholder runner
  label and archive path in its own header comment). Leave entirely as-is; do not wire it to `just` in
  this task — see §5's note on it.
- **`Dockerfile` / `docker-compose.yml`** — no build/test logic lives here worth migrating; the
  Dockerfile's `GO_VERSION` build-arg is supplied by `publish.yml`'s `go-version` job, untouched by
  this migration.
- **`dashboards/`, `docs/`, `docs.toml`** — static content and Zensical config; `docs.toml` has no
  build/generate logic (the m7kni.io hub does the generation), nothing to migrate.
- **`corpus/`, `corpus.sig.json`, `shapes.json`, `testdata/`, `archive/`** — data, not task surface.
- **`release-please-config.json`, `.release-please-manifest.json`** — release-please's own config;
  unrelated to `just`.
- **CodeQL, zizmor, actionlint, scorecard, dependency-review, container-publish workflows** — none of
  these exist as separate workflow files in this repo today (only the five listed in §5 exist); if any
  are added later they follow the same "GitHub-native, never folded into `just`" rule from the fleet
  standard, not this task.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 Top-level justfile defines all seven mandatory recipes (default, setup, fmt, fmt-check, lint, test, check) plus build, clean, run, sync, probe, probe-sampled, baseline, probe-ci, each with a # doc comment and a [group(...)]
- [ ] #2 just check passes locally and is exactly the sequence ci.yml's test job enforces: fmt-check, lint, build, test-short, probe-ci
- [ ] #3 just --fmt --check passes on the justfile
- [ ] #4 just --list shows every public recipe with its doc comment and group; default and setup are ungrouped
- [ ] #5 Makefile is deleted via git rm only after ci.yml is confirmed green on the justfile-based workflow
- [ ] #6 No shell scripts needed absorption (git ls-files has none); confirmed none were left un-migrated
- [ ] #7 ci.yml's test job calls a pinned setup-just step then a single run: just check step, replacing the five separate gofmt/vet/build/test/clbprobe run steps, with permissions, concurrency and the job name unchanged
- [ ] #8 publish.yml, release-please.yml, trigger-docs-sync.yml and scheduled-archive-probe.yml are untouched
- [ ] #9 AGENTS.md's gate section and README.md's usage table reference just recipes instead of make targets, with the pre-existing probe/probe-sampled description swapped to match real behavior
- [ ] #10 backlog/config.yml's definition_of_done names just check instead of make check
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 make check passes: gofmt -l . reports nothing, go vet ./... clean, go test ./... green
- [ ] #2 go build ./... succeeds
<!-- DOD:END -->
