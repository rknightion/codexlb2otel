package config

import (
	"strings"
	"testing"
)

// Secret must mask through every formatting path a struct can leak through -
// fmt's %v/%s (String), %#v (GoString), and encoding/json (MarshalJSON) - because a
// service that gets even one of these right and misses another still leaks.
func TestSecret_MasksEveryFormattingPath(t *testing.T) {
	s := Secret("do-not-leak-me")

	if s.String() != Mask {
		t.Errorf("String() = %q, want %q", s.String(), Mask)
	}
	if s.GoString() != Mask {
		t.Errorf("GoString() = %q, want %q", s.GoString(), Mask)
	}
	b, err := s.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != `"`+Mask+`"` {
		t.Errorf("MarshalJSON() = %s, want %q", got, Mask)
	}
	if strings.Contains(s.String()+s.GoString()+string(b), "do-not-leak-me") {
		t.Fatal("the raw value leaked through some formatting path")
	}
}

// Resolve is the only way to obtain the real value, and it must fail rather than
// return empty for every indirection kind - a service that starts with a
// silently-empty credential only discovers it at push time, against a 401.
func TestSecret_Resolve(t *testing.T) {
	t.Setenv("CODEXLB2OTEL_CFG_TEST", "resolved-value")

	cases := []struct {
		name    string
		secret  Secret
		want    string
		wantErr bool
	}{
		{"literal value passes through", Secret("literal"), "literal", false},
		{"env var resolves", Secret("${CODEXLB2OTEL_CFG_TEST}"), "resolved-value", false},
		{"unset env var errors", Secret("${CODEXLB2OTEL_CFG_TEST_UNSET}"), "", true},
		{"empty is an error, not empty-string success", Secret(""), "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.secret.Resolve()
			if (err != nil) != c.wantErr {
				t.Fatalf("Resolve() err = %v, wantErr %v", err, c.wantErr)
			}
			if !c.wantErr && got != c.want {
				t.Errorf("Resolve() = %q, want %q", got, c.want)
			}
		})
	}
}

// Default() alone leaves every sink disabled, which Validate correctly rejects - a
// service that tailed the archive and shipped it nowhere would be a silent no-op, and
// the issue's acceptance criterion is that startup fails loudly instead. This pins
// that Default() is a safe STARTING point, not a config that passes Validate() on
// its own; an operator must opt at least one sink in.
func TestDefault_RequiresASinkToValidate(t *testing.T) {
	if err := Default().Validate(); err == nil {
		t.Fatal("Default() with every sink disabled unexpectedly passed Validate()")
	}

	cfg := Default()
	cfg.Loki.Enabled = true
	cfg.Loki.User = "1"
	cfg.Loki.Token = "t"
	if err := cfg.Validate(); err != nil {
		t.Errorf("Default() plus one enabled, fully-credentialed sink should validate: %v", err)
	}
}

// Validate must report EVERY problem, not just the first - the issue's whole point is
// fixing a misconfigured deployment in one pass instead of one restart per mistake.
func TestValidate_NamesFieldsAndReportsAllOfThem(t *testing.T) {
	cfg := Config{} // the zero value: everything is wrong
	err := cfg.Validate()
	if err == nil {
		t.Fatal("the zero value Config unexpectedly validated")
	}
	for _, want := range []string{
		"service.name",
		"archive.dir",
		"archive.checkpoint",
		"archive.poll_interval",
		"archive.chunk_bytes",
		"log.level",
		"log.format",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Validate() error missing %q: %v", want, err)
		}
	}
}

// loki.max_line_bytes has a hard ceiling because Loki DISCARDS an oversized line
// rather than truncating it - silently accepting a config above that ceiling would
// reproduce the exact data loss the field exists to prevent.
func TestValidate_RejectsMaxLineBytesAboveLokiCeiling(t *testing.T) {
	cfg := Default()
	cfg.Loki.Enabled = true
	cfg.Loki.User = "1"
	cfg.Loki.Token = "t"
	cfg.Loki.MaxLineBytes = 300 << 10 // over Loki's 256KB max_line_size

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected max_line_bytes above the Loki ceiling to fail validation")
	}
	if !strings.Contains(err.Error(), "loki.max_line_bytes") {
		t.Errorf("error does not name loki.max_line_bytes: %v", err)
	}
}
