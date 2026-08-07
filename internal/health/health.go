// Package health serves the service's readiness probe and a config dump on
// cfg.Health.Listen, which defaults to loopback.
//
// The config dump is safe by construction rather than by care taken here:
// config.Secret's MarshalJSON always returns config.Mask, so serving the config
// struct as-is cannot leak a token no matter what shape it is in - a literal value,
// an ${ENV} reference, or a file: reference. Nothing in this package needs to know
// which fields are secret; it just has to not resolve one, and it never does.
package health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/rknightion/codexlb2otel/internal/config"
)

// shutdownGrace bounds how long a listener is given to drain in-flight requests
// once the caller stops the server. A probe request should never be why a shutdown
// hangs.
const shutdownGrace = 5 * time.Second

// Server answers readiness probes and reports the running configuration.
type Server struct {
	cfg   config.Config
	log   *slog.Logger
	ready atomic.Bool
	http  *http.Server
}

// response is the /healthz body. Ready is reported alongside the config dump rather
// than only as a status code, so a human hitting the endpoint in a browser does not
// have to separately curl -I to see why.
type response struct {
	Ready  bool          `json:"ready"`
	Config config.Config `json:"config"`
}

// New builds a Server. It does not start listening; call Run.
func New(cfg config.Config, log *slog.Logger) *Server {
	s := &Server{cfg: cfg, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handle)
	s.http = &http.Server{Addr: cfg.Health.Listen, Handler: mux}
	return s
}

// SetReady flips readiness. The caller drives this: true once the tail watcher has
// started, false again as shutdown begins - so a probe during a graceful drain
// reports not-ready rather than a stale "ok" for a service that has stopped polling.
func (s *Server) SetReady(ready bool) { s.ready.Store(ready) }

func (s *Server) handle(w http.ResponseWriter, _ *http.Request) {
	ready := s.ready.Load()
	body := response{Ready: ready, Config: s.cfg}
	w.Header().Set("Content-Type", "application/json")
	if !ready {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	// Encoding failure here would mean config.Config stopped being JSON-marshalable,
	// which every field's own json tag already guarantees - nothing left to do about
	// it at request time beyond logging.
	if err := json.NewEncoder(w).Encode(body); err != nil {
		s.log.Error("health: encode response", "err", err)
	}
}

// Run serves until ctx is cancelled, then shuts down within shutdownGrace. A bind
// failure (most likely the port already in use) is returned immediately rather than
// only logged, so main treats a health endpoint that never started as the startup
// failure it is.
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		if err := s.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("health: listen on %s: %w", s.cfg.Health.Listen, err)
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := s.http.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("health: shutdown: %w", err)
		}
		return <-errCh
	}
}
