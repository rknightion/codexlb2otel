# Loop: codexlb2otel
tier: guarded
gate: just check
ci-required: ci-success
release-on-push: yes
deploy-on-push: yes
receiver: https://loopwatch.m7kni.com
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
- Tempo traces and Agent Observability generations are permanently disabled on the deployment, so
  source-level trace tests are not live delivery evidence.

## Mutexes

None recorded.
