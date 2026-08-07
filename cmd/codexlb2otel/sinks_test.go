package main

import (
	"log/slog"
	"testing"

	"github.com/rknightion/codexlb2otel/internal/config"
	"github.com/rknightion/codexlb2otel/internal/sink"
)

func TestBuildSinks_OneEntryPerEnabledDestination(t *testing.T) {
	log := slog.New(slog.DiscardHandler)

	cases := []struct {
		name string
		cfg  func(*config.Config)
		want int
	}{
		{"nothing enabled", func(*config.Config) {}, 0},
		{"loki only", func(c *config.Config) { c.Loki.Enabled = true }, 1},
		{"metrics only", func(c *config.Config) { c.OTLP.Metrics.Enabled = true }, 1},
		{"traces only", func(c *config.Config) { c.OTLP.Traces.Enabled = true }, 1},
		{"all three", func(c *config.Config) {
			c.Loki.Enabled = true
			c.OTLP.Metrics.Enabled = true
			c.OTLP.Traces.Enabled = true
		}, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			tc.cfg(&cfg)

			got, err := buildSinks(cfg, log)
			if err != nil {
				t.Fatalf("buildSinks: %v", err)
			}
			multi, ok := got.(sink.Multi)
			if !ok {
				t.Fatalf("buildSinks returned %T, want sink.Multi", got)
			}
			if len(multi) != tc.want {
				t.Errorf("len(multi) = %d, want %d", len(multi), tc.want)
			}
			// Every entry is a stand-in until the other lanes' sinks land - see the
			// comment on buildSinks for why importing them here would not compile yet.
			for i, s := range multi {
				if _, ok := s.(sink.Discard); !ok {
					t.Errorf("entry %d is %T, want sink.Discard (the wiring pass has not run)", i, s)
				}
			}
		})
	}
}
