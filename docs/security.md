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

Loki exports all record types when `loki.record_types` is empty. Content records are bounded before
export: tool output is limited by the reducer's tool-content bound, and function-call arguments
replace opaque encrypted-looking string values with a structural marker. This is targeted handling,
not a general secret scanner. To operate a structural timeline without message bodies, configure
only:

```yaml
loki:
  record_types: [turn, transport, error]
```

Metadata can still carry model, agent, timing, usage, and conversation identifiers.

## Telemetry data boundary

The telemetry backend is an operator-controlled private system; the Git repository is public. Those
are separate boundaries. The exporter deliberately sends identity-class values such as conversation
and proxy-session identifiers, workspace metadata, turn-state metadata, model ETags, and passthrough
identifiers to Loki structured metadata and Tempo span attributes. They are never metric attributes
or Loki stream labels, because Identity is a cardinality classification rather than a redaction rule.

The database-sourced `codexlb.account.info` metric deliberately includes the account email alongside
account status, plan, and routing policy so the private dashboard can identify an account without a
database session. Account email is a bounded metric attribute, not a repository fixture or example.
Prompt metrics expose only bounded content-item kinds, never prompt or instruction text. Sticky
routing similarly exposes its kind and key source, never a sticky key value.

Some capture-derived values remain redacted from the committed content-free schema signature even
though they are emitted to telemetry. In particular, opaque turn-state metadata is not decrypted,
but its captured value can reach Loki metadata and span attributes. The private dashboard may read
bounded runtime account-email values from `codexlb.account.info`; never hard-code or export actual
email addresses, paths, identifiers, turn-state values, or conversation excerpts into dashboard
definitions, Git, tests, task records, or documentation.

## Function arguments

Function-call `input` is retained as valid JSON after reduction. The detector walks maps and arrays,
including a root string. It replaces a string when its key contains `encrypted` (case-insensitive)
or when its decoded URL-safe-base64 bytes have Fernet version `0x80` and are at least 73 bytes long.
The replacement is the JSON string `"[omitted: encrypted]"`; `input_omitted` counts replacements.
Non-string leaves remain unchanged. A map or array below an encrypted-named key keeps its structure,
while string descendants in that context are replaced.

`input_chars` records the original argument byte length. The reducer has no input-specific bound, so
it uses `MaxToolOutputChars`, whose default is 4096; `input_truncated` marks a bound replacement.
The summary package's `MaxCharsPerToolInput` is applied later while rendering and does not alter
capture. Malformed argument text is retained as a JSON string, and any bound fallback remains valid
JSON. Custom tool calls retain their existing behavior. Function-call extraction populates the
`spawn_agent` task, model, and effort fields when those arguments are present. This handling does not
decrypt reasoning content.

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

Postgres enrichment and the optional account poller are read-only. Use an existing role with `SELECT`
on `request_logs`, `api_keys`, `accounts`, `usage_history`, `additional_usage_history`, and
`api_key_accounts`; the service does not create roles, alter grants, or write request data. Keep
`postgres.enabled` and `account_poller.enabled` false when no such DSN is available. A database
outage must affect only the optional database signals, not archive ingestion or another enabled sink.

Upstream status, error code, and transport are bounded diagnostics carried in the turn body and
response span. Freeform database or error bodies, failure detail, client addresses, endpoint
identifiers, and other unbounded columns stay out of the enrichment output. Proxy waits preserve
null versus zero and are recorded as separate measurements rather than an inferred total.

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
observable metadata such as reasoning token counts; encrypted reasoning text remains opaque. Camden
also keeps Tempo traces and Agent Observability generations disabled permanently, so source-level
trace-link tests do not imply live content delivery there.
