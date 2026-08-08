package main

import (
	"context"
	"log/slog"

	"github.com/rknightion/codexlb2otel/internal/attr"
	"github.com/rknightion/codexlb2otel/internal/config"
	"github.com/rknightion/codexlb2otel/internal/live"
	"github.com/rknightion/codexlb2otel/internal/sink"
	"github.com/rknightion/codexlb2otel/internal/sink/agento11y"
	"github.com/rknightion/codexlb2otel/internal/sink/loki"
	"github.com/rknightion/codexlb2otel/internal/sink/otlpmetric"
	"github.com/rknightion/codexlb2otel/internal/sink/otlptrace"
)

// buildSinks wires one sink.Multi entry per enabled destination.
//
// ONE attr.Guard is shared by all three. That is not tidiness - the guard's caps are
// per field across the PROCESS, so three guards would each independently admit 32
// models and the cardinality ceiling would silently become three times what the
// contract states. It also means the rejection counter the metrics sink exports
// accounts for every sink's attributes, not just its own.
//
// The *otlpmetric.Sink return (nil when metrics are disabled) is separate from the
// sink.Sink tree above it purely so main.go's run() can wire self-observability
// (issue #8) onto it later: RegisterSelfObs needs the tail.Watcher, which does not
// exist yet at this point in startup (built inside run(), after buildSinks returns) -
// see run()'s own comment for the ordering. A type assertion back through sink.Multi
// would work too, but returning the concrete pointer here is a straight line instead
// of a tree walk to undo one this function itself just built.
func buildSinks(ctx context.Context, cfg config.Config, log *slog.Logger) (sink.Sink, *attr.Guard, *otlpmetric.Sink, *live.Store, error) {
	guard := attr.NewGuard()
	var sinks sink.Multi
	var metricsSink *otlpmetric.Sink
	var liveStore *live.Store

	if cfg.Loki.Enabled {
		s, err := loki.New(cfg.Loki, guard, cfg.Service.Name)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		log.Info("loki sink enabled", "url", cfg.Loki.URL, "labels", cfg.Loki.Labels,
			"max_line_bytes", cfg.Loki.MaxLineBytes)
		sinks = append(sinks, s)
	}
	if cfg.OTLP.Metrics.Enabled {
		s, err := otlpmetric.New(ctx, cfg.OTLP, cfg.Service, guard)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		log.Info("otlp metrics sink enabled", "endpoint", cfg.OTLP.Endpoint,
			"interval", cfg.OTLP.Metrics.Interval)
		sinks = append(sinks, s)
		metricsSink = s
	}
	if cfg.OTLP.Traces.Enabled {
		s, err := otlptrace.New(ctx, cfg.OTLP, cfg.Service, guard)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		log.Info("otlp traces sink enabled", "endpoint", cfg.OTLP.Endpoint,
			"sample_ratio", cfg.OTLP.Traces.SampleRatio)
		sinks = append(sinks, s)
	}
	if cfg.AgentO11y.Enabled {
		// Additive, not a replacement for OTLP.Traces above: sigil's own product
		// surface (conversations, generations, agent catalog) is populated only by
		// this ExportGenerations wire contract, and the OTLP trace path to Tempo
		// keeps running unchanged alongside it. See config.AgentO11y's doc comment.
		s, err := agento11y.New(cfg.AgentO11y, guard)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		log.Info("agento11y sink enabled", "url", cfg.AgentO11y.URL)
		sinks = append(sinks, s)
	}

	if cfg.Live.Enabled {
		// Deliberately NOT behind the shared attr.Guard the other sinks share. The guard
		// caps distinct values per field to protect metric cardinality; this sink emits
		// no metrics and a capped view would simply stop showing new models and threads
		// once the ceiling was reached - the exact opposite of what a live view is for.
		liveStore = live.New(live.Options{
			RetainTurns:  cfg.Live.RetainTurns,
			RetainWindow: cfg.Live.RetainWindow,
			Content:      cfg.Live.Content,
			IncludeProbe: cfg.Live.IncludeProbe,
		})
		log.Info("live view enabled", "listen", cfg.Live.Listen,
			"retain_turns", cfg.Live.RetainTurns, "content", cfg.Live.Content)
		sinks = append(sinks, liveStore)
	}

	return sinks, guard, metricsSink, liveStore, nil
}
