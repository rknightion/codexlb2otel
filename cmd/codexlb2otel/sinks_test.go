package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rknightion/codexlb2otel/internal/attr"
	"github.com/rknightion/codexlb2otel/internal/config"
	"github.com/rknightion/codexlb2otel/internal/sink"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

// credentialed points every sink at a local stub and gives it credentials that
// resolve. Both halves matter: a sink refuses to construct without credentials, and
// without the stub these sinks push at the REAL Grafana Cloud endpoints, which the
// defaults carry - a test suite that authenticates against production and passes only
// because the token is wrong is not a test suite.
func credentialed(t *testing.T, c *config.Config) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	c.Loki.URL, c.Loki.User, c.Loki.Token = srv.URL+"/loki/api/v1/push", "1175378", "test-token"
	c.OTLP.Endpoint, c.OTLP.InstanceID, c.OTLP.Token = srv.URL, "1217581", "test-token"
	c.AgentO11y.URL, c.AgentO11y.User, c.AgentO11y.Token = srv.URL+"/api/v1/generations:export", "1217581", "test-token"
}

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
		{"agento11y only", func(c *config.Config) { c.AgentO11y.Enabled = true }, 1},
		{"all four", func(c *config.Config) {
			c.Loki.Enabled = true
			c.OTLP.Metrics.Enabled = true
			c.OTLP.Traces.Enabled = true
			c.AgentO11y.Enabled = true
		}, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			credentialed(t, &cfg)
			tc.cfg(&cfg)

			got, guard, err := buildSinks(t.Context(), cfg, log)
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
			if guard == nil {
				t.Error("no guard returned; nothing would report attribute rejections")
			}
			for i, s := range multi {
				if _, stub := s.(sink.Discard); stub {
					t.Errorf("entry %d is still sink.Discard; the wiring pass did not replace it", i)
				}
			}
			if err := got.Close(context.Background()); err != nil {
				t.Errorf("close: %v", err)
			}
		})
	}
}

// The caps are per field across the PROCESS. A guard per sink would let each admit its
// own 32 models, making the real ceiling three times what the contract states.
func TestBuildSinks_AllSinksShareOneGuard(t *testing.T) {
	cfg := config.Default()
	credentialed(t, &cfg)
	cfg.Loki.Enabled, cfg.OTLP.Metrics.Enabled, cfg.OTLP.Traces.Enabled = true, true, true
	cfg.AgentO11y.Enabled = true

	snk, guard, err := buildSinks(t.Context(), cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("buildSinks: %v", err)
	}
	defer snk.Close(context.Background())

	if n := len(guard.Distinct()); n != 0 {
		t.Fatalf("a fresh guard has already seen %d fields", n)
	}

	// Emitting through the Multi must move the guard buildSinks returned. If any sink
	// held a private one, its values would land there and this stays empty - which is
	// exactly the failure that would triple the real cardinality ceiling in silence.
	err = snk.Emit(t.Context(), []*turn.Turn{{
		RequestID: "ws_1", ResponseID: "resp_1", ThreadID: "t1",
		Model: "gpt-5.6-sol", Status: "completed", Family: "websocket", RequestKind: "turn",
		AccountID: "acct-1", FirstTS: time.Now(), LastTS: time.Now(),
	}})
	// Delivery itself is expected to fail - nothing is listening on the real endpoints -
	// but the attributes are built before the push, which is all this asserts.
	_ = err

	seen := guard.Distinct()
	if len(seen) == 0 {
		t.Fatal("no sink used the shared guard; each must be holding its own")
	}
	if seen[attr.GenAIRequestModel] != 1 {
		t.Errorf("shared guard saw %d models, want 1: %v", seen[attr.GenAIRequestModel], seen)
	}
}
