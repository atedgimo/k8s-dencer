package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func probeReady(t *testing.T, h *Health) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	h.Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	return rec
}

// Readiness used to be a latch: set once at startup and never asked again. A
// ui-backend that migrated its database successfully and then had the schema
// disappear underneath it stayed Ready and served "relation does not exist" to
// every request, indefinitely, because nothing ever asked the store a question
// a second time.
func TestReadinessReflectsTheDependencyNotJustTheLatch(t *testing.T) {
	var h Health
	broken := errors.New(`relation "runs" does not exist`)
	fail := false
	h.AddCheck("plan store", func(context.Context) error {
		if fail {
			return broken
		}
		return nil
	})
	h.SetReady(true)

	if rec := probeReady(t, &h); rec.Code != http.StatusOK {
		t.Fatalf("healthy store reported not ready: %d %s", rec.Code, rec.Body)
	}

	fail = true
	rec := probeReady(t, &h)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("store is unusable and the pod still reports ready: %d", rec.Code)
	}
	// The operator reads this in `kubectl describe`, so it has to say which
	// dependency and why.
	for _, want := range []string{"plan store", "relation"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("readiness failure does not say %q: %s", want, rec.Body.String())
		}
	}

	// And it recovers on its own once the dependency does, without a restart.
	fail = false
	if rec := probeReady(t, &h); rec.Code != http.StatusOK {
		t.Errorf("readiness did not recover with the store: %d", rec.Code)
	}
}

// The latch still comes first: a component still starting up is not ready
// however healthy its dependencies are.
func TestUnstartedComponentIsNotReadyEvenWithHealthyChecks(t *testing.T) {
	var h Health
	h.AddCheck("plan store", func(context.Context) error { return nil })

	if rec := probeReady(t, &h); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("a component that never called SetReady reports ready: %d", rec.Code)
	}
}

// Liveness must not follow the dependency. A probe that restarts the container
// when the database blips turns a small outage into every pod restarting at
// once.
func TestLivenessIgnoresDependencies(t *testing.T) {
	var h Health
	h.AddCheck("plan store", func(context.Context) error { return errors.New("down") })
	h.SetReady(true)

	mux := http.NewServeMux()
	h.Register(mux)
	for _, path := range []string{"/healthz", "/startupz"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s failed because a dependency is down (%d); that restarts the container", path, rec.Code)
		}
	}
}

// A component with no dependencies registered behaves exactly as before.
func TestNoChecksIsTheOldBehaviour(t *testing.T) {
	var h Health
	h.SetReady(true)
	if rec := probeReady(t, &h); rec.Code != http.StatusOK {
		t.Errorf("a component with no checks is no longer ready: %d", rec.Code)
	}
}
