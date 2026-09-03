// Package drift runs the archive profile diff inside the long-running service.
package drift

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rknightion/codexlb2otel/internal/profile"
)

const (
	// SeverityBreaking is the metric label value for a breaking drift finding.
	SeverityBreaking = "breaking"
	// SeverityNew is the metric label value for a new, non-breaking drift finding.
	SeverityNew = "new"
	// SeverityInfo is the metric label value for an informational drift finding.
	SeverityInfo = "info"

	defaultInterval  = 24 * time.Hour
	defaultWindows   = 24
	defaultWindow    = 1 << 20
	defaultChunkSize = profile.DefaultChunk
)

// ErrNoInput means the configured archive path exists but no archive files matched.
var ErrNoInput = errors.New("drift: no .jsonl.gz files found")

// Counts is the metric-ready count of findings by severity.
type Counts struct {
	Breaking int64
	New      int64
	Info     int64
}

// MetricPoint is one value for codexlb.archive_drift_findings, labeled by
// codexlb.selfobs.severity at the OTLP registration layer.
type MetricPoint struct {
	Severity string
	Value    int64
}

// MetricPoints returns all three bounded severity series, including zero values, so
// the metrics layer never has to infer an absent series.
func (c Counts) MetricPoints() []MetricPoint {
	return []MetricPoint{
		{Severity: SeverityBreaking, Value: c.Breaking},
		{Severity: SeverityNew, Value: c.New},
		{Severity: SeverityInfo, Value: c.Info},
	}
}

// CountsFromFindings reduces raw profile findings to the bounded severity counts
// exported as self-observability metrics.
func CountsFromFindings(findings []profile.Finding) Counts {
	var counts Counts
	for _, f := range findings {
		switch f.Severity {
		case profile.SevBreaking:
			counts.Breaking++
		case profile.SevNew:
			counts.New++
		default:
			counts.Info++
		}
	}
	return counts
}

// Stats is the last completed probe run.
type Stats struct {
	Findings     Counts
	RawFindings  []profile.Finding
	LastCoverage profile.Coverage
	LastRun      time.Time
	LastError    string
}

// MetricPoints returns the current finding counts in metric label form.
func (s Stats) MetricPoints() []MetricPoint { return s.Findings.MetricPoints() }

// Scanner is the scan seam used by tests and by future wiring that embeds the
// baseline in a package this lane does not own.
type Scanner func(context.Context, ScanOptions) (*profile.Signature, profile.Coverage, error)

// ScanOptions is one probe pass over the archive directory.
type ScanOptions struct {
	ArchiveDir     string
	Sampled        bool
	Windows        int
	WindowBytes    int
	ChunkBytes     int
	MaxConcurrency int
}

// Config configures an in-process drift runner. BaselineBytes is the production
// path. Baseline exists for tests that need to inject an already-decoded signature.
type Config struct {
	ArchiveDir     string
	BaselineBytes  []byte
	Baseline       *profile.Signature
	Interval       time.Duration
	Sampled        bool
	Windows        int
	WindowBytes    int
	ChunkBytes     int
	MaxConcurrency int
	Scanner        Scanner
	Now            func() time.Time
}

// Runner owns the baseline and the last observed probe state.
type Runner struct {
	baseline *profile.Signature
	opts     ScanOptions
	interval time.Duration
	scanner  Scanner
	now      func() time.Time

	mu    sync.RWMutex
	stats Stats
}

// New validates cfg and prepares a runner. It does not start any goroutines.
func New(cfg Config) (*Runner, error) {
	baseline := cfg.Baseline
	if baseline == nil {
		if len(cfg.BaselineBytes) == 0 {
			return nil, fmt.Errorf("drift: baseline is required")
		}
		var decoded profile.Signature
		if err := json.Unmarshal(cfg.BaselineBytes, &decoded); err != nil {
			return nil, fmt.Errorf("drift: decode baseline: %w", err)
		}
		baseline = &decoded
	}
	if cfg.ArchiveDir == "" {
		return nil, fmt.Errorf("drift: archive dir is required")
	}

	interval := cfg.Interval
	if interval <= 0 {
		interval = defaultInterval
	}
	windows := cfg.Windows
	if windows <= 0 {
		windows = defaultWindows
	}
	windowBytes := cfg.WindowBytes
	if windowBytes <= 0 {
		windowBytes = defaultWindow
	}
	chunkBytes := cfg.ChunkBytes
	if chunkBytes <= 0 {
		chunkBytes = defaultChunkSize
	}
	concurrency := cfg.MaxConcurrency
	if concurrency <= 0 {
		concurrency = max(1, runtime.NumCPU()/2)
	}
	scanner := cfg.Scanner
	if scanner == nil {
		scanner = ScanArchive
	}
	now := cfg.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}

	return &Runner{
		baseline: baseline,
		opts: ScanOptions{
			ArchiveDir:     cfg.ArchiveDir,
			Sampled:        cfg.Sampled,
			Windows:        windows,
			WindowBytes:    windowBytes,
			ChunkBytes:     chunkBytes,
			MaxConcurrency: concurrency,
		},
		interval: interval,
		scanner:  scanner,
		now:      now,
	}, nil
}

