package main

import (
	"log/slog"

	"github.com/rknightion/codexlb2otel/internal/config"
	"github.com/rknightion/codexlb2otel/internal/sink"
)

// buildSinks wires one sink.Multi entry per enabled destination.
//
// THE REAL SINKS DO NOT EXIST YET IN THIS WORKING TREE. internal/sink/loki,
// internal/sink/otlpmetric and internal/sink/otlptrace are being built concurrently by
// other lanes of this parallel build (see issue #1's wave plan) and importing any of
// them here would not compile. Each enabled destination gets sink.Discard{} instead -
// it satisfies the Sink interface and drops everything, so the rest of the service
// (batching, checkpointing, shutdown ordering) can be built and tested against a real
// Multi shape today. The wiring pass swaps each Discard{} for the matching
// constructor once all four lanes land; nothing else in this file should need to
// change when it does.
func buildSinks(cfg config.Config, log *slog.Logger) (sink.Sink, error) {
	var sinks sink.Multi

	if cfg.Loki.Enabled {
		log.Info("loki sink enabled (stub)", "url", cfg.Loki.URL)
		// TODO(wiring pass): sinks = append(sinks, loki.New(cfg.Loki, log))
		sinks = append(sinks, sink.Discard{})
	}
	if cfg.OTLP.Metrics.Enabled {
		log.Info("otlp metrics sink enabled (stub)", "endpoint", cfg.OTLP.Endpoint)
		// TODO(wiring pass): sinks = append(sinks, otlpmetric.New(cfg.OTLP, log))
		sinks = append(sinks, sink.Discard{})
	}
	if cfg.OTLP.Traces.Enabled {
		log.Info("otlp traces sink enabled (stub)", "endpoint", cfg.OTLP.Endpoint)
		// TODO(wiring pass): sinks = append(sinks, otlptrace.New(cfg.OTLP, log))
		sinks = append(sinks, sink.Discard{})
	}

	return sinks, nil
}
