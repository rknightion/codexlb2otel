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
	"time"

	"github.com/rknightion/codexlb2otel/internal/config"
	"github.com/rknightion/codexlb2otel/internal/drift"
	"github.com/rknightion/codexlb2otel/internal/enrich"
	"github.com/rknightion/codexlb2otel/internal/health"
	"github.com/rknightion/codexlb2otel/internal/live"
	"github.com/rknightion/codexlb2otel/internal/profile"
	"github.com/rknightion/codexlb2otel/internal/selfobs"
	"github.com/rknightion/codexlb2otel/internal/sink"
	"github.com/rknightion/codexlb2otel/internal/sink/otlpmetric"
	"github.com/rknightion/codexlb2otel/internal/tail"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

func main() {
	cfgPath := flag.String("config", "/etc/codexlb2otel/config.yaml", "path to the config file")
	healthcheck := flag.Bool("healthcheck", false,
		"probe the health endpoint of an already-running instance and exit 0 if ready; for a container HEALTHCHECK")
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

	// -healthcheck exists so the container image needs no HTTP client of its own.
	//
	// The alternative was copying busybox's wget applet into the distroless final
	// layer, and that quietly defeats the "no shell in the final layer" requirement:
	// busybox is one multi-call binary that dispatches on argv[0], so anything able
	// to exec it can ask for the sh applet no matter what the file is named.
	// Shipping the probe inside the binary that is already there costs nothing and
	// leaves the image with genuinely no shell to reach.
	//
	// It reads the same config the server does, so it probes whatever health.listen
	// is actually configured rather than a port baked into the Dockerfile at build
	// time.
	if *healthcheck {
		if err := health.Probe(cfg.Health); err != nil {
			fmt.Fprintf(os.Stderr, "codexlb2otel: healthcheck: %v\n", err)
			os.Exit(1)
		}
		return
	}

	log := newLogger(cfg.Log)

	// The signal context is established BEFORE the sinks, because the OTLP sinks open
	// exporters against it: built the other way round, a SIGTERM arriving during
	// startup would leave those exporters running on a background context nothing ever
	// cancels.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	snk, _, metricsSink, liveStore, err := buildSinks(ctx, cfg, log)
	if err != nil {
		log.Error("build sinks", "err", err)
		os.Exit(1)
	}

	if err := run(ctx, cfg, log, snk, metricsSink, liveStore); err != nil && !errors.Is(err, context.Canceled) {
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
//
// metricsSink is nil whenever OTLP metrics are disabled (buildSinks' own doc
// comment); self-observability (issue #8) is then simply not wired up, since there is
// no metrics pipeline for it to share.
// liveStore is nil whenever the live view is disabled, in which case no second
// listener is started and nothing observes the watcher's in-flight snapshot.
func run(ctx context.Context, cfg config.Config, log *slog.Logger, snk sink.Sink, metricsSink *otlpmetric.Sink, liveStore *live.Store) error {
	enricher := buildEnricher(ctx, cfg.Postgres, log)
	defer enricher.Close()

	probeCtx, stopProbe := context.WithCancel(ctx)
	probe := buildDriftProbe(probeCtx, cfg, log)
	defer stopProbe()

	reducer := turn.New()
	w, err := tail.New(tail.Config{
		Dir:                cfg.Archive.Dir,
		CheckpointPath:     cfg.Archive.Checkpoint,
		ChunkSize:          cfg.Archive.ChunkBytes,
		PollInterval:       cfg.Archive.PollInterval,
		CheckpointInterval: cfg.Archive.CheckpointInterval,
		DeleteAfter:        cfg.Archive.DeleteAfter,
		RetainDays:         cfg.Archive.RetainDays,
		StateRetain:        cfg.Archive.StateRetain,
		Logger:             log,
	}, reducer)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}

	// Self-observability (issue #8) can only be wired up here, not in buildSinks: it
	// needs w, which does not exist until this line. See buildSinks' own doc comment
	// on why the concrete *otlpmetric.Sink is threaded all the way from there to here
	// instead of being reconstructed by walking snk.
	if metricsSink != nil {
		collector := selfobs.New(w, snk)
		collector.SetEnrichmentSource(func() int64 {
			return int64(enricher.Stats().CacheEntries)
		})
		if probe != nil {
			collector.SetDriftSource(func() (int64, int64, int64) {
				counts := probe.Stats().Findings
				return counts.Breaking, counts.New, counts.Info
			})
		}
		if err := metricsSink.RegisterSelfObs(collector.Collect); err != nil {
			return fmt.Errorf("register self-observability: %w", err)
		}
	}

	var hsrv *health.Server
	healthDone := make(chan error, 1)
	if cfg.Health.Enabled {
		hsrv = health.New(cfg, log)
		go func() { healthDone <- hsrv.Run(ctx) }()
	}

	liveDone := make(chan error, 1)
	if liveStore != nil {
		// The in-flight source is wired here for the same reason self-observability is:
		// it needs w, which did not exist when buildSinks ran. Watcher.InFlight takes no
		// lock (see its doc comment and issue #29), so an HTTP handler calling it cannot
		// contend with, let alone deadlock against, the poll goroutine.
		liveStore.SetInFlightSource(w.InFlight)
		// Resolve rather than dereference: the token may be an ${ENV} or file: reference,
		// and Validate has already proved it resolves. Empty is skipped rather than
		// resolved - the token is optional here, and Resolve rightly rejects an unset
		// value for a credential that is required.
		var token string
		if cfg.Live.Token != "" {
			var err error
			if token, err = cfg.Live.Token.Resolve(); err != nil {
				return fmt.Errorf("live.token: %w", err)
			}
		}
		lsrv := live.NewServer(liveStore, live.ServerConfig{
			Listen:       cfg.Live.Listen,
			Token:        token,
			PollInterval: cfg.Archive.PollInterval,
		}, log)
		go func() { liveDone <- lsrv.Run(ctx) }()
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
	runErr := w.Run(ctx, enrichingEmit(enricher, metricsSink, snk))
	stopProbe()
	var probeErr error
	if probe != nil {
		waitCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		probeErr = probe.Wait(waitCtx)
		cancel()
	}

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
	reportRejections(log, snk)
	closeErr := snk.Close(context.WithoutCancel(ctx))

	var healthErr error
	if hsrv != nil {
		healthErr = <-healthDone
	}
	var liveErr error
	if liveStore != nil {
		liveErr = <-liveDone
	}

	return errors.Join(runErr, probeErr, flushErr, closeErr, healthErr, liveErr)
}

func buildEnricher(ctx context.Context, cfg config.Postgres, log *slog.Logger) enrich.Enricher {
	if !cfg.Enabled {
		return enrich.Disabled
	}
	if cfg.LookupTimeout <= 0 || cfg.PrefetchInterval <= 0 || cfg.CacheEntries <= 0 {
		log.Warn("postgres enrichment disabled: invalid bounds",
			"lookup_timeout", cfg.LookupTimeout,
			"prefetch_interval", cfg.PrefetchInterval,
			"cache_entries", cfg.CacheEntries)
		return enrich.Disabled
	}
	dsn, err := cfg.DSN.Resolve()
	if err != nil {
		log.Warn("postgres enrichment disabled: dsn unavailable", "err", err)
		return enrich.Disabled
	}
	e, err := enrich.NewPostgres(ctx, dsn, enrich.Options{
		LookupTimeout:    cfg.LookupTimeout,
		PrefetchInterval: cfg.PrefetchInterval,
		CacheEntries:     cfg.CacheEntries,
	})
	if err != nil {
		log.Warn("postgres enrichment disabled: pool unavailable", "err", err)
		return enrich.Disabled
	}
	log.Info("postgres enrichment enabled",
		"lookup_timeout", cfg.LookupTimeout,
		"prefetch_interval", cfg.PrefetchInterval,
		"cache_entries", cfg.CacheEntries)
	return e
}

func buildDriftProbe(ctx context.Context, cfg config.Config, log *slog.Logger) *drift.Runner {
	if !cfg.Probe.Enabled {
		return nil
	}
	runner, err := drift.Start(ctx, drift.Config{
		ArchiveDir:    cfg.Archive.Dir,
		BaselineBytes: profile.EmbeddedBaseline(),
		Interval:      cfg.Probe.Interval,
		Sampled:       cfg.Probe.Sampled,
	})
	if err != nil {
		log.Warn("archive drift probe disabled", "err", err)
		return nil
	}
	log.Info("archive drift probe enabled", "interval", cfg.Probe.Interval, "sampled", cfg.Probe.Sampled)
	return runner
}

func enrichingEmit(enricher enrich.Enricher, metrics *otlpmetric.Sink, downstream sink.Sink) tail.Emit {
	emit := sinkEmit(downstream)
	return func(ctx context.Context, turns []*turn.Turn) error {
		for _, t := range turns {
			result := enricher.Enrich(ctx, t)
			if metrics != nil {
				metrics.RecordEnrichment(ctx, result)
			}
		}
		return emit(ctx, turns)
	}
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

// reportRejections logs what each sink refused to deliver, by reason.
//
// This is the signal whose absence let a whole run vanish: the first live deployment
// consumed an archive, wrote a clean checkpoint, exited zero, and delivered nothing -
// because every line was rejected for a reason nothing printed. A count that exists
// only inside a sink's struct is not observability.
//
// Non-zero counts log at WARN. A rejection is never routine: every reason on
// sink.Reason* means a line that will never exist in Loki.
func reportRejections(log *slog.Logger, s sink.Sink) {
	var walk func(sink.Sink)
	walk = func(x sink.Sink) {
		if m, ok := x.(sink.Multi); ok {
			for _, inner := range m {
				walk(inner)
			}
			return
		}
		r, ok := x.(sink.Reporter)
		if !ok {
			return
		}
		for _, rej := range r.Rejections() {
			if rej.Count > 0 {
				log.Warn("lines rejected", "sink", x.Name(), "reason", rej.Reason, "count", rej.Count)
			}
		}
		if n := r.Pending(); n > 0 {
			log.Warn("undelivered at exit", "sink", x.Name(), "pending", n)
		}
	}
	walk(s)
}
