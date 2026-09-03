package drift

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/rknightion/codexlb2otel/internal/profile"
)

func TestCountsFromFindingsReportsEverySeverity(t *testing.T) {
	findings := []profile.Finding{
		{Severity: profile.SevBreaking},
		{Severity: profile.SevNew},
		{Severity: profile.SevNew},
		{Severity: profile.SevInfo},
	}

	counts := CountsFromFindings(findings)
	if counts.Breaking != 1 || counts.New != 2 || counts.Info != 1 {
		t.Fatalf("CountsFromFindings = %+v, want 1 breaking, 2 new, 1 info", counts)
	}

	points := counts.MetricPoints()
	if len(points) != 3 {
		t.Fatalf("MetricPoints returned %d points, want all three severities", len(points))
	}
	for _, sev := range []string{SeverityBreaking, SeverityNew, SeverityInfo} {
		if !slices.ContainsFunc(points, func(p MetricPoint) bool { return p.Severity == sev }) {
			t.Errorf("MetricPoints did not include severity %q", sev)
		}
	}
}

func TestRunOnceInjectedProtocolChangeIsBreaking(t *testing.T) {
	now := time.Date(2026, 9, 3, 18, 0, 0, 0, time.UTC)
	baseline := signatureBytes(t, stableArchiveRecord(`{"type":"response.completed","response":{"safety_buffering":{"enabled":true}}}`))

	dir := t.TempDir()
	writeArchive(t, filepath.Join(dir, "2026-09-03T18.jsonl.gz"),
		stableArchiveRecord(`{"type":"response.completed","response":{"safety_buffering":"buffered"}}`))

	r, err := New(Config{
		ArchiveDir:     dir,
		BaselineBytes:  baseline,
		Interval:       time.Hour,
		Sampled:        false,
		Now:            func() time.Time { return now },
		MaxConcurrency: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	stats := r.Stats()
	if stats.LastRun != now {
		t.Fatalf("LastRun = %s, want %s", stats.LastRun, now)
	}
	if stats.LastError != "" {
		t.Fatalf("LastError = %q, want empty", stats.LastError)
	}
	if stats.Findings.Breaking == 0 {
		t.Fatalf("Breaking findings = 0, want the fixture-injected type change to be caught")
	}
	if !slices.ContainsFunc(stats.RawFindings, func(f profile.Finding) bool {
		return f.Kind == "event.path.type" && f.Subject == "response.completed response.safety_buffering"
	}) {
		t.Fatalf("findings did not include response.safety_buffering type drift: %+v", stats.RawFindings)
	}
}

func TestRunRepeatsScanOnInterval(t *testing.T) {
	baseline := signatureBytes(t, stableArchiveRecord(`{"type":"response.completed","response":{"status":"completed"}}`))
	calls := make(chan ScanOptions, 3)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r, err := New(Config{
		ArchiveDir:    t.TempDir(),
		BaselineBytes: baseline,
		Interval:      10 * time.Millisecond,
		Sampled:       true,
		Scanner: func(ctx context.Context, opts ScanOptions) (*profile.Signature, profile.Coverage, error) {
			select {
			case calls <- opts:
			default:
			}
			return decodeSignature(t, baseline), profile.Coverage{Files: 1, Sampled: opts.Sampled}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	var first, second ScanOptions
	select {
	case first = <-calls:
	case <-time.After(time.Second):
		t.Fatal("first scan did not run")
	}
	select {
	case second = <-calls:
	case <-time.After(time.Second):
		t.Fatal("second scan did not run after interval")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v after cancellation, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return after cancellation")
	}
	if !first.Sampled || !second.Sampled {
		t.Fatalf("scheduled scans did not preserve sampled=true: first=%+v second=%+v", first, second)
	}
}

func TestStartWaitsForCancellation(t *testing.T) {
	baseline := signatureBytes(t, stableArchiveRecord(`{"type":"response.completed"}`))
	ctx, cancel := context.WithCancel(context.Background())
	r, err := Start(ctx, Config{
		ArchiveDir:    t.TempDir(),
		BaselineBytes: baseline,
		Scanner: func(context.Context, ScanOptions) (*profile.Signature, profile.Coverage, error) {
			return decodeSignature(t, baseline), profile.Coverage{Files: 1}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	waitCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := r.Wait(waitCtx); err != nil {
		t.Fatalf("Wait after cancellation: %v", err)
	}
}

func TestRunOnceRecordsLastErrorAndKeepsLastGoodFindings(t *testing.T) {
	firstRun := time.Date(2026, 9, 3, 18, 0, 0, 0, time.UTC)
	secondRun := firstRun.Add(time.Hour)
	baseline := signatureBytes(t, stableArchiveRecord(`{"type":"response.completed","response":{"safety_buffering":{"enabled":true}}}`))
	changed := decodeSignature(t, signatureBytes(t,
		stableArchiveRecord(`{"type":"response.completed","response":{"safety_buffering":"buffered"}}`)))
	scanErr := errors.New("archive unavailable")
	run := 0

	r, err := New(Config{
		ArchiveDir:    t.TempDir(),
		BaselineBytes: baseline,
		Interval:      time.Hour,
		Now: func() time.Time {
			if run < 2 {
				return firstRun
			}
			return secondRun
		},
		Scanner: func(ctx context.Context, opts ScanOptions) (*profile.Signature, profile.Coverage, error) {
			run++
			if run == 1 {
				return changed, profile.Coverage{Files: 1}, nil
			}
			return nil, profile.Coverage{}, scanErr
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("first RunOnce: %v", err)
	}
	first := r.Stats()
	if first.Findings.Breaking == 0 {
		t.Fatalf("first RunOnce produced no breaking finding")
	}
	if err := r.RunOnce(context.Background()); !errors.Is(err, scanErr) {
		t.Fatalf("second RunOnce error = %v, want %v", err, scanErr)
	}
	second := r.Stats()
	if second.LastRun != secondRun {
		t.Fatalf("LastRun = %s, want %s", second.LastRun, secondRun)
	}
	if second.LastError != scanErr.Error() {
		t.Fatalf("LastError = %q, want %q", second.LastError, scanErr.Error())
	}
	if second.Findings != first.Findings {
		t.Fatalf("findings were discarded on scan error: before=%+v after=%+v", first.Findings, second.Findings)
	}
}

func signatureBytes(t *testing.T, lines ...string) []byte {
	t.Helper()

	p := profile.New()
	fp := profile.FileProfile{Name: "baseline.jsonl.gz", Members: int64(len(lines))}
	for _, line := range lines {
		p.AddLine([]byte(line), &fp)
	}
	p.Files = []profile.FileProfile{fp}
	p.Members = fp.Members
	sig := p.Signature(profile.Coverage{Files: 1, ReadBytes: int64(len(lines)), FileBytes: int64(len(lines))})
	b, err := json.Marshal(sig)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func decodeSignature(t *testing.T, b []byte) *profile.Signature {
	t.Helper()

	var sig profile.Signature
	if err := json.Unmarshal(b, &sig); err != nil {
		t.Fatal(err)
	}
	return &sig
}

func stableArchiveRecord(event string) string {
	payload, err := json.Marshal(event)
	if err != nil {
		panic(err)
	}
	return `{"kind":"responses","direction":"server_to_codex","transport":"websocket","request_id":"ws_12345678","payload":{"text":` + string(payload) + `}}`
}

func writeArchive(t *testing.T, path string, lines ...string) {
	t.Helper()

	var out bytes.Buffer
	for _, line := range lines {
		zw := gzip.NewWriter(&out)
		if _, err := zw.Write([]byte(line + "\n")); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}
