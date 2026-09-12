package accountpoll

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewWithStoreRejectsTypedNil(t *testing.T) {
	var store *fakeStore
	poller, err := NewWithStore(store, Options{Interval: time.Minute, QueryTimeout: time.Second})
	if err == nil || err.Error() != "accountpoll: store is required" {
		t.Fatalf("NewWithStore() error = %v, want accountpoll: store is required", err)
	}
	if poller != nil {
		t.Fatalf("NewWithStore() poller = %#v, want nil", poller)
	}
}

func TestPollerSnapshotDoesNotBlockWhilePollQueries(t *testing.T) {
	store := &blockingStore{started: make(chan struct{}), release: make(chan struct{})}
	poller := newPoller(store, Options{Interval: time.Minute, QueryTimeout: time.Second})

	pollDone := make(chan error, 1)
	go func() { pollDone <- poller.Poll(context.Background()) }()
	<-store.started

	readDone := make(chan Snapshot, 1)
	go func() { readDone <- poller.Snapshot() }()
	select {
	case snapshot := <-readDone:
		if !snapshot.CollectedAt.IsZero() || len(snapshot.Accounts) != 0 {
			t.Fatalf("Snapshot() = %#v while first poll is blocked, want empty snapshot", snapshot)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Snapshot() blocked while Poll() was waiting on database IO")
	}

	close(store.release)
	if err := <-pollDone; err != nil {
		t.Fatalf("Poll() error = %v", err)
	}
}

func TestPollerPublishesDeepImmutableSnapshot(t *testing.T) {
	now := time.Date(2026, time.September, 12, 12, 0, 0, 0, time.UTC)
	used := 42.5
	resetAt := now.Add(15 * time.Minute)
	credits := 9.25
	store := &fakeStore{rows: map[string][][]any{
		accountsSQL:          {{"11111111-1111-1111-1111-111111111111_deadbeef", "account@example.invalid", "active", "pro", "normal"}},
		usageSQL:             {{"11111111-1111-1111-1111-111111111111_deadbeef", "primary", &used, &resetAt, 60, &credits}},
		modelQuotaSQL:        {{"11111111-1111-1111-1111-111111111111_deadbeef", "codex_spark", "Codex Spark", "primary", &used, &resetAt, 60}},
		apiKeyEligibilitySQL: {{"11111111-1111-1111-1111-111111111111_deadbeef", "scoped-key", false}},
	}}
	poller := newPoller(store, Options{Interval: time.Minute, QueryTimeout: time.Second, Now: func() time.Time { return now }})
	if err := poller.Poll(context.Background()); err != nil {
		t.Fatalf("Poll() error = %v", err)
	}

	got := poller.Snapshot()
	if got.CollectedAt != now || len(got.Accounts) != 1 {
		t.Fatalf("Snapshot() = %#v, want one account collected at %s", got, now)
	}
	account := got.Accounts[0]
	if account.AccountID != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("AccountID = %q, want bare UUID", account.AccountID)
	}
	if len(account.Quotas) != 1 || account.Quotas[0].ResetAfterSeconds == nil || *account.Quotas[0].ResetAfterSeconds != 900 {
		t.Fatalf("Quotas = %#v, want reset after 900 seconds", account.Quotas)
	}
	if len(account.APIKeys) != 1 || account.APIKeys[0].Eligible {
		t.Fatalf("APIKeys = %#v, want scope-enabled unassigned key ineligible", account.APIKeys)
	}

	*got.Accounts[0].Quotas[0].UsedPercent = 0
	got.Accounts[0].APIKeys[0].Name = "mutated"
	again := poller.Snapshot()
	if *again.Accounts[0].Quotas[0].UsedPercent != used || again.Accounts[0].APIKeys[0].Name != "scoped-key" {
		t.Fatalf("Snapshot() aliases published state: %#v", again)
	}
}

