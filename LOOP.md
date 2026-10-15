# Loop: codexlb2otel
tier: guarded
gate: just check
ci-required: ci-success
release-on-push: yes
deploy-on-push: yes
grafana-stack: none

Public repository: no deployment identifier, host name or archive content in any tracked file,
`backlog/` included. Run `just` with stdin from `/dev/null`. `just ci` is the local superset of the
separate CI legs (`vuln`, `snapshot`, `image`).

## Credentials

- The Postgres DSN lives in the deployment environment, never in the repo. Enrichment and the
  optional account poller use a read-only role holding SELECT on a fixed set of tables only.

## Traps

- Every push to `main` that touches more than docs, dashboards or Markdown builds the edge `:main`
  image, and the deployment pulls it automatically. Treat a code push as a deploy.
- The conversation archives are personal data. Never stage one; `TestNoArchivesAreTracked` and
  `TestSignature_CarriesNoConversationContent` guard this, so never weaken either.
- `just baseline` overwrites the committed `internal/profile/baseline/corpus.sig.json` and is
  confirm-gated: never run it unasked, never pass `--yes` or `JUST_YES=1`.
- `just test-corpus` (about 25-30 minutes, reads the local corpus) is the only proof for caps, Loki
  sizing, drift and archive decoding. It never runs in the inner loop; run it once on the final
  candidate when one of those changed.
- `just check` and `just test` never read the local corpus, and `just check` needs `python3` on PATH.
- A baseline is a deliberate act, always from a `-full` scan: a sampled scan (`clbprobe -sampled`)
  never sets one, and absence findings are informational and never page.
- Tempo traces and Agent Observability generations are permanently disabled on the deployment, so
  source-level trace tests are not live delivery evidence.

## Mutexes

- The live archive directory on the codex-lb host (tailnet only): two lanes running `clbsync` race
  on the same local corpus tree. One at a time.
- `corpus/` and `corpus.sig.json`: two lanes cannot both accept a baseline.
- The Grafana Cloud stack: emitting telemetry live is visible to everything pointed at it; a brief
  that emits says so.
- The deployed service and its config: one owner at a time.
- Contended files, one lane or the root's wiring pass: `internal/attr/attr.go`,
  `internal/turn/reducer.go`, `internal/sink/otlpmetric/record.go`, `config.example.yaml` and
  `backlog/config.yml`.

## Lane brief

A lane that hits a contract decision its brief does not cover stops and returns the question: a new
attribute key, a change to an instrument's cardinality, a baseline acceptance, or what a sink does
with an error class. The root may close a Renovate pull request itself once the equivalent change
has landed on `main` under another module path or version; comment the landing SHA first. Releases,
dashboard writes, tenant or cloud mutations and deploys stay with the owner unless the charter's
authority names them.
