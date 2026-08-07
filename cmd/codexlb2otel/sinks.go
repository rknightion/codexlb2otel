package main

import (
	"context"
	"log/slog"

	"github.com/rknightion/codexlb2otel/internal/attr"
	"github.com/rknightion/codexlb2otel/internal/config"
	"github.com/rknightion/codexlb2otel/internal/sink"
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
func buildSinks(ctx context.Context, cfg config.Config, log *slog.Logger) (sink.Sink, *attr.Guard, error) {
	guard := attr.NewGuard()
	var sinks sink.Multi

	if cfg.Loki.Enabled {
		s, err := loki.New(cfg.Loki, guard, cfg.Service.Name)
		if err != nil {
			return nil, nil, err
		}
		log.Info("loki sink enabled", "url", cfg.Loki.URL, "labels", cfg.Loki.Labels,
			"max_line_bytes", cfg.Loki.MaxLineBytes)
		sinks = append(sinks, s)
	}
	if cfg.OTLP.Metrics.Enabled {
		s, err := otlpmetric.New(ctx, cfg.OTLP, cfg.Service, guard)
		if err != nil {
			return nil, nil, err
		}
		log.Info("otlp metrics sink enabled", "endpoint", cfg.OTLP.Endpoint,
			"interval", cfg.OTLP.Metrics.Interval)
		sinks = append(sinks, s)
	}
	if cfg.OTLP.Traces.Enabled {
		s, err := otlptrace.New(ctx, cfg.OTLP, cfg.Service, guard)
		if err != nil {
			return nil, nil, err
		}
		log.Info("otlp traces sink enabled", "endpoint", cfg.OTLP.Endpoint,
			"sample_ratio", cfg.OTLP.Traces.SampleRatio)
		sinks = append(sinks, s)
	}

	return sinks, guard, nil
}
