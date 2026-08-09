package summary

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OpenRouterTeam/go-sdk/models/sdkerrors"
)

// The distinction this pins cost a real run: a single call that ran out of time and a
// cancelled run both surface as context.DeadlineExceeded/Canceled, and they need opposite
// answers. Matching on the returned error treated a slow combining pass as a cancellation
// and discarded 86 calls that had already succeeded and been paid for.
func TestRetryable(t *testing.T) {
	live := context.Background()
	dead, cancel := context.WithCancel(context.Background())
	cancel()

	cases := []struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}{
		{
			name: "the http client's own timeout, run still live",
			ctx:  live,
			err:  context.DeadlineExceeded,
			want: true,
		},
		{
			name: "same error, but the run was cancelled",
			ctx:  dead,
			err:  context.DeadlineExceeded,
			want: false,
		},
		{"ctrl-c", dead, context.Canceled, false},
		{"rate limited", live, &sdkerrors.APIError{StatusCode: http.StatusTooManyRequests}, true},
		{"server fault", live, &sdkerrors.APIError{StatusCode: http.StatusBadGateway}, true},
		{"rejected key", live, &sdkerrors.APIError{StatusCode: http.StatusUnauthorized}, false},
		{"bad request", live, &sdkerrors.APIError{StatusCode: http.StatusBadRequest}, false},
		{"payment required", live, &sdkerrors.APIError{StatusCode: http.StatusPaymentRequired}, false},
		{
			// Not modelled as an APIError, so it never reached a server: a dropped
			// connection or a DNS blip, both worth another go.
			name: "transport failure",
			ctx:  live,
			err:  errors.New("connection reset by peer"),
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := retryable(tc.ctx, tc.err); got != tc.want {
				t.Errorf("retryable = %v, want %v", got, tc.want)
			}
		})
	}
}

// A run's calls must share one sticky-routing key, or every call hashes differently and
// scatters across providers - and a prompt cache never landed on twice is no cache.
func TestSessionID_StableWithinAClientAndUniqueBetween(t *testing.T) {
	a, err := NewOpenRouter(OpenRouterOptions{APIKey: "k", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewOpenRouter(OpenRouterOptions{APIKey: "k", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}

	if a.session == "" {
		t.Fatal("no session id was minted")
	}
	if a.session == b.session {
		t.Error("two runs share a session id; that pins every run onto one provider forever")
	}
	if len(a.session) > 256 {
		t.Errorf("session id is %d chars, over OpenRouter's 256 limit", len(a.session))
	}
	if !strings.HasPrefix(a.session, "clbsum") {
		t.Errorf("session id %q should name the tool, so it is identifiable in OpenRouter's activity view", a.session)
	}
}

func TestNewOpenRouter_RejectsUnusableOptions(t *testing.T) {
	if _, err := NewOpenRouter(OpenRouterOptions{Model: "m"}); err == nil {
		t.Error("an empty API key was accepted")
	}
	if _, err := NewOpenRouter(OpenRouterOptions{APIKey: "k"}); err == nil {
		t.Error("an empty model was accepted")
	}
}

// data_collection must never silently become OpenRouter's own default of "allow" - this
// ships real prompts and real command output to a third party.
func TestNewOpenRouter_DefaultsDataCollectionToDeny(t *testing.T) {
	c, err := NewOpenRouter(OpenRouterOptions{APIKey: "k", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if c.opts.DataCollection != "deny" {
		t.Errorf("DataCollection defaulted to %q, want deny", c.opts.DataCollection)
	}
}

// The response-cache opt-in rides on the HTTP client because the SDK exposes no
// per-request header hook, so the header actually reaching the wire is worth pinning.
func TestHeaderClient_SetsTheCacheOptIn(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-OpenRouter-Cache")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := &headerClient{
		base:    srv.Client(),
		headers: map[string]string{"X-OpenRouter-Cache": "true"},
	}
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if got != "true" {
		t.Errorf("X-OpenRouter-Cache = %q, want true", got)
	}
}

func TestTextOf_ReportsWhyThereIsNoText(t *testing.T) {
	if _, err := textOf(nil); err == nil {
		t.Error("a nil response was accepted")
	}
}
