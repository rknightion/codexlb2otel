package config

import (
	"strings"
	"testing"
)

// validBase is Default() plus the one enabled sink Validate() insists on, so these tests
// fail on the summarize block and nothing else.
func validBase() Config {
	c := Default()
	c.Loki.Enabled = true
	c.Loki.User = "1"
	c.Loki.Token = "t"
	return c
}

// The block is present in every default config, so validating it unconditionally would
// fail every deployment that never asked for the feature.
func TestValidate_SummarizeIgnoredWhenDisabled(t *testing.T) {
	c := validBase()
	c.Summarize.Model = ""
	c.Summarize.Concurrency = -5
	c.Summarize.DataCollection = "nonsense"

	if err := c.Validate(); err != nil {
		t.Fatalf("a disabled summarize block must not fail validation: %v", err)
	}
}

func TestValidate_SummarizeWhenEnabled(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Summarize)
		want   string
	}{
		{"valid", func(*Summarize) {}, ""},
		{"no model", func(s *Summarize) { s.Model = "" }, "summarize.model"},
		{"no key", func(s *Summarize) { s.APIKey = "" }, "summarize.api_key"},
		{
			// Resolve treats an unset environment variable as an error rather than an
			// empty string, so a missing key fails at startup and not at push time.
			name:   "key points at an unset variable",
			mutate: func(s *Summarize) { s.APIKey = "${CLBSUM_DEFINITELY_UNSET_VAR}" },
			want:   "summarize.api_key",
		},
		{"zero session budget", func(s *Summarize) { s.MaxCharsPerSession = 0 }, "max_chars_per_session"},
		{"negative tool input budget", func(s *Summarize) { s.MaxCharsPerToolInput = -1 }, "max_chars_per_tool_input"},
		{"zero tool output budget", func(s *Summarize) { s.MaxCharsPerToolOutput = 0 }, "max_chars_per_tool_output"},
		{"zero concurrency", func(s *Summarize) { s.Concurrency = 0 }, "summarize.concurrency"},
		{"negative retries", func(s *Summarize) { s.MaxRetries = -1 }, "summarize.max_retries"},
		{"zero timeout", func(s *Summarize) { s.Timeout = 0 }, "summarize.timeout"},
		{"bad data_collection", func(s *Summarize) { s.DataCollection = "maybe" }, "summarize.data_collection"},
		{"data_collection allow is valid", func(s *Summarize) { s.DataCollection = "allow" }, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validBase()
			c.Summarize.Enabled = true
			c.Summarize.APIKey = "literal-key"
			tc.mutate(&c.Summarize)

			err := c.Validate()
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.want == "":
				return
			case err == nil:
				t.Fatalf("expected an error mentioning %q", tc.want)
			case !strings.Contains(err.Error(), tc.want):
				t.Errorf("error %q does not name %q", err, tc.want)
			}
		})
	}
}

// This is the project's only third-party egress, so the restrictive routing preferences
// have to be what you get by default rather than something you remember to set.
func TestDefault_SummarizeIsOffAndRestrictive(t *testing.T) {
	s := Default().Summarize

	if s.Enabled {
		t.Error("summarize is enabled by default; it sends conversation content off-site")
	}
	if !s.ZDR {
		t.Error("zdr is off by default")
	}
	if s.DataCollection != "deny" {
		t.Errorf("data_collection = %q by default, want deny (OpenRouter's own default is allow)", s.DataCollection)
	}
	if s.Model == "" {
		t.Error("no default model")
	}
	if s.Timeout <= 0 || s.Concurrency <= 0 {
		t.Errorf("defaults are not usable: timeout=%s concurrency=%d", s.Timeout, s.Concurrency)
	}
	// The default model has a 1M-token context; a budget that does not exploit it would
	// chunk sessions that comfortably fit in one call.
	if s.MaxCharsPerSession < 1_000_000 {
		t.Errorf("max_chars_per_session = %d, too small for the default model's context", s.MaxCharsPerSession)
	}
	// Arguments say what changed; output is console noise. The asymmetry is the design.
	if s.MaxCharsPerToolInput <= s.MaxCharsPerToolOutput {
		t.Errorf("tool input budget %d should exceed tool output budget %d",
			s.MaxCharsPerToolInput, s.MaxCharsPerToolOutput)
	}
}

// A key must never reach a log line or the /healthz config dump.
func TestSummarize_APIKeyIsMasked(t *testing.T) {
	c := Default()
	c.Summarize.APIKey = "sk-or-v1-real-secret"

	if got := c.Summarize.APIKey.String(); strings.Contains(got, "real-secret") {
		t.Errorf("String() leaked the key: %q", got)
	}
	b, err := c.Summarize.APIKey.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	if strings.Contains(string(b), "real-secret") {
		t.Errorf("MarshalJSON leaked the key: %s", b)
	}

	got, err := c.Summarize.APIKey.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "sk-or-v1-real-secret" {
		t.Errorf("Resolve() = %q, want the real value", got)
	}
}