func TestPollerQueriesFrozenSourcesAndQuotesWindow(t *testing.T) {
	base := &fakeStore{rows: map[string][][]any{
		accountsSQL:          nil,
		usageSQL:             nil,
		modelQuotaSQL:        nil,
		apiKeyEligibilitySQL: nil,
	}}
	store := &windowCheckingStore{store: base}
	poller := newPoller(store, Options{Interval: time.Minute, QueryTimeout: time.Second})
	if err := poller.Poll(context.Background()); err != nil {
		t.Fatalf("Poll() error = %v", err)
	}
	if !strings.Contains(usageSQL, `COALESCE("window", 'primary')`) {
		t.Fatalf("usage query does not use indexed nullable window expression:\n%s", usageSQL)
	}
	if !strings.Contains(modelQuotaSQL, `history."window"`) {
		t.Fatalf("model query does not quote the window column:\n%s", modelQuotaSQL)
	}
	if !strings.Contains(apiKeyEligibilitySQL, "api_keys") || !strings.Contains(apiKeyEligibilitySQL, "api_key_accounts") {
		t.Fatalf("eligibility query does not read both key sources:\n%s", apiKeyEligibilitySQL)
	}
}

func TestQuotaQueriesConvertEpochResetAtToTimestamp(t *testing.T) {
	for name, query := range map[string]string{
		"usage history":            usageSQL,
		"additional usage history": modelQuotaSQL,
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(query, "to_timestamp(reset_at)") {
				t.Fatalf("query scans integer reset_at without converting it to a timestamp:\n%s", query)
			}
		})
	}
}

func TestQuotaQueriesKeepQuietAccountsWithOldLatestRows(t *testing.T) {
	for name, query := range map[string]string{
		"usage history":            usageSQL,
		"additional usage history": modelQuotaSQL,
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(query, "FROM accounts AS account") {
				t.Fatalf("query is not driven by the bounded accounts population:\n%s", query)
			}
			if !strings.Contains(query, "CROSS JOIN (VALUES ('primary'), ('secondary'))") {
				t.Fatalf("query does not use the known bounded window population:\n%s", query)
			}
			if strings.Contains(query, "recorded_at >=") || strings.Contains(query, "recorded_at >") {
				t.Fatalf("query drops a quiet account whose latest quota row predates a time cutoff:\n%s", query)
			}
		})
	}
}

