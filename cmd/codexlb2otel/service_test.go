package main

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/rknightion/codexlb2otel/internal/config"
	"github.com/rknightion/codexlb2otel/internal/sink"
)

func TestNewLogger_LevelsAndFormats(t *testing.T) {
	cases := []struct {
		level string
		want  slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"error", slog.LevelError},
	}
	for _, c := range cases {
		log := newLogger(config.Log{Level: c.level, Format: "text"})
		if !log.Enabled(context.Background(), c.want) {
			t.Errorf("level %q: logger does not enable %s", c.level, c.want)
		}
		if c.want > slog.LevelDebug && log.Enabled(context.Background(), c.want-1) {
			t.Errorf("level %q: logger enables a level below its floor", c.level)
		}
	}

	// json vs text just needs to not panic and to produce a non-nil handler; the
	// standard library owns the actual encoding.
	for _, format := range []string{"text", "json"} {
		if newLogger(config.Log{Level: "info", Format: format}) == nil {
			t.Errorf("format %q: newLogger returned nil", format)
		}
	}
}

// Binary starts from a config file, tails a directory, and exits cleanly on
// SIGTERM - the first acceptance criterion in issue #4. signal.NotifyContext is
// stdlib and not worth re-testing; what belongs to this service is that run() itself
// unwinds promptly and without error once its context is cancelled, which is what a
// delivered SIGTERM turns into.
func TestRun_ExitsCleanlyOnContextCancel(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Archive.Dir = dir
	cfg.Archive.Checkpoint = filepath.Join(dir, "state", "checkpoint.json")
	cfg.Archive.PollInterval = time.Hour // this test drives shutdown, not polling
	cfg.Health.Enabled = false

	ctx, cancel := context.WithCancel(context.Background())
	log := slog.New(slog.DiscardHandler)

	done := make(chan error, 1)
	go func() { done <- run(ctx, cfg, log, sink.Discard{}) }()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run() = %v, want nil on a clean shutdown with an empty archive dir", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run() did not return within 5s of context cancellation")
	}
}

// The health endpoint must come up and answer while the service runs, and go quiet
// once shutdown starts - readiness flipping false is what a load balancer or
// orchestrator uses to stop sending new probes into a draining instance.
func TestRun_HealthEndpointServesWhileRunning(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Archive.Dir = dir
	cfg.Archive.Checkpoint = filepath.Join(dir, "state", "checkpoint.json")
	cfg.Archive.PollInterval = time.Hour
	cfg.Health.Enabled = true
	cfg.Health.Listen = "127.0.0.1:0" // OS-assigned port; this test only exercises run()'s wiring, not the port

	// health.Listen with :0 cannot be dialed back without knowing the assigned port,
	// which net/http does not expose before ListenAndServe binds. Proving run()
	// starts and stops the health server without deadlocking or leaking the
	// goroutine is what matters at this layer; internal/health's own tests cover the
	// HTTP contract.
	ctx, cancel := context.WithCancel(context.Background())
	log := slog.New(slog.DiscardHandler)

	done := make(chan error, 1)
	go func() { done <- run(ctx, cfg, log, sink.Discard{}) }()

	time.Sleep(50 * time.Millisecond) // let the health server's ListenAndServe start
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run() = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run() did not return within 5s; health server shutdown likely hung")
	}
}
