---
title: Security
description: Conversation-content exposure, secret handling, listener controls, retention, and safe repository practices
tags:
  - Security
  - Loki
---

# Security

`codexlb2otel` processes full Codex conversations. Treat the archive, content-bearing telemetry,
live view, summaries, and diagnostic output as sensitive data.

## Conversation content

The archive can contain prompts, assistant messages, tool arguments, tool output, instructions, and
anything a command printed—including credentials accidentally read by an agent.

Loki exports all record types when `loki.record_types` is empty. To operate a structural timeline
without message bodies, configure only:

```yaml
loki:
  record_types: [turn, transport, error]
```

This is data minimization, not a redaction engine. Metadata can still carry model, agent, timing,
usage, and conversation identifiers.

## Live view

The live view defaults off and to loopback. `live.content: false` removes prose while keeping the
agent tree and operational metadata. A non-loopback bind without `live.token` fails validation unless
`live.allow_insecure` explicitly overrides the guard.

Query-string tokens exist for browser `EventSource` compatibility and can appear in browser history
or proxy logs. Prefer a tightly scoped private listener, and rotate any token used in a URL.

## Third-party summarization

`clbsum` is the only component that deliberately sends selected conversation content to a third-
party model router. It is disabled until `summarize.enabled` is true. Review the selected sessions
with `-list` or `-dry-run` before sending them. Zero-data-retention routing and denied provider data
collection are restrictive defaults, not a substitute for deciding whether the content may leave
your environment.

## Secrets

Use `${ENV_VAR}` or `file:` indirection for Postgres, Loki, OTLP, generation, live-view, and
OpenRouter tokens. Do not place a DSN or any credential inline in the example configuration. The
config dump masks secret fields. Missing indirections for an enabled sink fail at startup; an
unavailable optional Postgres DSN disables enrichment and leaves the archive path running.

Scope backend tokens to the signals they write. Agent Observability needs its generation-write
permission in addition to ordinary telemetry scopes.

Postgres enrichment is read-only and optional. Use an existing role with `SELECT` on `request_logs`,
`api_keys`, and `accounts`; the service does not create roles, alter grants, or write request data.
Keep `postgres.enabled` false when no such DSN is available. A database outage must affect only
enrichment, not archive ingestion or another enabled sink.

## Archive and checkpoint storage

Conversation archives must remain outside Git. Repository tests fail if an archive-shaped file is
tracked, and `.gitignore` blocks the corpus tree and common archive extensions. `corpus.sig.json` is
safe metadata; `clbprofile` output is not and is ignored.

The checkpoint contains identifiers and cumulative state but no message bodies. Protect it from
unauthorized reads and keep its directory writable only by the service identity.

Archive file retention and reducer state retention are separate controls. Enabling archive deletion
turns the raw capture into a disposable buffer, so confirm that no recovery requirement depends on
it. State eviction is anchored to archive event time and keeps open responses; a returning series is
marked `BaselineReset` and its first cumulative value is only an upper bound. Deleted-file tombstones
are retained until their UTC filename day is more than three days old, then pruned.

The optional in-process drift probe reads the archive and an embedded content-free baseline. It does
not export conversation bodies as part of its findings, and sampled scans cannot prove that a rare
shape is absent. Treat `info` disappearance findings as diagnostic rather than as evidence that data
was safely removed.

## Encrypted reasoning

`reasoning.encrypted_content` is encrypted by OpenAI and is never decrypted here. The exporter uses
observable metadata such as reasoning token counts; encrypted reasoning text remains opaque.
