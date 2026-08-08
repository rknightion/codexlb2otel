package config

import (
	"strings"
	"testing"
	"time"
)

// The live endpoint serves conversation content - prompts, assistant messages, whole
// command output. Binding it off-loopback with no token is the one mistake that turns a
// convenience feature into an unauthenticated transcript feed, so it must be an error
// rather than a warning in a log nobody reads.
func TestValidate_LiveExposureGuard(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Live)
		wantErr bool
	}{
		{"loopback, no token", func(*Live) {}, false},
		{"loopback by name", func(l *Live) { l.Listen = "localhost:9465" }, false},
		{"ipv6 loopback", func(l *Live) { l.Listen = "[::1]:9465" }, false},
		{
			// The case an eyeball misreads: this binds every interface and looks far more
			// innocent than 0.0.0.0 does.
			name:    "empty host binds everything",
			mutate:  func(l *Live) { l.Listen = ":9465" },
			wantErr: true,
		},
		{"wildcard", func(l *Live) { l.Listen = "0.0.0.0:9465" }, true},
		{"lan address", func(l *Live) { l.Listen = "192.168.1.10:9465" }, true},
		{
			name:   "off-loopback is fine with a token",
			mutate: func(l *Live) { l.Listen, l.Token = "0.0.0.0:9465", "s3cret" },
		},
		{
			name:   "off-loopback is fine when explicitly acknowledged",
			mutate: func(l *Live) { l.Listen, l.AllowInsecure = "0.0.0.0:9465", true },
		},
		{
			// Fail closed: an address shape this cannot parse is not assumed safe.
			name:    "unparseable listen",
			mutate:  func(l *Live) { l.Listen = "not-an-address" },
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			cfg.Loki.Enabled, cfg.Loki.User, cfg.Loki.Token = true, "123456", "tok"
			cfg.Live.Enabled = true
			tc.mutate(&cfg.Live)

			err := cfg.Validate()
			gotGuard := err != nil && strings.Contains(err.Error(), "live.listen")
			if gotGuard != tc.wantErr {
				t.Errorf("exposure guard fired = %v, want %v (err: %v)", gotGuard, tc.wantErr, err)
			}
		})
	}
}

func TestValidate_LiveBounds(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Live)
		want   string
	}{
		{"no listen", func(l *Live) { l.Listen = "" }, "live.listen is empty"},
		{"zero retain", func(l *Live) { l.RetainTurns = 0 }, "live.retain_turns"},
		{"zero window", func(l *Live) { l.RetainWindow = 0 }, "live.retain_window"},
		{"negative window", func(l *Live) { l.RetainWindow = -time.Minute }, "live.retain_window"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			cfg.Loki.Enabled, cfg.Loki.User, cfg.Loki.Token = true, "123456", "tok"
			cfg.Live.Enabled = true
			tc.mutate(&cfg.Live)

			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Validate = %v, want it to name %s", err, tc.want)
			}
		})
	}
}

// The live view is a legitimate sole reason to run the service: with it on, the archive
// is being tailed FOR something, so the every-sink-disabled check must not block it.
func TestValidate_LiveAloneIsNotAnEmptyPipeline(t *testing.T) {
	cfg := Default()
	cfg.Live.Enabled = true
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate = %v, want the live view alone to be a valid deployment", err)
	}
}

// The token can be an ${ENV} or file: reference, so it must never be printed even by a
// config dump the health endpoint serves wholesale.
func TestLive_TokenIsMaskedInAConfigDump(t *testing.T) {
	cfg := Default()
	cfg.Live.Token = "super-secret"
	body, err := cfg.Live.Token.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	if strings.Contains(string(body), "super-secret") {
		t.Errorf("live.token serialised as %s; it leaks through /healthz", body)
	}
}
