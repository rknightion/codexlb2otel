package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// An absent key must keep its Default(), not fall to the zero value - that is the
// entire point of decoding over an already-populated struct rather than a bare one.
func TestParse_OverlaysDefaults(t *testing.T) {
	yaml := `
service:
  name: test-svc
loki:
  enabled: true
  user: "12345"
  token: "literal-token"
`
	cfg, err := parse([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	def := Default()

	if cfg.Service.Name != "test-svc" {
		t.Errorf("service.name = %q, want the overridden value", cfg.Service.Name)
	}
	// Untouched by the YAML: must still be exactly what Default() set.
	if cfg.Loki.BatchSize != def.Loki.BatchSize {
		t.Errorf("loki.batch_size = %d, want default %d (an unset key must not zero it)",
			cfg.Loki.BatchSize, def.Loki.BatchSize)
	}
	if cfg.Loki.MaxLineBytes != def.Loki.MaxLineBytes {
		t.Errorf("loki.max_line_bytes = %d, want default %d", cfg.Loki.MaxLineBytes, def.Loki.MaxLineBytes)
	}
	if cfg.Archive != def.Archive {
		t.Errorf("archive section changed despite not being set: got %+v, want %+v", cfg.Archive, def.Archive)
	}
	if cfg.Health != def.Health {
		t.Errorf("health section changed despite not being set: got %+v, want %+v", cfg.Health, def.Health)
	}
}

// A duration field is the one place a config value could silently misparse into
// something other than what an operator wrote - "45s" landing as 45 nanoseconds, say
// - so this pins the whole set of duration fields Load has to carry through.
func TestParse_ParsesDurationStrings(t *testing.T) {
	yaml := `
archive:
  poll_interval: "45s"
  state_retain: "168h"
  delete_after: "72h"
postgres:
  lookup_timeout: "2s"
  prefetch_interval: "5s"
probe:
  interval: "24h"
loki:
  enabled: true
  user: "1"
  token: "t"
  batch_wait: "3s"
  timeout: "10s"
otlp:
  instance_id: "1"
  token: "t"
  metrics:
    enabled: true
    interval: "90s"
`
	cfg, err := parse([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cases := []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"archive.poll_interval", cfg.Archive.PollInterval, 45 * time.Second},
		{"archive.state_retain", cfg.Archive.StateRetain, 168 * time.Hour},
		{"archive.delete_after", cfg.Archive.DeleteAfter, 72 * time.Hour},
		{"postgres.lookup_timeout", cfg.Postgres.LookupTimeout, 2 * time.Second},
		{"postgres.prefetch_interval", cfg.Postgres.PrefetchInterval, 5 * time.Second},
		{"probe.interval", cfg.Probe.Interval, 24 * time.Hour},
		{"loki.batch_wait", cfg.Loki.BatchWait, 3 * time.Second},
		{"loki.timeout", cfg.Loki.Timeout, 10 * time.Second},
		{"otlp.metrics.interval", cfg.OTLP.Metrics.Interval, 90 * time.Second},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %s, want %s", c.name, c.got, c.want)
		}
	}
}

// A malformed duration must fail loudly rather than silently keeping Default()'s
// poll_interval because "5 seconds" failed to parse - that would be exactly the
// half-wired startup the issue calls out. yaml.v3's own error names the bad value
// but not the YAML key path (verified against the pinned v3.0.1); Validate's field-
// naming guarantee is what covers the "names the field" half of the acceptance
// criterion, for the fields it checks the range of - see TestParse_ReturnsValidateErrorUnchanged.
func TestParse_InvalidDurationNamesTheField(t *testing.T) {
	_, err := parse([]byte("archive:\n  poll_interval: \"5 seconds\"\n"))
	if err == nil {
		t.Fatal("expected an error for an unparseable duration")
	}
	if !strings.Contains(err.Error(), "5 seconds") {
		t.Errorf("error does not surface the offending value: %v", err)
	}
}

// Load must surface Validate's error text unchanged - Validate already names every
// bad field, and duplicating that logic here would be a second place to keep in sync.
func TestParse_ReturnsValidateErrorUnchanged(t *testing.T) {
	_, err := parse([]byte("service:\n  name: \"\"\n"))
	if err == nil {
		t.Fatal("expected the empty service.name to fail validation")
	}
	if !strings.Contains(err.Error(), "service.name is empty") {
		t.Errorf("error does not match Validate's own message: %v", err)
	}
}

// Secrets resolve through both indirections the issue requires, and Load must never
// itself hold or print the resolved value - it only calls Resolve via Validate, to
// check the reference is usable, and never logs the result.
func TestLoad_SecretsResolveFromEnvAndFile(t *testing.T) {
	t.Setenv("CODEXLB2OTEL_TEST_TOKEN", "env-secret-value")

	dir := t.TempDir()
	secretFile := filepath.Join(dir, "otlp-token")
	if err := os.WriteFile(secretFile, []byte("file-secret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	yaml := `
loki:
  enabled: true
  user: "1"
  token: "${CODEXLB2OTEL_TEST_TOKEN}"
otlp:
  metrics:
    enabled: true
  instance_id: "1"
  token: "file:` + secretFile + `"
`
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got, err := cfg.Loki.Token.Resolve(); err != nil || got != "env-secret-value" {
		t.Errorf("loki.token resolved to (%q, %v), want env-secret-value", got, err)
	}
	if got, err := cfg.OTLP.Token.Resolve(); err != nil || got != "file-secret-value" {
		t.Errorf("otlp.token resolved to (%q, %v), want file-secret-value", got, err)
	}

	// The config struct itself must mask both, regardless of resolution: String,
	// GoString and MarshalJSON never see the reference kind, only the type.
	if cfg.Loki.Token.String() != Mask || cfg.OTLP.Token.String() != Mask {
		t.Error("a resolvable Secret must still format as the mask")
	}
}

// An unset ${ENV} must fail Load loudly rather than starting with an empty
// credential that only surfaces as a 401 against the remote much later.
func TestLoad_UnresolvableSecretFailsLoudly(t *testing.T) {
	yaml := `
loki:
  enabled: true
  user: "1"
  token: "${CODEXLB2OTEL_DOES_NOT_EXIST}"
`
	_, err := parse([]byte(yaml))
	if err == nil {
		t.Fatal("expected an error for an unset environment variable")
	}
	if !strings.Contains(err.Error(), "loki.token") {
		t.Errorf("error does not name loki.token: %v", err)
	}
}

func TestParse_UnresolvablePostgresSecretDoesNotStopOtherSignals(t *testing.T) {
	const name = "CODEXLB2OTEL_MISSING_POSTGRES_TEST_DSN"
	t.Setenv(name, "")
	cfg, err := parse([]byte("postgres:\n  enabled: true\n  dsn: ${" + name + "}\n" +
		"loki:\n  enabled: true\n  user: test\n  token: test\n"))
	if err != nil {
		t.Fatalf("optional postgres config stopped the service: %v", err)
	}
	if !cfg.Postgres.Enabled {
		t.Fatal("postgres.enabled was not retained for runtime degradation")
	}
}

// config.example.yaml is not documentation: it is copied verbatim onto camden as the
// deployed config (see docker-compose.yml's CONFIG FILE block), and nothing else
// checks it. A typo in a yaml key is silent - the field keeps its Default() value and
// the operator's setting is simply ignored, which for archive.retain_days means the
// disk quietly never gets reclaimed.
func TestLoad_ExampleConfigIsDeployable(t *testing.T) {
	for _, env := range []string{
		"CODEXLB2OTEL_LOKI_TOKEN",
		"CODEXLB2OTEL_OTLP_TOKEN",
		"CODEXLB2OTEL_AGENTO11Y_TOKEN",
	} {
		t.Setenv(env, "test-token")
	}

	cfg, err := Load(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatalf("the shipped example config does not load and validate: %v", err)
	}

	// Spot-check one key per section that Default() does NOT already supply, so a
	// dropped or misspelled key cannot pass by falling back to the default.
	if cfg.Service.Environment != "lab" {
		t.Errorf("service.environment = %q, want lab", cfg.Service.Environment)
	}
	if cfg.Archive.RetainDays != 1 {
		t.Errorf("archive.retain_days = %d, want 1 (yesterday and older are reclaimed)", cfg.Archive.RetainDays)
	}
	// Deliberately false as shipped: the access policy has no sigil:write scope, and
	// with it on, the 401 is a config fault that holds the checkpoint and stops every
	// other signal. Asserted rather than ignored so flipping it is a decision someone
	// makes with the credential in hand, not an accident.
	if cfg.AgentO11y.Enabled {
		t.Error("agento11y.enabled is true; without sigil:write on the token that 401s and holds the checkpoint")
	}
	if cfg.Postgres.Enabled {
		t.Error("postgres.enabled is true; enrichment must ship disabled until a read-only DSN is deployed")
	}
	if cfg.Probe.Enabled {
		t.Error("probe.enabled is true; the in-process archive scan must ship disabled and be enabled deliberately")
	}
	if cfg.Loki.User == "" || cfg.OTLP.InstanceID == "" || cfg.AgentO11y.User == "" {
		t.Error("a basic-auth username is missing; that is a 401 at push time, not a startup error")
	}
}
