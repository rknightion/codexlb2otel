// Command codexlb2otel tails codex-lb's conversation archive and ships reduced turns
// to Loki and to an OTLP endpoint for metrics and traces.
//
// It is the only long-running piece of this repo; everything else (clbstat, clbfind,
// clbprofile, clbsync) is an offline tool run against a directory of archive files.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/rknightion/codexlb2otel/internal/config"
	"github.com/rknightion/codexlb2otel/internal/health"
	"github.com/rknightion/codexlb2otel/internal/sink"
	"github.com/rknightion/codexlb2otel/internal/tail"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

func main() {
	cfgPath := flag.String("config", "/etc/codexlb2otel/config.yaml", "path to the config file")
	flag.Parse()

	// Config errors, including an invalid config, are reported before any logger
	// exists, so the message goes straight to stderr rather than through slog - a
	// misconfigured log.format is exactly one of the things Validate can be
	// complaining about.
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "codexlb2otel: %v\n", err)
		os.Exit(1)
	}

	log := newLogger(cfg.Log)

	// The signal context is established BEFORE the sinks, because the OTLP sinks open
	// exporters against it: built the other way round, a SIGTERM arriving during
	// startup would leave those exporters running on a background context nothing ever
	// cancels.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	snk, _, err := buildSinks(ctx, cfg, log)
	if err != nil {
		log.Error("build sinks", "err", err)
		os.Exit(1)
	}

	if err := run(ctx, cfg, log, snk); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("exited with error", "err", err)
		os.Exit(1)
	}
}

// newLogger builds the service's logger from cfg.Log. Level and format are the only
// two knobs, and config.Validate has already rejected anything but the four levels
// and two formats it checks - the switch below cannot see an unrecognised value by
// the time this runs.
func newLogger(cfg config.Log) *slog.Logger {
	level := slog.LevelInfo
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if cfg.Format == "json" {
		h = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		h = slog.NewTextHandler(os.Stdout, opts)
	}
	return slog.New(h)
}

// run wires the tail watcher to snk and blocks until ctx is cancelled, then shuts
// down in the order the issue requires: stop polling, flush, and only THEN let the
// checkpoint be treated as durable.
//
// internal/tail already withholds its checkpoint save whenever the emit callback it
// was given returns an error - proven by TestWatcher_FailedEmitDoesNotAdvanceCheckpoint
// - and it calls that save after EVERY successful poll, not only at shutdown. That
// matters here: the sink interface lets Emit return nil for "accepted OR safely
// buffered for a later Flush", which would let the watcher checkpoint a batch the
// sink has not actually delivered yet. sinkEmit closes that gap by calling Flush
// immediately after every Emit, so the callback tail.Watcher relies on only ever
// reports success once the batch has truly left the building - see sinkEmit's own
// comment for the tradeoff that buys.
func run(ctx context.Context, cfg config.Config, log *slog.Logger, snk sink.Sink) error {
	reducer := turn.New()
	w, err := tail.New(tail.Config{
		Dir:            cfg.Archive.Dir,
		CheckpointPath: cfg.Archive.Checkpoint,
		ChunkSize:      cfg.Archive.ChunkBytes,
		PollInterval:   cfg.Archive.PollInterval,
		DeleteAfter:    cfg.Archive.DeleteAfter,
		Logger:         log,
	}, reducer)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}

	var hsrv *health.Server
	healthDone := make(chan error, 1)
	if cfg.Health.Enabled {
		hsrv = health.New(cfg, log)
		go func() { healthDone <- hsrv.Run(ctx) }()
	}

	log.Info("starting",
		"archive_dir", cfg.Archive.Dir,
		"checkpoint", cfg.Archive.Checkpoint,
		"poll_interval", cfg.Archive.PollInterval)
	if hsrv != nil {
		hsrv.SetReady(true)
	}

	// Watcher.Run blocks until ctx is cancelled (SIGINT/SIGTERM), at which point it
	// stops polling on its own and performs its own final flush-then-checkpoint of
	// whatever the reducer was still holding in-flight.
	runErr := w.Run(ctx, sinkEmit(snk))

	if hsrv != nil {
		hsrv.SetReady(false)
	}
	log.Info("stopped polling; draining sink", "err", runErr)

	// A safety-net flush, independent of the ctx that just got cancelled: sinkEmit
	// already flushes after every batch, so in the normal case this has nothing left
	// to do. It exists for whatever the sink is holding outside that path (its own
	// background retry queue, say). Close does not flush by design - a Close that
	// silently flushed would hide a failed flush behind what looks like a clean exit -
	// so this must run first regardless of runErr.
	flushErr := snk.Flush(context.WithoutCancel(ctx))
	closeErr := snk.Close(context.WithoutCancel(ctx))

	var healthErr error
	if hsrv != nil {
		healthErr = <-healthDone
	}

	return errors.Join(runErr, flushErr, closeErr, healthErr)
}

// sinkEmit adapts a Sink into the tail.Emit function Watcher.Run/Poll call.
//
// It calls Flush immediately after every Emit rather than only at shutdown. Sink's
// own contract allows Emit to return nil for "durably accepted OR safely buffered for
// a later Flush" - but Watcher persists the checkpoint right after every successful
// emit, on every poll, not only at shutdown (see internal/tail's Poll). If Emit's
// "buffered" promise were taken at face value here, a batch could be checkpointed as
// delivered and then genuinely lost if a later, decoupled flush failed - which is
// exactly the failure the issue calls out. Folding Flush into every Emit call closes
// that window: this callback only reports success once the batch has actually left
// the building, so the checkpoint watcher persists is never ahead of what has really
// been sent. The cost is more frequent network pushes than a sink's own
// BatchSize/BatchWait would otherwise allow between polls; that is a deliberate
// tradeoff of throughput for the correctness the issue prioritises, not an oversight.
func sinkEmit(s sink.Sink) tail.Emit {
	return func(ctx context.Context, turns []*turn.Turn) error {
		if err := s.Emit(ctx, turns); err != nil {
			return fmt.Errorf("emit: %w", err)
		}
		if err := s.Flush(ctx); err != nil {
			return fmt.Errorf("flush: %w", err)
		}
		return nil
	}
}
