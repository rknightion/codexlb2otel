---
id: CXO-0041
title: 'Capture the quota snapshot the 429 error event carries, and emit limit_reached'
status: To Do
assignee: []
created_date: '2026-09-12 10:07'
labels:
  - wire
  - telemetry
  - metrics
dependencies: []
priority: high
type: enhancement
ordinal: 40000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
When an account is refused for quota, the archive error event carries a complete account quota snapshot that nothing reads. This is the one moment the reading matters most, and it is also the only reading available for a turn that died before any codex.rate_limits event was emitted.

Measured shape, from the live capture on 2026-09-12:

  {"type":"error",
   "error":{"type":"usage_limit_reached","message":"...","plan_type":"prolite",
            "resets_at":<unix seconds, number>,"resets_in_seconds":<number>,
            "eligible_promo":null},
   "status_code":429,
   "headers":{ 34 string-valued headers }}

The 34 headers are three parallel limit families with identical sub-shapes:
  X-Codex-{Primary,Secondary}-{Used-Percent,Window-Minutes,Reset-At,Reset-After-Seconds}
  X-Codex-Primary-Over-Secondary-Limit-Percent
  X-Codex-Bengalfox-* (same six, plus X-Codex-Bengalfox-Limit-Name)
  X-Base-Model-Inference-* (same six, plus X-Base-Model-Inference-Limit-Name)
  X-Codex-Active-Limit, X-Codex-Plan-Type
  X-Codex-Credits-{Has-Credits,Balance,Unlimited}

Traps measured, not assumed. Every value is a STRING even when it is numeric. Booleans are the Python spellings "True" and "False", not "true"/"false". An absent window is the EMPTY STRING, not "0" and not absent: X-Codex-Secondary-Reset-At was "" while X-Codex-Secondary-Reset-After-Seconds was "0" in the same header set, so empty and zero are different observations and the zero is real. The two *-Limit-Name headers carry model-family names and are the only high-information labels in the set. The header block is absent entirely on the status/400 error shape.

Separately, two fields that ARE already reduced reach no sink at all. `Turn.RateLimitReached` and `Turn.RateLimitAllowed` are set by applyRateLimits and appear in no instrument and no Loki field, so "were we actually blocked" has no series. The full-corpus scan saw rate_limits.limit_reached=true and rate_limits.allowed=false for the first time in this capture, so the values are live, not theoretical.

Per-model, the same two fields are decoded into rateLimitBlock and then discarded: the additional_rate_limits loop keeps only the windows. The scan also found a second per-model family, additional_rate_limits.gpt-reserve, carrying allowed, limit_reached, primary.{used_percent,window_minutes,reset_after_seconds,reset_at} and an explicit null secondary. reset_at is a window field the existing RateLimitWindow struct does not carry.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 The error event quota snapshot is reduced onto the Turn: plan type, resets_at, resets_in_seconds, and per limit family the used percent, window minutes, reset-after seconds and limit name
- [ ] #2 Empty-string header values are distinguished from zero and are omitted rather than recorded as 0
- [ ] #3 The Python-spelled booleans in the credits headers are parsed case-insensitively and a value that is neither True nor False is omitted, not defaulted
- [ ] #4 Quota-at-failure metrics exist and carry the account id and the limit family as bounded attributes, with the limit-family attribute capped in internal/attr
- [ ] #5 codexlb.rate_limit.limit_reached is emitted from the existing Turn.RateLimitReached and Turn.RateLimitAllowed fields, per account
- [ ] #6 Per-model allowed and limit_reached from additional_rate_limits are preserved rather than discarded, and reset_at is added to RateLimitWindow
- [ ] #7 A test drives a captured 429 error event through the reducer and asserts every extracted field, including one empty-string window
- [ ] #8 No header value reaches telemetry as a raw header name; the attribute keys are codexlb-namespaced
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 just check passes: fmt-check, lint, build, test-short and probe-ci all clean
<!-- DOD:END -->