// Start starts Run in a goroutine and returns the runner it observes.
func Start(ctx context.Context, cfg Config) (*Runner, error) {
	r, err := New(cfg)
	if err != nil {
		return nil, err
	}
	go func() { _ = r.Run(ctx) }()
	return r, nil
}

// Run performs one immediate probe and then repeats it every configured interval
// until ctx is cancelled. Scan failures are recorded in Stats and do not stop the
// schedule.
func (r *Runner) Run(ctx context.Context) error {
	if err := r.RunOnce(ctx); err != nil && ctx.Err() != nil {
		return nil
	}

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := r.RunOnce(ctx); err != nil && ctx.Err() != nil {
				return nil
			}
		}
	}
}

// RunOnce scans the archive path once, diffs the result against the baseline, and
// stores a fresh snapshot.
func (r *Runner) RunOnce(ctx context.Context) error {
	sig, cov, err := r.scanner(ctx, r.opts)
	now := r.now()

	r.mu.Lock()
	defer r.mu.Unlock()
	r.stats.LastRun = now
	if err != nil {
		r.stats.LastError = err.Error()
		return err
	}

	findings := profile.Diff(r.baseline, sig)
	r.stats.Findings = CountsFromFindings(findings)
	r.stats.RawFindings = append(r.stats.RawFindings[:0], findings...)
	r.stats.LastCoverage = cov
	r.stats.LastError = ""
	return nil
}

// Stats returns the last completed run snapshot.
func (r *Runner) Stats() Stats {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := r.stats
	out.RawFindings = append([]profile.Finding(nil), r.stats.RawFindings...)
	return out
}

// ScanArchive profiles every archive file under opts.ArchiveDir. It runs sampled
// scans when opts.Sampled is true and full scans otherwise.
func ScanArchive(ctx context.Context, opts ScanOptions) (*profile.Signature, profile.Coverage, error) {
	files, err := expand(opts.ArchiveDir)
	if err != nil {
		return nil, profile.Coverage{}, err
	}
	if len(files) == 0 {
		return nil, profile.Coverage{}, ErrNoInput
	}

	total := profile.New()
	var cov profile.Coverage
	var errs []error
	var mu sync.Mutex
	sem := make(chan struct{}, max(1, opts.MaxConcurrency))
	var wg sync.WaitGroup

	for _, path := range files {
		if err := ctx.Err(); err != nil {
			break
		}
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			p, c, err := scanFile(path, opts)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", filepath.Base(path), err))
				return
			}
			total.Merge(p)
			cov.Files += c.Files
			cov.FileBytes += c.FileBytes
			cov.ReadBytes += c.ReadBytes
			cov.Windows += c.Windows
			cov.Resynced += c.Resynced
			cov.DeadWindows += c.DeadWindows
			cov.Sampled = cov.Sampled || c.Sampled
		}(path)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, cov, err
	}
	if len(errs) > 0 {
		return nil, cov, errors.Join(errs...)
	}
	return total.Signature(cov), cov, nil
}

func scanFile(path string, opts ScanOptions) (*profile.Profile, profile.Coverage, error) {
	if opts.Sampled {
		return profile.ScanSampled(path, opts.Windows, opts.WindowBytes)
	}
	return profile.ScanFile(path, opts.ChunkBytes)
}

func expand(path string) ([]string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		if strings.HasSuffix(path, ".jsonl.gz") {
			return []string{path}, nil
		}
		return nil, nil
	}

	var out []string
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".jsonl.gz") {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}
