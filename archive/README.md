# GitHub Issues archive

`issues-dump.json` is the complete GitHub Issues history of this repo, captured on 2026-08-14 just
before the issues were deleted. It is **the record**, not a pointer: the issues it describes no
longer exist on GitHub, so `gh issue view <N>` will not resolve them.

**39 issues (8 open, 31 closed), 52 comments.** Comment completeness was verified against the REST
API's own per-issue `comments` counts rather than trusting `gh --json comments`, which paginates:
both sides summed to 52.

The 8 issues that were open at capture time were migrated into the Backlog.md board as tasks before
deletion. `backlog task list --plain` is the live queue; this file is history. The closed set is
indexed, one row per issue, in the "Closed GitHub Issues (#1-#42)" document — `backlog doc list --plain`.

## Reading it

```bash
# one issue by number
jq '.[] | select(.number == 32)' archive/issues-dump.json

# titles and close dates, newest first
jq -r '.[] | select(.state=="CLOSED") | "\(.number)\t\(.closedAt[0:10])\t\(.title)"' archive/issues-dump.json | sort -rn

# full body and comment thread of one issue, as text
jq -r '.[] | select(.number == 19) | .body, (.comments[] | "\n--- comment \(.createdAt) ---\n\(.body)")' archive/issues-dump.json
```

## Redaction

**This file is redacted.** One real value maps to one placeholder everywhere, so cross-issue
correlation survives without the identifier:

| Placeholder | What it stood for |
| --- | --- |
| `<TAILNET_HOST_IP>` | the tailnet address the service was reachable on |
| `<LAN_HOST_IP>` | the same host's LAN address |
| `/Users/<user>` | the absolute path prefix of local developer checkouts |
| `<GRAFANA_STACK_ID>` | the Grafana Cloud stack id used for OTLP gateway auth |
| `<LOKI_TENANT_ID>` | the Loki tenant id, a different number in the same stack |

The sweep that certified this clean ran over the **decoded string fields**, not the serialized JSON.
That distinction is not cosmetic: in `json.dumps` output an escape such as `\n` leaves a literal `n`
against the following word and breaks a `\b` word boundary, so a blob sweep can report a file clean
while it still leaks.

**Deliberately not redacted:** the host name `camden`, the service host name `codexlb.family`, the
Grafana Cloud endpoint host names, and the `m7kni` org name. Each is already part of the tracked
source — `camden` alone appears in 15 committed files, including Go source and tests — so redacting
it here would break correlation with the code it describes while removing nothing from git history
that the checkout does not already carry. If those are ever scrubbed from the repo, scrub them here
in the same change.
