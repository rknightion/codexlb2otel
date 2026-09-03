package live

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rknightion/codexlb2otel/internal/turn"
)

func testServer(t *testing.T, token string) (*Store, http.Handler) {
	t.Helper()
	store := newTestStore(t, Options{Content: true})
	srv := NewServer(store, ServerConfig{
		Listen: "127.0.0.1:0", Token: token, PollInterval: 5 * time.Second,
	}, slog.New(slog.DiscardHandler))
	return store, srv.http.Handler
}

func TestServer_Routes(t *testing.T) {
	store, h := testServer(t, "")
	emit(t, store, completed("thread-a", "turn-1", 1))

	t.Run("index is self-contained and declares a restrictive CSP", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
			t.Errorf("CSP = %q, want default-src 'none'", csp)
		}
		if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "font-src 'self'") {
			t.Errorf("CSP = %q, want same-origin fonts", csp)
		}
		// The page must never reach off-box: no CDN, no font host, nothing. A strict CSP
		// would break it, which is the point of asserting both together.
		for _, bad := range []string{"http://", "https://"} {
			if strings.Contains(rec.Body.String(), bad) {
				t.Errorf("index references an external URL (%q); the CSP forbids loading it", bad)
			}
		}
	})

	t.Run("embedded font is served locally with an immutable cache policy", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
			"/_static/fonts/hanken-grotesk-latin.woff2", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if got := rec.Header().Get("Content-Type"); got != "font/woff2" {
			t.Errorf("Content-Type = %q, want font/woff2", got)
		}
		if rec.Body.Len() == 0 {
			t.Fatal("font response is empty")
		}
	})

	t.Run("unknown font is rejected", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
			"/_static/fonts/not-embedded.woff2", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("threads carries the poll interval so the UI can state its staleness", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/threads", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var got struct {
			Roots          []*Thread `json:"roots"`
			PollIntervalMs int64     `json:"poll_interval_ms"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(got.Roots) != 1 {
			t.Errorf("got %d roots, want 1", len(got.Roots))
		}
		if got.PollIntervalMs != 5000 {
			t.Errorf("poll_interval_ms = %d, want 5000", got.PollIntervalMs)
		}
	})

	t.Run("one thread's turns", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/threads/thread-a", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var got struct {
			Turns []*turn.Turn `json:"turns"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(got.Turns) != 1 {
			t.Errorf("got %d turns, want 1", len(got.Turns))
		}
	})

	t.Run("unknown thread", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/threads/nope", nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})
}

func TestServer_TokenGuardsEveryRoute(t *testing.T) {
	_, h := testServer(t, "s3cret")

	// The query form is not a convenience: EventSource cannot set headers, so without it
	// a token-protected stream is unreachable from a browser at all.
	cases := []struct {
		name string
		path string
		want int
	}{
		{"no token", "/api/threads", http.StatusUnauthorized},
		{"wrong token", "/api/threads?token=nope", http.StatusUnauthorized},
		{"right token in query", "/api/threads?token=s3cret", http.StatusOK},
		{"index is not exempt", "/", http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}

	t.Run("bearer header", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/threads", nil)
		req.Header.Set("Authorization", "Bearer s3cret")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rec.Code)
		}
	})
}

// The stream must deliver the current view on connect and again on every emit, over a
// real socket - httptest.Recorder cannot exercise flushing or a live connection.
func TestServer_StreamDeliversSnapshots(t *testing.T) {
	store, h := testServer(t, "")
	emit(t, store, completed("thread-a", "turn-1", 1))

	srv := httptest.NewServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/stream", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	read := func(sc *bufio.Scanner) Snapshot {
		t.Helper()
		var event string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				if event != "snapshot" {
					continue
				}
				var snap Snapshot
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &snap); err != nil {
					t.Fatalf("decode event: %v", err)
				}
				return snap
			}
		}
		t.Fatal("stream ended before a snapshot arrived")
		return Snapshot{}
	}

	sc := bufio.NewScanner(resp.Body)
	first := read(sc)
	if len(first.Roots) != 1 {
		t.Fatalf("primed snapshot has %d roots, want 1", len(first.Roots))
	}

	emit(t, store, completed("thread-b", "turn-2", 2))
	second := read(sc)
	if len(second.Roots) != 2 {
		t.Errorf("post-emit snapshot has %d roots, want 2", len(second.Roots))
	}
}

// SSE is newline-delimited, so a payload containing a raw newline would silently split
// into two malformed events. Conversation text is full of them.
func TestWriteEvent_EscapesNewlinesInContent(t *testing.T) {
	var b strings.Builder
	snap := Snapshot{Roots: []*Thread{{ThreadID: "t", Latest: "line one\nline two"}}}
	if err := writeEvent(&b, "snapshot", snap); err != nil {
		t.Fatalf("writeEvent: %v", err)
	}
	got := strings.TrimSuffix(b.String(), "\n\n")
	if n := strings.Count(got, "\n"); n != 1 {
		t.Errorf("event body contains %d newlines, want exactly the one separating event: from data:\n%q", n, got)
	}
}
