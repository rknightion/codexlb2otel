# Wave 5 frozen seams

This file freezes the one new cross-package contract introduced in wave 5. Every seam in
`codex/wave4-seams.md` predates this wave and remains immutable.

## Poll outcome metric

```go
MetricSelfAccountPolls = "codexlb.selfobs.account_polls" // counter, {poll}, by codexlb.selfobs.result
```

The metric is an `Int64Counter` with description `Account poller attempts by bounded result.` and
unit `{poll}`. Its exact attribute set is `codexlb.selfobs.result` and nothing else. In particular,
it does not carry an account id because it reports one outcome for a whole poll attempt.

The closed result set is `success`, `error`, and `disabled`. The existing
`attr.SelfObsResult` field remains `Bounded` with cap 16; no new attribute key or registry row is
created.

## Reporter interface

The producer in `internal/accountpoll` exposes:

```go
// OutcomeReporter receives one call per completed poll attempt.
// Implementations must not block: the poller calls this while holding pollMu.
type OutcomeReporter interface {
	ReportPoll(result string)
}
```

`accountpoll.Options` gains `Reporter OutcomeReporter`. A nil reporter is valid and emits nothing.
The producer reports `success` after publishing a snapshot, `error` on every poll failure including
a query timeout, and `disabled` once at construction when polling is configured off.

The metric sink owns the consuming implementation and instrument registration. The poller never
imports the metric sink.

## Amendment 1 - store injection for the seam regression

Frozen by the root after dispatch under the delegated current-wave seam authority. The required
cross-package regression cannot construct a real `Poller` around a blocking store while the
poller's store and rows interfaces and its store-injected constructor are package-private. Moving
the test into `accountpoll` would import `otlpmetric` back into the package that it already imports,
creating a cycle.

`internal/accountpoll` therefore exports its existing minimal `Rows` and `Store` interfaces and a
`NewWithStore(source Store, opts Options) (*Poller, error)` constructor. The constructor validates
options and rejects a nil store. `New` remains the production pgx-pool constructor. These names are
reachable only inside this module because Go's `internal` boundary remains in force; they do not
change a telemetry name, attribute set, poll outcome, or database query.