func TestPostgresIntegration_AccountPollerGatedByDSN(t *testing.T) {
	dsn := os.Getenv("CLB_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("CLB_TEST_PG_DSN is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	poller, err := New(ctx, dsn, Options{
		Interval:     time.Minute,
		QueryTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer poller.Close()
	if err := poller.Poll(ctx); err != nil {
		t.Fatalf("Poll() error = %v", err)
	}

	snapshot := poller.Snapshot()
	var quotas, modelQuotas, apiKeys int
	for _, account := range snapshot.Accounts {
		if strings.Contains(account.AccountID, "_") {
			t.Fatal("account poller emitted a suffixed account id")
		}
		quotas += len(account.Quotas)
		modelQuotas += len(account.ModelQuotas)
		apiKeys += len(account.APIKeys)
	}
	if len(snapshot.Accounts) == 0 || quotas == 0 || modelQuotas == 0 || apiKeys == 0 {
		t.Fatalf(
			"snapshot population: accounts=%d quotas=%d model_quotas=%d api_keys=%d; want every family non-empty",
			len(snapshot.Accounts), quotas, modelQuotas, apiKeys,
		)
	}
}

func TestPollerKeepsLastSnapshotWhenDatabaseFails(t *testing.T) {
	now := time.Date(2026, time.September, 12, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{rows: map[string][][]any{
		accountsSQL:          {{"11111111-1111-1111-1111-111111111111_deadbeef", "", "active", "pro", "normal"}},
		usageSQL:             nil,
		modelQuotaSQL:        nil,
		apiKeyEligibilitySQL: nil,
	}}
	poller := newPoller(store, Options{Interval: time.Minute, QueryTimeout: time.Second, Now: func() time.Time { return now }})
	if err := poller.Poll(context.Background()); err != nil {
		t.Fatalf("initial Poll() error = %v", err)
	}
	store.err = errors.New("database unavailable")
	if err := poller.Poll(context.Background()); err == nil {
		t.Fatal("Poll() error = nil, want database error")
	}
	if got := poller.Snapshot(); len(got.Accounts) != 1 || got.CollectedAt != now {
		t.Fatalf("Snapshot() after failed poll = %#v, want prior published snapshot", got)
	}
}

func TestPollerReportsCompletedOutcomes(t *testing.T) {
	reporter := &recordingReporter{}
	store := &fakeStore{rows: map[string][][]any{
		accountsSQL:          nil,
		usageSQL:             nil,
		modelQuotaSQL:        nil,
		apiKeyEligibilitySQL: nil,
	}}
	poller := newPoller(store, Options{Interval: time.Minute, QueryTimeout: time.Second, Reporter: reporter})
	if err := poller.Poll(context.Background()); err != nil {
		t.Fatalf("successful Poll() error = %v", err)
	}
	store.err = errors.New("database unavailable")
	if err := poller.Poll(context.Background()); err == nil {
		t.Fatal("failed Poll() error = nil")
	}
	if got, want := reporter.Results(), []string{"success", "error"}; !equalStrings(got, want) {
		t.Fatalf("ReportPoll calls = %q, want %q", got, want)
	}
}

func TestPollerRunReportsDatabaseFailureAndKeepsRunning(t *testing.T) {
	store := &fakeStore{err: errors.New("database unavailable")}
	reported := make(chan error, 1)
	poller := newPoller(store, Options{
		Interval:     time.Millisecond,
		QueryTimeout: time.Second,
		OnError: func(err error) {
			select {
			case reported <- err:
			default:
			}
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		poller.Run(ctx)
		close(done)
	}()

	select {
	case err := <-reported:
		if !strings.Contains(err.Error(), "database unavailable") {
			t.Fatalf("reported error = %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Run did not report the initial database failure")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Run did not stop after cancellation")
	}
}

func TestPollerRunKeepsRunningAfterDatabaseFault(t *testing.T) {
	store := &errorStore{started: make(chan struct{})}
	poller := newPoller(store, Options{Interval: time.Hour, QueryTimeout: time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		poller.Run(ctx)
		close(done)
	}()
	<-store.started
	select {
	case <-done:
		t.Fatal("Run() returned after a database fault instead of isolating it")
	default:
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run() did not stop after its context was cancelled")
	}
}

type blockingStore struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingStore) Query(context.Context, string, ...any) (Rows, error) {
	s.once.Do(func() {
		close(s.started)
		<-s.release
	})
	return &fakeRows{}, nil
}

func (s *blockingStore) Close() {}

type errorStore struct {
	started chan struct{}
	once    sync.Once
}

func (s *errorStore) Query(context.Context, string, ...any) (Rows, error) {
	s.once.Do(func() { close(s.started) })
	return nil, errors.New("database unavailable")
}

func (s *errorStore) Close() {}

type recordingReporter struct {
	mu      sync.Mutex
	results []string
}

func (r *recordingReporter) ReportPoll(result string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.results = append(r.results, result)
}

func (r *recordingReporter) Results() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.results...)
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

type fakeStore struct {
	rows    map[string][][]any
	queries []string
	err     error
}

func (s *fakeStore) Query(_ context.Context, query string, _ ...any) (Rows, error) {
	s.queries = append(s.queries, query)
	if s.err != nil {
		return nil, s.err
	}
	return &fakeRows{values: s.rows[query]}, nil
}

func (s *fakeStore) Close() {}

type windowCheckingStore struct{ store *fakeStore }

func (s *windowCheckingStore) Query(ctx context.Context, query string, args ...any) (Rows, error) {
	if strings.Contains(query, " window") || strings.Contains(query, ".window") {
		return nil, errors.New("unquoted reserved word window")
	}
	return s.store.Query(ctx, query, args...)
}

func (s *windowCheckingStore) Close() { s.store.Close() }

type fakeRows struct {
	values [][]any
	index  int
}

func (r *fakeRows) Next() bool {
	if r.index >= len(r.values) {
		return false
	}
	r.index++
	return true
}

func (r *fakeRows) Scan(dest ...any) error {
	return assign(dest, r.values[r.index-1])
}

func (fakeRows) Err() error { return nil }
func (fakeRows) Close()     {}

func assign(dest, values []any) error {
	if len(dest) != len(values) {
		return errors.New("scan destination count does not match row")
	}
	for index, value := range values {
		switch destination := dest[index].(type) {
		case *string:
			*destination = value.(string)
		case *int:
			*destination = value.(int)
		case *bool:
			*destination = value.(bool)
		case **float64:
			if value != nil {
				*destination = value.(*float64)
			}
		case **time.Time:
			if value != nil {
				*destination = value.(*time.Time)
			}
		default:
			return errors.New("unsupported scan destination")
		}
	}
	return nil
}
