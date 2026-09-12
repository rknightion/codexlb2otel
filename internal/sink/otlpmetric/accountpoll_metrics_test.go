package otlpmetric

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/rknightion/codexlb2otel/internal/accountpoll"
	"github.com/rknightion/codexlb2otel/internal/attr"
)

func TestReportPoll_RecordsOnlyBoundedResult(t *testing.T) {
	s, reader, _ := newTestSink(t)
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	for _, result := range []string{"success", "error", "disabled"} {
		s.ReportPoll(result)
	}
	s.ReportPoll("unexpected")

	rm := collect(t, reader)
	m, ok := findMetric(rm, attr.MetricSelfAccountPolls)
	if !ok {
		t.Fatalf("%s not recorded", attr.MetricSelfAccountPolls)
	}
	if m.Description != "Account poller attempts by bounded result." {
		t.Errorf("description = %q", m.Description)
	}
	if m.Unit != "{poll}" {
		t.Errorf("unit = %q", m.Unit)
	}
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("%s: data type = %T, want int64 sum", attr.MetricSelfAccountPolls, m.Data)
	}
	if len(sum.DataPoints) != 3 {
		t.Fatalf("%s: data points = %d, want 3", attr.MetricSelfAccountPolls, len(sum.DataPoints))
	}
	seen := map[string]int64{}
	for _, dp := range sum.DataPoints {
		if len(dp.Attributes.ToSlice()) != 1 {
			t.Errorf("attributes = %v, want only %s", dp.Attributes.ToSlice(), attr.SelfObsResult)
		}
		value, ok := dp.Attributes.Value(attribute.Key(attr.SelfObsResult))
		if !ok {
			t.Errorf("attributes = %v, missing %s", dp.Attributes.ToSlice(), attr.SelfObsResult)
			continue
		}
		seen[value.AsString()] = dp.Value
	}
	for _, result := range []string{"success", "error", "disabled"} {
		if seen[result] != 1 {
			t.Errorf("result %q count = %d, want 1", result, seen[result])
		}
	}
}

// TestRegisterAccountPoller_CollectsPublishedSnapshotWhilePollQueryBlocks is a
// regression for the re-entrant observable-callback deadlock. The second Poll is
// deliberately stopped inside Store.Query, where Poll holds pollMu. ManualReader's
// real SDK collection callback must still read the snapshot published by the first
// Poll promptly; a callback that calls Poll or takes pollMu instead either blocks or
// observes no prior account state.
func TestRegisterAccountPoller_CollectsPublishedSnapshotWhilePollQueryBlocks(t *testing.T) {
	s, reader, _ := newTestSink(t)
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	store := newBlockingAccountPollStore()
	poller, err := accountpoll.NewWithStore(store, accountpoll.Options{
		Interval:     time.Hour,
		QueryTimeout: time.Second,
		Now:          func() time.Time { return time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("NewWithStore: %v", err)
	}
	t.Cleanup(poller.Close)
	if err := poller.Poll(context.Background()); err != nil {
		t.Fatalf("first Poll: %v", err)
	}
	if err := s.RegisterAccountPoller(poller); err != nil {
		t.Fatalf("RegisterAccountPoller: %v", err)
	}

	store.blockNextPoll()
	pollDone := make(chan error, 1)
	go func() { pollDone <- poller.Poll(context.Background()) }()
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("second Poll did not reach its deliberately blocking query")
	}
	t.Cleanup(store.release)

	type collectResult struct {
		rm  metricdata.ResourceMetrics
		err error
	}
	collected := make(chan collectResult, 1)
	go func() {
		var rm metricdata.ResourceMetrics
		collected <- collectResult{rm: rm, err: reader.Collect(context.Background(), &rm)}
	}()

	var rm metricdata.ResourceMetrics
	select {
	case got := <-collected:
		if got.err != nil {
			t.Fatalf("ManualReader.Collect: %v", got.err)
		}
		rm = got.rm
	case <-time.After(250 * time.Millisecond):
		t.Fatal("ManualReader.Collect blocked while Poll held pollMu")
	}

	info, ok := findMetric(rm, attr.MetricAccountInfo)
	if !ok {
		t.Fatalf("%s not observed from the previously published snapshot", attr.MetricAccountInfo)
	}
	gauge, ok := info.Data.(metricdata.Gauge[float64])
	if !ok || len(gauge.DataPoints) != 1 {
		t.Fatalf("%s = %T %+v, want one prior-snapshot gauge", attr.MetricAccountInfo, info.Data, gauge.DataPoints)
	}
	if accountID, ok := gauge.DataPoints[0].Attributes.Value(attribute.Key(attr.AccountID)); !ok || accountID.AsString() != "acct-previous" {
		t.Fatalf("%s account id = %v (ok=%v), want prior published account", attr.MetricAccountInfo, accountID, ok)
	}

	store.release()
	select {
	case err := <-pollDone:
		if err != nil {
			t.Fatalf("second Poll: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("second Poll did not finish after its query was released")
	}
}

type blockingAccountPollStore struct {
	mu          sync.Mutex
	queries     int
	block       bool
	started     chan struct{}
	releaseCh   chan struct{}
	startOnce   sync.Once
	releaseOnce sync.Once
}

func newBlockingAccountPollStore() *blockingAccountPollStore {
	return &blockingAccountPollStore{started: make(chan struct{}), releaseCh: make(chan struct{})}
}

func (s *blockingAccountPollStore) blockNextPoll() {
	s.mu.Lock()
	s.block = true
	s.mu.Unlock()
}

func (s *blockingAccountPollStore) Query(_ context.Context, _ string, _ ...any) (accountpoll.Rows, error) {
	s.mu.Lock()
	s.queries++
	query := s.queries
	block := s.block && query == 5
	s.mu.Unlock()
	if block {
		s.startOnce.Do(func() { close(s.started) })
		<-s.releaseCh
	}
	if query == 1 {
		return &accountPollRows{values: [][]any{{"acct-previous", "account@example.invalid", "active", "pro", "round_robin"}}}, nil
	}
	return &accountPollRows{}, nil
}

func (s *blockingAccountPollStore) Close() {}

func (s *blockingAccountPollStore) release() {
	s.releaseOnce.Do(func() { close(s.releaseCh) })
}

type accountPollRows struct {
	values [][]any
	index  int
}

func (r *accountPollRows) Next() bool {
	if r.index >= len(r.values) {
		return false
	}
	r.index++
	return true
}

func (r *accountPollRows) Scan(dest ...any) error {
	if r.index == 0 || r.index > len(r.values) {
		return fmt.Errorf("Scan called without a row")
	}
	for i, value := range r.values[r.index-1] {
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("row value %d is %T, want string", i, value)
		}
		out, ok := dest[i].(*string)
		if !ok {
			return fmt.Errorf("scan destination %d is %T, want *string", i, dest[i])
		}
		*out = text
	}
	return nil
}

func (r *accountPollRows) Err() error { return nil }
func (r *accountPollRows) Close()     {}
