# Wave 4 frozen seams

This file is the verbatim contract for lanes L1 to L6. `just check` is expected to be RED until L6 adds all eighteen panels; no lane may repair that by renaming or removing a metric. `internal/attr/names.go` and `internal/attr/attr.go` are frozen after this file's commit. A lane needing another name stops and returns the uncovered decision.

## Turn fields and carriers

The following fields are added to `turn.Turn` and keep these exact Go names and types:

```go
ProxyLatencyMS                float64
ProxyFirstTokenMS             float64
ConversationID                string
ProxySessionID                string
ProxyServiceTier              string
ProxyActualServiceTier        string
ProxyRequestedServiceTier     string
ServiceTierOutcome            string
StickyKind                    string
StickyKeySource               string
ProxyRouteMode                string
RoutingHintAgreement          string
ContentItemKinds              map[string]int
CompactionTrigger             string
CompactionReason              string
CompactionImplementation      string
CompactionPhase               string
CompactionStrategy            string
Workspaces                    string
TurnState                     string
ModelsETag                    string
ContextWindowID               string
PassthroughTurnID             string
PassthroughCreateTime         float64
ExtraRateLimitAllowed         map[string]bool
ExtraRateLimitReached         map[string]bool
QuotaFailureLimits            []QuotaFailureLimit
QuotaFailureActiveLimit       string
QuotaFailureResetsAt          *float64
QuotaFailureResetsInSeconds   *float64
QuotaFailureCreditsHas        *bool
QuotaFailureCreditsUnlimited  *bool
QuotaFailureCreditsBalance    string
```

`RateLimitWindow` gains `ResetAt float64`. Map membership distinguishes an observed `false` from an absent per-model boolean.

The 429 carrier types are:

```go
type QuotaFailureLimit struct {
    Family                           string
    LimitName                        string
    PrimaryOverSecondaryLimitPercent *float64
    Windows                          []QuotaFailureWindow
}

type QuotaFailureWindow struct {
    Window            string
    UsedPercent       *float64
    WindowMinutes     *int
    ResetAt           *float64
    ResetAfterSeconds *float64
}
```

Pointer measurements distinguish an omitted or empty-string header from a real observed zero. `PlanType`, `ErrorType`, `ErrorMessage`, `UpstreamStatusCode`, `UpstreamTransport`, `RateLimitReached`, `RateLimitAllowed`, `RateLimitWindowMin`, `AccountID`, `APIKeyName`, `Model`, `Family` and `ConnectionKind` remain the existing shared fields.

## Attribute keys

Every new bounded key has cap 100. Identity keys are `ContentOnly` and are never metric attributes or Loki stream labels.

