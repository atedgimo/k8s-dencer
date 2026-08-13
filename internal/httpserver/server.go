// Package httpserver provides the health-probe HTTP server shared by the
// k8s-dencer components, along with graceful shutdown on SIGTERM.
package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Health tracks a component's readiness. Liveness is implicit: if the process
// is serving, it is alive. Readiness is flipped by the component once its
// dependencies (informer caches, database migrations) are satisfied.
type Health struct {
	ready atomic.Bool

	mu     sync.Mutex
	checks []namedCheck
}

type namedCheck struct {
	name string
	fn   func(context.Context) error
}

// AddCheck registers a dependency that must be usable for the component to be
// ready, consulted on every probe rather than latched once at startup.
//
// The latch alone was not enough. A ui-backend that migrated its database
// successfully and then had the schema disappear underneath it — a restore
// from before the current version, a failover to a lagging replica, a wrong
// database name — stayed Ready and served "internal error" to every request
// indefinitely, because nothing ever asked the store a question again.
//
// Deliberately readiness and not liveness. Liveness restarts the container,
// and a probe that restarts on a dependency failure turns a brief database
// blip into every pod restarting at once, which is the standard way to convert
// a small outage into a large one. Readiness takes the pod out of the Service
// so a healthy replica serves, and makes the failure visible as 0/1 Ready
// rather than as HTTP 500s an operator has to go looking for.
func (h *Health) AddCheck(name string, fn func(context.Context) error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checks = append(h.checks, namedCheck{name: name, fn: fn})
}

// probe runs every registered check, returning the first failure.
func (h *Health) probe(ctx context.Context) (string, error) {
	h.mu.Lock()
	checks := make([]namedCheck, len(h.checks))
	copy(checks, h.checks)
	h.mu.Unlock()

	for _, c := range checks {
		if err := c.fn(ctx); err != nil {
			return c.name, err
		}
	}
	return "", nil
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
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if !h.ready.Load() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		// Bounded, because a probe that hangs is reported as a failure only
		// after the kubelet's own timeout, and a database that has stopped
		// answering is exactly when this runs.
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if name, err := h.probe(ctx); err != nil {
			http.Error(w, "not ready: "+name+": "+err.Error(), http.StatusServiceUnavailable)
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
