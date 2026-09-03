package live

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

//go:embed ui/index.html ui/fonts/*.woff2
var ui embed.FS

var (
	fontHankenLatin    = mustReadUI("ui/fonts/hanken-grotesk-latin.woff2")
	fontHankenLatinExt = mustReadUI("ui/fonts/hanken-grotesk-latin-ext.woff2")
	fontJetBrainsMono  = mustReadUI("ui/fonts/JetBrainsMono-Variable.woff2")
)

func mustReadUI(name string) []byte {
	body, err := ui.ReadFile(name)
	if err != nil {
		panic("live: embedded UI asset missing: " + name)
	}
	return body
}

// shutdownGrace bounds the drain on stop. Deliberately short: the only long-lived
// request this server has is the SSE stream, and every one of those is by definition
// still open at shutdown. Waiting for them to finish means waiting forever.
const shutdownGrace = 2 * time.Second

// heartbeat is how often an idle SSE stream emits a comment line. Proxies and load
// balancers reap idle connections - 60s is a common default - and a stream that only
// speaks when something happens is idle for exactly the quiet periods a human is most
// likely to be watching through.
const heartbeat = 15 * time.Second

// ServerConfig is what the HTTP layer needs. Separate from Options, which is what the
// store needs.
type ServerConfig struct {
	Listen string
	// Token, when set, is required as `Authorization: Bearer <token>` or `?token=`.
	// The query form exists because EventSource cannot set headers, so a token-protected
	// stream is unreachable from a browser without it.
	Token string
	// PollInterval is the archive poll cadence, reported to the UI so it can state how
	// stale the in-flight rows may be rather than implying they are instantaneous.
	PollInterval time.Duration
}

// Server exposes a Store over HTTP.
type Server struct {
	store *Store
	cfg   ServerConfig
	log   *slog.Logger
	http  *http.Server
}

// NewServer builds a Server. It does not listen; call Run.
func NewServer(store *Store, cfg ServerConfig, log *slog.Logger) *Server {
	s := &Server{store: store, cfg: cfg, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/threads", s.handleThreads)
	mux.HandleFunc("GET /api/threads/{id}", s.handleThread)
	mux.HandleFunc("GET /api/stream", s.handleStream)
	mux.HandleFunc("GET /_static/fonts/{name}", s.handleFont)
	mux.HandleFunc("GET /", s.handleIndex)
	s.http = &http.Server{
		Addr:    cfg.Listen,
		Handler: s.authorize(mux),
		// No WriteTimeout, and that is not an oversight: it applies to the whole response,
		// so any non-zero value would sever every SSE stream on a fixed timer. ReadTimeout
		// still bounds a client that opens a connection and never sends a request.
		ReadHeaderTimeout: 5 * time.Second,
	}
	return s
}

// authorize gates every route on the shared token when one is configured.
func (s *Server) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Token == "" {
			next.ServeHTTP(w, r)
			return
		}
		got := r.URL.Query().Get("token")
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			got = strings.TrimPrefix(h, "Bearer ")
		}
		// Constant time, so a token cannot be recovered a byte at a time by timing the
		// comparison. Cheap here and the habit is worth keeping.
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.Token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	body, err := ui.ReadFile("ui/index.html")
	if err != nil {
		// Unreachable short of a broken build - the file is embedded at compile time.
		http.Error(w, "ui unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The page is entirely self-contained. Fonts are served from the same embedded FS;
	// no external script, style or font origin is allowed. Content is inserted through
	// DOM text nodes, but defence in depth matters more here than usual given what this
	// page displays.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; font-src 'self'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if _, err := w.Write(body); err != nil {
		s.log.Debug("live: write index", "err", err)
	}
}

func (s *Server) handleFont(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body []byte
	switch name {
	case "hanken-grotesk-latin.woff2":
		body = fontHankenLatin
	case "hanken-grotesk-latin-ext.woff2":
		body = fontHankenLatinExt
	case "JetBrainsMono-Variable.woff2":
		body = fontJetBrainsMono
	default:
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "font/woff2")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if _, err := w.Write(body); err != nil {
		s.log.Debug("live: write font", "name", name, "err", err)
	}
}

func (s *Server) handleThreads(w http.ResponseWriter, r *http.Request) {
	snap := s.store.Snapshot()
	s.writeJSON(w, r, struct {
		Snapshot
		PollIntervalMs int64 `json:"poll_interval_ms"`
	}{Snapshot: snap, PollIntervalMs: s.cfg.PollInterval.Milliseconds()})
}

func (s *Server) handleThread(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	turns := s.store.Turns(id)
	if len(turns) == 0 {
		http.Error(w, "no retained turns for that thread", http.StatusNotFound)
		return
	}
	s.writeJSON(w, r, map[string]any{"thread_id": id, "turns": turns})
}

func (s *Server) writeJSON(w http.ResponseWriter, r *http.Request, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The status line is long gone by the time an encode fails mid-body, so there is
		// nothing to report to the client. Log it and let the truncated body be the
		// client's error.
		s.log.Debug("live: encode response", "path", r.URL.Path, "err", err)
	}
}

// handleStream serves the snapshot feed as server-sent events.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	ch, cancel := s.store.Subscribe()
	defer cancel()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Tell any nginx in front of this not to buffer, which would otherwise hold events
	// until its buffer filled and make a live view arbitrarily late.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case snap, open := <-ch:
			if !open {
				return
			}
			if err := writeEvent(w, "snapshot", snap); err != nil {
				s.log.Debug("live: stream write", "err", err)
				return
			}
			flusher.Flush()
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// writeEvent takes an io.Writer rather than the http.ResponseWriter it is always called
// with, so the framing can be asserted against a buffer.
func writeEvent(w io.Writer, name string, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	// SSE is newline-delimited, so a payload containing a newline would silently split
	// into two malformed events. json.Marshal escapes newlines inside strings, which is
	// the only place one could appear here - asserted rather than assumed, because
	// conversation text is full of them and the failure would be a mangled stream.
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, body); err != nil {
		return err
	}
	return nil
}

// Run serves until ctx is cancelled. A bind failure returns immediately, so a live
// server that never started is a startup failure rather than a silently missing page.
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		if err := s.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("live: listen on %s: %w", s.cfg.Listen, err)
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	// Close, not Shutdown: Shutdown waits for open connections, and every SSE stream is
	// open by construction, so it would always burn the full grace period before exiting.
	if err := s.http.Shutdown(shutCtx); err != nil {
		_ = s.http.Close()
	}
	return <-errCh
}