| Go constant | Key | Class | Observed values |
|---|---|---|---|
| `QuotaLimitFamily` | `codexlb.quota.limit_family` | Bounded | `codex`, `bengalfox`, `base_model_inference` |
| `QuotaLimitName` | `codexlb.quota.limit_name` | Bounded | `GPT-5.3-Codex-Spark`, `gpt-reserve` |
| `QuotaActiveLimit` | `codexlb.quota.active_limit` | Bounded | `premium` |
| `QuotaWindow` | `codexlb.quota.window` | Bounded | `primary`, `secondary` |
| `QuotaKey` | `codexlb.quota.key` | Bounded | `codex_spark`, `gpt_reserve` |
| `RoutingHintAgreement` | `codexlb.routing_hint.agreement` | Bounded | `agree`, `disagree`, `absent` |
| `ContentItemKind` | `codexlb.content_item_kind` | Bounded | `model.base_instructions`, `model_switch.instructions`, `memories.instructions`, `host_skills.instructions`, `permissions.instructions`, `collaboration_mode.instructions`, `apps.instructions`, `plugins.usage_instructions`, `plugins.recommendations`, `multi_agent.usage_hint`, `multi_agent.mode_instructions`, `multi_agent.role_instructions`, `agents_md.instructions`, `environments.environment_context`, `hooks.additional_context`, `user.text`, `generic.turn_aborted`, `unknown` |
| `CompactionTrigger` | `codexlb.compaction.trigger` | Bounded | `manual` |
| `CompactionReason` | `codexlb.compaction.reason` | Bounded | `user_requested` |
| `CompactionImplementation` | `codexlb.compaction.implementation` | Bounded | `responses_compaction_v2` |
| `CompactionPhase` | `codexlb.compaction.phase` | Bounded | `standalone_turn` |
| `CompactionStrategy` | `codexlb.compaction.strategy` | Bounded | `memento` |
| `ServiceTierOutcome` | `codexlb.service_tier.outcome` | Bounded | `granted`, `downgraded`, `not_requested`, `unknown` |
| `StickyKind` | `codexlb.sticky.kind` | Bounded | `prompt_cache` |
| `StickyKeySource` | `codexlb.sticky.key_source` | Bounded | `thread_header` |
| `ProxyRouteMode` | `codexlb.proxy.route_mode` | Bounded | `direct` |
| `AccountStatus` | `codexlb.account.status` | Bounded | `active`, `rate_limited`, `quota_exceeded`, `paused`, `reauth_required`, `deactivated` |
| `AccountRoutingPolicy` | `codexlb.account.routing_policy` | Bounded | `burn_first`, `normal`, `preserve` |
| `AccountEmail` | `codexlb.account.email` | Bounded | no values committed; live values only |
| `Workspaces` | `codexlb.workspaces` | Identity, ContentOnly | not enumerated |
| `TurnState` | `codexlb.turn_state` | Identity, ContentOnly | not enumerated |
| `ConversationID` | `codexlb.conversation_id` | Identity, ContentOnly | not enumerated |
| `ProxySessionID` | `codexlb.proxy.session_id` | Identity, ContentOnly | not enumerated |
| `ModelsETag` | `codexlb.models_etag` | Identity, ContentOnly | not enumerated |
| `ContextWindowID` | `codexlb.context_window_id` | Identity, ContentOnly | not enumerated |
| `PassthroughTurnID` | `codexlb.passthrough.turn_id` | Identity, ContentOnly | not enumerated |
| `PassthroughCreateTime` | `codexlb.passthrough.create_time` | Identity, ContentOnly | not enumerated |

Reuse without renaming: `AccountID`, `PlanType`, `APIKeyName`, `RateLimitWindowMinutes`, `ServiceTier`, `ServiceTierRequested`, `Family`, `ConnectionKind`, `GenAIRequestModel` and `ErrorType`. `SafetyID` is now Identity. The Sensitive class and every Sensitive gate are gone.

## Metric names and exact attribute sets

`attr.Only` must name exactly the attributes below. Any attribute not listed is deliberately dropped to contain series cardinality; `gen_ai.provider.name` and `gen_ai.operation.name` are not implicit on the database snapshot gauges.

