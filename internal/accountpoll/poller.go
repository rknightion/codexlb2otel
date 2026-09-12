// Package accountpoll snapshots account and quota state from codex-lb's Postgres
// database. It deliberately keeps database work out of metric callbacks: callers
// read the last successful immutable Snapshot instead.
package accountpoll

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Snapshot is one complete account and quota reading collected by Poller.
type Snapshot struct {
	CollectedAt time.Time
	Accounts    []Account
}

// Account is the database state for one codex-lb account.
type Account struct {
	AccountID      string
	Email          string
	Status         string
	PlanType       string
	RoutingPolicy  string
	Quotas         []Quota
	ModelQuotas    []ModelQuota
	CreditsBalance *float64
	APIKeys        []APIKeyEligibility
}

// Quota is the latest account-wide quota observation for one window.
type Quota struct {
	Window            string
	UsedPercent       *float64
	ResetAfterSeconds *float64
	WindowMinutes     int
}

// ModelQuota is the latest quota observation for one named quota key and window.
type ModelQuota struct {
	QuotaKey          string
	LimitName         string
	Window            string
	UsedPercent       *float64
	ResetAfterSeconds *float64
	WindowMinutes     int
}

// APIKeyEligibility says whether a named API key can route to this account.
type APIKeyEligibility struct {
	Name     string
	Eligible bool
}

// Options controls the polling schedule and the bounded lifetime of each query.
type Options struct {
	Interval     time.Duration
	QueryTimeout time.Duration
	Now          func() time.Time
	OnError      func(error)
}

// Poller reads account state on its own schedule and retains its most recent
// successful reading. Poll serializes database work, but Snapshot never waits for
// that work: its lock protects only replacing or copying an already-published
// value.
type Poller struct {
	store store
	opts  Options

	pollMu sync.Mutex
	snapMu sync.RWMutex
	snap   Snapshot
}

// New creates a pgx-backed account poller. Connecting or querying is deferred to
// Poll, so a temporarily unavailable optional database does not stop startup.
func New(ctx context.Context, dsn string, opts Options) (*Poller, error) {
	if err := validateOptions(opts); err != nil {
		return nil, err
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("accountpoll: create pool: %w", err)
	}
	return newPoller(&poolStore{pool: pool}, opts), nil
}

// Close releases the poller's database pool. It is safe to call more than once.
func (p *Poller) Close() {
	if p != nil && p.store != nil {
		p.store.Close()
	}
}

// Run polls immediately, then repeats at Options.Interval until ctx is cancelled.
// Individual database errors intentionally do not escape this loop; the previous
// successful snapshot remains available to the rest of the service.
func (p *Poller) Run(ctx context.Context) {
	if p == nil {
		return
	}
	p.report(p.Poll(ctx))
	ticker := time.NewTicker(p.opts.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.report(p.Poll(ctx))
		}
	}
}

func (p *Poller) report(err error) {
	if err != nil && p.opts.OnError != nil {
		p.opts.OnError(err)
	}
}

// Poll reads all five account and quota sources, then atomically publishes a deep
// immutable snapshot. A failed read leaves the previous snapshot untouched.
func (p *Poller) Poll(ctx context.Context) error {
	if p == nil || p.store == nil {
		return fmt.Errorf("accountpoll: poller is not configured")
	}
	p.pollMu.Lock()
	defer p.pollMu.Unlock()

	snapshot, err := readSnapshot(ctx, p.store, p.opts.Now(), p.opts.QueryTimeout)
	if err != nil {
		return err
	}

	p.snapMu.Lock()
	p.snap = cloneSnapshot(snapshot)
	p.snapMu.Unlock()
	return nil
}

// Snapshot returns a deep copy of the latest successful polling result. It does
// no database IO and releases its short read lock before copying the immutable
// published snapshot.
func (p *Poller) Snapshot() Snapshot {
	if p == nil {
		return Snapshot{}
	}
	p.snapMu.RLock()
	snapshot := p.snap
	p.snapMu.RUnlock()
	return cloneSnapshot(snapshot)
}

func newPoller(store store, opts Options) *Poller {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Poller{store: store, opts: opts}
}

func validateOptions(opts Options) error {
	if opts.Interval <= 0 {
		return fmt.Errorf("accountpoll: interval must be positive")
	}
	if opts.QueryTimeout <= 0 {
		return fmt.Errorf("accountpoll: query timeout must be positive")
	}
	return nil
}

type rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close()
}

type store interface {
	Query(context.Context, string, ...any) (rows, error)
	Close()
}

type poolStore struct {
	pool *pgxpool.Pool
}

func (s *poolStore) Query(ctx context.Context, query string, args ...any) (rows, error) {
	return s.pool.Query(ctx, query, args...)
}

func (s *poolStore) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

func readSnapshot(ctx context.Context, source store, collectedAt time.Time, queryTimeout time.Duration) (Snapshot, error) {
	accounts, err := readAccounts(ctx, source, queryTimeout)
	if err != nil {
		return Snapshot{}, err
	}
	byID := make(map[string]*Account, len(accounts))
	for index := range accounts {
		byID[accounts[index].AccountID] = &accounts[index]
	}
	if err := readQuotas(ctx, source, byID, collectedAt, queryTimeout); err != nil {
		return Snapshot{}, err
	}
	if err := readModelQuotas(ctx, source, byID, collectedAt, queryTimeout); err != nil {
		return Snapshot{}, err
	}
	if err := readAPIKeys(ctx, source, byID, queryTimeout); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{CollectedAt: collectedAt, Accounts: accounts}, nil
}

func readAccounts(ctx context.Context, source store, queryTimeout time.Duration) ([]Account, error) {
	queryCtx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	rows, err := source.Query(queryCtx, accountsSQL)
	if err != nil {
		return nil, fmt.Errorf("accountpoll: accounts: %w", err)
	}
	defer rows.Close()

	var accounts []Account
	for rows.Next() {
		var account Account
		if err := rows.Scan(&account.AccountID, &account.Email, &account.Status, &account.PlanType, &account.RoutingPolicy); err != nil {
			return nil, fmt.Errorf("accountpoll: scan accounts: %w", err)
		}
		account.AccountID = bareAccountID(account.AccountID)
		accounts = append(accounts, account)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("accountpoll: accounts rows: %w", err)
	}
	return accounts, nil
}

