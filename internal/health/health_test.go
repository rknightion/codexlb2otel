package health

import (
	"encoding/json"
	"io"
	"log/slog"
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