| Go constant | Metric | Kind | Exact attributes |
|---|---|---|---|
| `MetricRateLimitReached` | `codexlb.rate_limit.limit_reached` | gauge 0 or 1 | `codexlb.account_id` |
| `MetricRateLimitModelReached` | `codexlb.rate_limit.model_limit_reached` | gauge 0 or 1 | `codexlb.account_id`, `gen_ai.request.model` |
| `MetricQuotaFailures` | `codexlb.quota_failures` | counter | `codexlb.account_id`, `codexlb.plan_type`, `codexlb.quota.active_limit`, `error.type` |
| `MetricQuotaFailureUsed` | `codexlb.quota_failure.used_percent` | gauge percent | `codexlb.account_id`, `codexlb.quota.limit_family`, `codexlb.quota.window` |
| `MetricQuotaFailureReset` | `codexlb.quota_failure.reset_after` | gauge seconds | `codexlb.account_id`, `codexlb.quota.limit_family`, `codexlb.quota.window` |
| `MetricRoutingHintAgreement` | `codexlb.routing_hint_agreement` | counter | `codexlb.routing_hint.agreement`, `codexlb.family`, `codexlb.connection_kind` |
| `MetricContentItemKinds` | `codexlb.content_item_kinds` | counter | `codexlb.content_item_kind`, `codexlb.family` |
| `MetricCompactions` | `codexlb.compactions` | counter | `codexlb.compaction.trigger`, `codexlb.compaction.reason`, `codexlb.compaction.strategy`, `codexlb.compaction.phase` |
| `MetricServiceTierOutcome` | `codexlb.service_tier_outcome` | counter | `codexlb.service_tier.outcome`, `gen_ai.request.model`, `codexlb.family` |
| `MetricStickyRouting` | `codexlb.sticky_routing` | counter | `codexlb.sticky.kind`, `codexlb.sticky.key_source`, `codexlb.family` |
| `MetricProxyLatency` | `codexlb.proxy_latency` | histogram seconds | `codexlb.family`, `codexlb.connection_kind`, `gen_ai.request.model` |
| `MetricProxyFirstToken` | `codexlb.proxy_first_token` | histogram seconds | `codexlb.family`, `codexlb.connection_kind`, `gen_ai.request.model` |
| `MetricAccountQuotaUsed` | `codexlb.account.quota_used_percent` | gauge percent | `codexlb.account_id`, `codexlb.quota.window`, `codexlb.plan_type`, `codexlb.rate_limit.window_minutes` |
| `MetricAccountQuotaReset` | `codexlb.account.quota_reset_after` | gauge seconds | `codexlb.account_id`, `codexlb.quota.window`, `codexlb.rate_limit.window_minutes` |
| `MetricAccountModelQuotaUsed` | `codexlb.account.model_quota_used_percent` | gauge percent | `codexlb.account_id`, `codexlb.quota.key`, `codexlb.quota.window`, `codexlb.rate_limit.window_minutes` |
| `MetricAccountCreditsBalance` | `codexlb.account.credits_balance` | gauge | `codexlb.account_id` |
| `MetricAccountInfo` | `codexlb.account.info` | gauge 1 | `codexlb.account_id`, `codexlb.account.email`, `codexlb.account.status`, `codexlb.plan_type`, `codexlb.account.routing_policy` |
| `MetricAccountAPIKeyEligible` | `codexlb.account.api_key_eligible` | gauge 0 or 1 | `codexlb.account_id`, `codexlb.api_key_name` |

`QuotaLimitName` and `CompactionImplementation` are retained as structured metadata and span attributes but are deliberately absent from the metric sets above because D6 does not grant those dimensions. Identity keys are always absent from every metric set. No new attribute becomes a Loki stream label.

## Account poller configuration

The root-owned config seam is:

```go
type AccountPoller struct {
    Enabled      bool
    DSN          Secret
    Interval     time.Duration
    QueryTimeout time.Duration
}
```

It is `Config.AccountPoller`, YAML key `account_poller`, disabled by default, using `${CODEXLB2OTEL_POSTGRES_DSN}`, a two-minute interval and a two-second query timeout. Runtime validation is local to the optional poller so a bad database setting cannot stop another signal.

## Account snapshot type

L3 creates these exact exported types in package `internal/accountpoll`; L4 consumes them without a parallel adapter:

```go
type Snapshot struct {
    CollectedAt time.Time
    Accounts    []Account
}

type Account struct {
    AccountID     string
    Email         string
    Status        string
    PlanType      string
    RoutingPolicy string
    Quotas        []Quota
    ModelQuotas   []ModelQuota
    CreditsBalance *float64
    APIKeys       []APIKeyEligibility
}

type Quota struct {
    Window            string
    UsedPercent       *float64
    ResetAfterSeconds *float64
    WindowMinutes     int
}

type ModelQuota struct {
    QuotaKey          string
    LimitName         string
    Window            string
    UsedPercent       *float64
    ResetAfterSeconds *float64
    WindowMinutes     int
}

type APIKeyEligibility struct {
    Name     string
    Eligible bool
}
```

`AccountID` is the bare UUID prefix stripped from the database suffix. The poller publishes a deep immutable copy; observable callbacks read that snapshot under a short lock and perform no database IO. Reset-after values are computed at poll time from `reset_at` and `CollectedAt`, never from callback time. The five query sources are `accounts`, `usage_history`, `additional_usage_history`, `api_keys` and `api_key_accounts`; every use of the database column named `window` is quoted, and the latest-window expression uses `COALESCE("window", 'primary')`.
