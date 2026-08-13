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

// Readiness must NOT follow a dependency every replica shares.
//
// The tempting version of this fix failed /readyz when the store was
// unreachable. That takes every pod out of the Service at the same moment,
// leaving zero endpoints and a bare 503 from the ingress — "some requests fail
// with a message" becomes "nothing answers at all", and a rolling update can
// stall because no pod ever becomes Ready. The store's absence is reported by
// the API's own 503 and by /dependenciesz instead.
func TestReadinessIgnoresSharedDependencies(t *testing.T) {
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
	if rec := probeReady(t, &h); rec.Code != http.StatusOK {
		t.Errorf("a shared dependency failure took the pod out of the Service: %d %s", rec.Code, rec.Body)
	}
}

// The dependency is still reported — just somewhere the kubelet is not
// reading, so monitoring and a human with curl can both see it.
func TestDependenciesEndpointReportsTheFailure(t *testing.T) {
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

	probe := func() *httptest.ResponseRecorder {
		mux := http.NewServeMux()
		h.Register(mux)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dependenciesz", nil))
		return rec
	}

	if rec := probe(); rec.Code != http.StatusOK {
		t.Fatalf("healthy dependency reported down: %d %s", rec.Code, rec.Body)
	}

	fail = true
	rec := probe()
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("broken dependency reported healthy: %d", rec.Code)
	}
	for _, want := range []string{"plan store", "relation"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("report does not say %q: %s", want, rec.Body.String())
		}
	}

	fail = false
	if rec := probe(); rec.Code != http.StatusOK {
		t.Errorf("did not recover with the dependency: %d", rec.Code)
	}
}

// The latch still comes first: a component still starting up is not ready
// however healthy its dependencies are.
func TestUnstartedComponentIsNotReady(t *testing.T) {
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
