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

// AddCheck registers a dependency reported by /dependenciesz.
//
// Explicitly NOT wired into /readyz, and the reasoning is worth keeping,
// because the obvious thing to do here is the wrong one.
//
// Readiness decides Service membership. Failing it on a dependency every
// replica shares means that when the database goes down, every pod goes
// NotReady at the same moment, the Service drops to zero endpoints, and the
// ingress returns a bare 503 with no explanation — turning "some requests fail
// with a message" into "nothing answers at all". It can also stall a rolling
// update, since no new pod ever becomes Ready.
//
// The failure this was written for — a store that has gone away — is therefore
// handled where it is actually visible: the API answers 503 naming the plan
// store instead of "internal error", so an operator reads the cause in the
// response rather than inferring it from an empty endpoint list. This endpoint
// exists for monitoring and for a human with curl, neither of which should be
// able to take the Service down by asking a question.
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
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !h.ready.Load() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})

	// Dependency state, for monitoring and for a human with curl. The kubelet
	// does not read this, so a database outage cannot empty the Service
	// through it.
	mux.HandleFunc("GET /dependenciesz", func(w http.ResponseWriter, r *http.Request) {
		// Bounded, because a dependency that has stopped answering is exactly
		// when this runs, and a hanging probe is worse than a failing one.
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if name, err := h.probe(ctx); err != nil {
			http.Error(w, name+": "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
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
