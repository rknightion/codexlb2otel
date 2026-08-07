package health

import (
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rknightion/codexlb2otel/internal/config"
)

func testLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// A secret value must never reach the wire, however it got into the config: a literal
// value is the worst case (an operator's mistake, not just the normal ${ENV}/file:
// indirection), so that is what this test plants.
func TestServer_ConfigDumpMasksSecrets(t *testing.T) {
	const lokiSecret = "loki-literal-token-do-not-leak"
	const otlpSecret = "otlp-literal-token-do-not-leak"

	cfg := config.Default()
	cfg.Loki.Enabled = true
	cfg.Loki.Token = config.Secret(lokiSecret)
	cfg.OTLP.Token = config.Secret(otlpSecret)

	s := New(cfg, testLogger())
	s.SetReady(true)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/healthz", nil)
	s.handle(rr, req)

	body, err := io.ReadAll(rr.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)

	for _, secret := range []string{lokiSecret, otlpSecret} {
		if strings.Contains(got, secret) {
			t.Fatalf("health response contains a raw secret value: body=%s", got)
		}
	}
	wantMasks := strings.Count(got, config.Mask)
	if wantMasks < 2 {
		t.Errorf("expected both tokens masked with %q, found %d occurrences in: %s",
			config.Mask, wantMasks, got)
	}

	var decoded response
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if string(decoded.Config.Loki.Token) != config.Mask {
		t.Errorf("decoded loki.token = %q, want the mask", decoded.Config.Loki.Token)
	}
	if string(decoded.Config.OTLP.Token) != config.Mask {
		t.Errorf("decoded otlp.token = %q, want the mask", decoded.Config.OTLP.Token)
	}
}

func TestServer_ReadinessGatesTheStatusCode(t *testing.T) {
	s := New(config.Default(), testLogger())

	rr := httptest.NewRecorder()
	s.handle(rr, httptest.NewRequest("GET", "/healthz", nil))
	if rr.Code != 503 {
		t.Errorf("before SetReady(true): status = %d, want 503", rr.Code)
	}

	s.SetReady(true)
	rr = httptest.NewRecorder()
	s.handle(rr, httptest.NewRequest("GET", "/healthz", nil))
	if rr.Code != 200 {
		t.Errorf("after SetReady(true): status = %d, want 200", rr.Code)
	}

	var decoded response
	if err := json.NewDecoder(rr.Result().Body).Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.Ready {
		t.Error("decoded body says ready=false after SetReady(true)")
	}
}

// TestProbe_MirrorsReadinessAndFailsClosed covers the container HEALTHCHECK path
// (`codexlb2otel -healthcheck`). The 503-while-draining case is the one that
// matters: Docker restarts an unhealthy container, so a probe that treated
// not-ready as success would report a service that has stopped polling as fine,
// and one that treated an unreachable service as success would never restart
// anything at all.
func TestProbe_MirrorsReadinessAndFailsClosed(t *testing.T) {
	srv := New(config.Config{Health: config.Health{Enabled: true, Listen: "127.0.0.1:0"}},
		slog.New(slog.DiscardHandler))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	go func() { _ = http.Serve(ln, srv.http.Handler) }()
	t.Cleanup(func() { _ = ln.Close() })

	cfg := config.Health{Enabled: true, Listen: addr}

	// Not ready yet - the handler returns 503, and the probe must fail rather than
	// report a still-starting service as healthy.
	if err := Probe(cfg); err == nil {
		t.Error("Probe succeeded against a not-ready server; a draining or still-starting " +
			"service would report healthy")
	}

	srv.SetReady(true)
	if err := Probe(cfg); err != nil {
		t.Errorf("Probe failed against a ready server: %v", err)
	}

	// Nothing listening at all. Fails closed, which is what makes the container
	// restart instead of sitting there dead and "healthy".
	if err := Probe(config.Health{Enabled: true, Listen: "127.0.0.1:1"}); err == nil {
		t.Error("Probe succeeded against a port with nothing on it")
	}

	if err := Probe(config.Health{Enabled: true}); err == nil {
		t.Error("Probe succeeded with an empty listen address")
	}
}
