// Package httpserver provides the health-probe HTTP server shared by the
// k8s-dencer components, along with graceful shutdown on SIGTERM.
package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"
)

// Health tracks a component's readiness. Liveness is implicit: if the process
// is serving, it is alive. Readiness is flipped by the component once its
// dependencies (informer caches, database migrations) are satisfied.
type Health struct {
	ready atomic.Bool
}

// SetReady marks the component ready to serve traffic.
func (h *Health) SetReady(ready bool) { h.ready.Store(ready) }

// IsReady reports whether the component has declared itself ready.
func (h *Health) IsReady() bool { return h.ready.Load() }

// Register installs /healthz, /readyz and /startupz on mux.
//
// Startup and liveness share a handler on purpose: the kubelet uses the startup
// probe to grant a longer grace period before liveness begins, so both need to
// answer the same "is the process up" question.
func (h *Health) Register(mux *http.ServeMux) {
	alive := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}
	mux.HandleFunc("GET /healthz", alive)
	mux.HandleFunc("GET /startupz", alive)
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !h.ready.Load() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})
}

// Run serves mux on addr until ctx is cancelled, then drains in-flight requests
// within the shutdown grace period. It returns nil on a clean shutdown.
func Run(ctx context.Context, log *slog.Logger, addr string, mux *http.ServeMux) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("http server listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutdown signal received, draining")
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}