func readQuotas(ctx context.Context, source store, accounts map[string]*Account, collectedAt time.Time, queryTimeout time.Duration) error {
	queryCtx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	rows, err := source.Query(queryCtx, usageSQL)
	if err != nil {
		return fmt.Errorf("accountpoll: usage history: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var accountID, window string
		var used, credits *float64
		var resetAt *time.Time
		var windowMinutes int
		if err := rows.Scan(&accountID, &window, &used, &resetAt, &windowMinutes, &credits); err != nil {
			return fmt.Errorf("accountpoll: scan usage history: %w", err)
		}
		account := accounts[bareAccountID(accountID)]
		if account == nil {
			continue
		}
		account.Quotas = append(account.Quotas, Quota{
			Window: window, UsedPercent: used, ResetAfterSeconds: resetAfter(resetAt, collectedAt), WindowMinutes: windowMinutes,
		})
		if credits != nil {
			account.CreditsBalance = credits
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("accountpoll: usage history rows: %w", err)
	}
	return nil
}

func readModelQuotas(ctx context.Context, source store, accounts map[string]*Account, collectedAt time.Time, queryTimeout time.Duration) error {
	queryCtx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	rows, err := source.Query(queryCtx, modelQuotaSQL)
	if err != nil {
		return fmt.Errorf("accountpoll: additional usage history: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var accountID, quotaKey, limitName, window string
		var used *float64
		var resetAt *time.Time
		var windowMinutes int
		if err := rows.Scan(&accountID, &quotaKey, &limitName, &window, &used, &resetAt, &windowMinutes); err != nil {
			return fmt.Errorf("accountpoll: scan additional usage history: %w", err)
		}
		account := accounts[bareAccountID(accountID)]
		if account == nil {
			continue
		}
		account.ModelQuotas = append(account.ModelQuotas, ModelQuota{
			QuotaKey: quotaKey, LimitName: limitName, Window: window, UsedPercent: used,
			ResetAfterSeconds: resetAfter(resetAt, collectedAt), WindowMinutes: windowMinutes,
		})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("accountpoll: additional usage history rows: %w", err)
	}
	return nil
}

func readAPIKeys(ctx context.Context, source store, accounts map[string]*Account, queryTimeout time.Duration) error {
	queryCtx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	rows, err := source.Query(queryCtx, apiKeyEligibilitySQL)
	if err != nil {
		return fmt.Errorf("accountpoll: API key eligibility: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var accountID, name string
		var eligible bool
		if err := rows.Scan(&accountID, &name, &eligible); err != nil {
			return fmt.Errorf("accountpoll: scan API key eligibility: %w", err)
		}
		account := accounts[bareAccountID(accountID)]
		if account != nil {
			account.APIKeys = append(account.APIKeys, APIKeyEligibility{Name: name, Eligible: eligible})
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("accountpoll: API key eligibility rows: %w", err)
	}
	return nil
}

func bareAccountID(id string) string {
	if prefix, _, found := strings.Cut(id, "_"); found {
		return prefix
	}
	return id
}

func resetAfter(resetAt *time.Time, collectedAt time.Time) *float64 {
	if resetAt == nil {
		return nil
	}
	seconds := resetAt.Sub(collectedAt).Seconds()
	return &seconds
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	clone := Snapshot{CollectedAt: snapshot.CollectedAt, Accounts: make([]Account, len(snapshot.Accounts))}
	for index, account := range snapshot.Accounts {
		copy := account
		copy.CreditsBalance = cloneFloat(account.CreditsBalance)
		copy.Quotas = make([]Quota, len(account.Quotas))
		for quotaIndex, quota := range account.Quotas {
			copy.Quotas[quotaIndex] = quota
			copy.Quotas[quotaIndex].UsedPercent = cloneFloat(quota.UsedPercent)
			copy.Quotas[quotaIndex].ResetAfterSeconds = cloneFloat(quota.ResetAfterSeconds)
		}
		copy.ModelQuotas = make([]ModelQuota, len(account.ModelQuotas))
		for quotaIndex, quota := range account.ModelQuotas {
			copy.ModelQuotas[quotaIndex] = quota
			copy.ModelQuotas[quotaIndex].UsedPercent = cloneFloat(quota.UsedPercent)
			copy.ModelQuotas[quotaIndex].ResetAfterSeconds = cloneFloat(quota.ResetAfterSeconds)
		}
		copy.APIKeys = append([]APIKeyEligibility(nil), account.APIKeys...)
		clone.Accounts[index] = copy
	}
	return clone
}

func cloneFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

const accountsSQL = `
SELECT
	id,
	COALESCE(email, '') AS email,
	COALESCE(status::text, '') AS status,
	COALESCE(plan_type, '') AS plan_type,
	COALESCE(routing_policy, '') AS routing_policy
FROM accounts
ORDER BY id`

const usageSQL = `
SELECT DISTINCT ON (account_id, COALESCE("window", 'primary'))
	account_id,
	COALESCE("window", 'primary') AS quota_window,
	used_percent,
	to_timestamp(reset_at) AS reset_at,
	window_minutes,
	(
		SELECT latest.credits_balance
		FROM usage_history latest
		WHERE latest.account_id = usage_history.account_id
			AND latest.credits_balance IS NOT NULL
		ORDER BY latest.recorded_at DESC
		LIMIT 1
	) AS credits_balance
FROM usage_history
ORDER BY account_id, COALESCE("window", 'primary'), recorded_at DESC`

const modelQuotaSQL = `
SELECT DISTINCT ON (account_id, quota_key, COALESCE("window", 'primary'))
	account_id,
	quota_key,
	limit_name,
	COALESCE("window", 'primary') AS quota_window,
	used_percent,
	to_timestamp(reset_at) AS reset_at,
	window_minutes
FROM additional_usage_history
ORDER BY account_id, quota_key, COALESCE("window", 'primary'), recorded_at DESC`

// #nosec G101 -- the SQL names api_keys; it contains no credential.
const apiKeyEligibilitySQL = `
SELECT
	a.id,
	COALESCE(k.name, '') AS name,
	(k.is_active AND (
		NOT k.account_assignment_scope_enabled OR EXISTS (
			SELECT 1
			FROM api_key_accounts aka
			WHERE aka.api_key_id = k.id AND aka.account_id = a.id
		)
	)) AS eligible
FROM accounts a
CROSS JOIN api_keys k
ORDER BY a.id, k.name`

var _ store = (*poolStore)(nil)
