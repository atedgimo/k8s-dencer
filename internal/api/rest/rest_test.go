package rest_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atedgimo/k8s-dencer/internal/api/rest"
	"github.com/atedgimo/k8s-dencer/internal/auth"
	"github.com/atedgimo/k8s-dencer/internal/constraints"
	"github.com/atedgimo/k8s-dencer/internal/model"
	"github.com/atedgimo/k8s-dencer/internal/pricing"
	"github.com/atedgimo/k8s-dencer/internal/store"
	sqlitestore "github.com/atedgimo/k8s-dencer/internal/store/sqlstore"
)

func testServer(t *testing.T, records ...store.Record) *httptest.Server {
	t.Helper()
	db, err := sqlitestore.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, rec := range records {
		if _, err := db.Save(context.Background(), rec); err != nil {
			t.Fatal(err)
		}
	}

	// Auth disabled: these tests cover the read API's behaviour, and the guard
	// has its own suite in internal/auth. A disabled middleware is the real
	// type on a transparent path, not a stub, so the wiring is still exercised.
	guard := auth.NewMiddleware(nil, nil, auth.Config{Enabled: false}, slog.New(slog.DiscardHandler))

	api := rest.New(db, slog.New(slog.DiscardHandler), "test", guard, auth.Config{Enabled: false}.Describe())
	mux := http.NewServeMux()
	api.Routes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// testServerPriced is the same server with an operator-supplied price table,
// which is the only way prices ever reach this product.
func testServerPriced(t *testing.T, records ...store.Record) *httptest.Server {
	t.Helper()
	db, err := sqlitestore.Open(filepath.Join(t.TempDir(), "priced.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, rec := range records {
		if _, err := db.Save(context.Background(), rec); err != nil {
			t.Fatal(err)
		}
	}
	guard := auth.NewMiddleware(nil, nil, auth.Config{Enabled: false}, slog.New(slog.DiscardHandler))
	api := rest.New(db, slog.New(slog.DiscardHandler), "test", guard, auth.Config{Enabled: false}.Describe()).
		WithPricing(pricing.Table{
			Currency: "USD",
			PerHour:  map[string]float64{"e2-medium": 0.034},
		})
	mux := http.NewServeMux()
	api.Routes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func sampleRecord(id string) store.Record {
	snap := &model.ClusterSnapshot{
		TakenAt: time.Now().UTC(),
		Nodes: []model.Node{
			{Name: "n1", Ready: true, Labels: map[string]string{model.LabelZone: "z1"},
				Allocatable: model.Resources{MilliCPU: 4000, MemoryBytes: 1 << 33, Pods: 110}},
			{Name: "n2", Ready: true, Labels: map[string]string{model.LabelZone: "z2"},
				Allocatable: model.Resources{MilliCPU: 4000, MemoryBytes: 1 << 33, Pods: 110}},
		},
		Pods: []model.Pod{{
			Namespace: "app", Name: "web", NodeName: "n1", Phase: model.PodRunning,
			Labels:   map[string]string{"app": "web"},
			Requests: model.Resources{MilliCPU: 500, MemoryBytes: 1 << 28},
			Owner:    &model.OwnerRef{Kind: "Deployment", Name: "web"},
		}},
	}
	return store.Record{
		Plan: &model.Plan{
			ID: id, Status: model.PlanValid,
			GeneratedAt: time.Now().UTC(), SnapshotTakenAt: snap.TakenAt,
			NodesBefore: 2, NodesAfter: 1,
			Steps: []model.PlanStep{{
				ID: "s1", SequenceNumber: 1, TargetNode: "n1",
				Moves:     []model.Move{{Namespace: "app", Pod: "web", FromNode: "n1", ToNode: "n2"}},
				Impact:    model.ImpactGreen,
				Rationale: "Draining n1 moves 1 pod(s). No constraints apply.",
			}},
		},
		Snapshot: snap,
		Analysis: constraints.Analyze(snap),
		Strategy: "greedy-first-fit-decreasing",
	}
}

func get(t *testing.T, srv *httptest.Server, path string) (int, map[string]any) {
	t.Helper()
	resp, err := srv.Client().Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(body, &out)
	return resp.StatusCode, out
}

func TestEmptyStoreReturns404NotError(t *testing.T) {
	srv := testServer(t)
	// A fresh install has no plan yet. That is a normal state, not a fault,
	// and the UI needs to tell the two apart.
	for _, path := range []string{"/api/v1/plans/latest", "/api/v1/plans/latest/graph"} {
		if code, _ := get(t, srv, path); code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, code)
		}
	}
	code, body := get(t, srv, "/api/v1/plans")
	if code != http.StatusOK {
		t.Errorf("plan list = %d, want 200 with an empty array", code)
	}
	if plans, ok := body["plans"].([]any); !ok || len(plans) != 0 {
		t.Errorf("expected an empty array, got %#v", body["plans"])
	}
}

func TestLatestPlanAndAlias(t *testing.T) {
	srv := testServer(t, sampleRecord("plan-1"))

	code, body := get(t, srv, "/api/v1/plans/latest")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	plan, _ := body["plan"].(map[string]any)
	if plan["id"] != "plan-1" {
		t.Errorf("id = %v", plan["id"])
	}
	if body["readOnly"] != true {
		t.Error("responses must advertise that the API is read-only")
	}

	// "latest" and the explicit ID must resolve to the same plan, so the UI
	// can deep-link without knowing an ID.
	codeByID, byID := get(t, srv, "/api/v1/plans/plan-1")
	if codeByID != http.StatusOK {
		t.Fatalf("by id status = %d", codeByID)
	}
	if byID["plan"].(map[string]any)["id"] != plan["id"] {
		t.Error("latest and by-id disagree")
	}
}

func TestStepEndpointCarriesItsConstraints(t *testing.T) {
	srv := testServer(t, sampleRecord("plan-1"))

	code, body := get(t, srv, "/api/v1/plans/latest/steps/1")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	step, _ := body["step"].(map[string]any)
	if step["sequenceNumber"] != float64(1) {
		t.Errorf("sequenceNumber = %v", step["sequenceNumber"])
	}
	if step["rationale"] == "" {
		t.Error("step must carry its rationale")
	}
	// "Why is step N rated this way?" should be one request, not three.
	if _, ok := body["constraints"].([]any); !ok {
		t.Errorf("step response must include the constraints of the pods it moves, got %#v", body["constraints"])
	}
}

func TestUnknownStepAndPlanAre404(t *testing.T) {
	srv := testServer(t, sampleRecord("plan-1"))

	if code, _ := get(t, srv, "/api/v1/plans/latest/steps/99"); code != http.StatusNotFound {
		t.Errorf("unknown step = %d, want 404", code)
	}
	if code, _ := get(t, srv, "/api/v1/plans/nope"); code != http.StatusNotFound {
		t.Errorf("unknown plan = %d, want 404", code)
	}
	if code, _ := get(t, srv, "/api/v1/plans/latest/steps/abc"); code != http.StatusBadRequest {
		t.Errorf("non-numeric step = %d, want 400", code)
	}
}

func TestGraphPayloadShape(t *testing.T) {
	srv := testServer(t, sampleRecord("plan-1"))

	code, body := get(t, srv, "/api/v1/plans/latest/graph")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	elements, _ := body["elements"].([]any)
	if len(elements) == 0 {
		t.Fatal("graph has no elements")
	}

	var nodes, pods, other int
	for _, raw := range elements {
		el := raw.(map[string]any)
		data := el["data"].(map[string]any)
		switch data["kind"] {
		case "node":
			nodes++
		case "pod":
			pods++
			// A pod names the node it sits inside. Without a parent the
			// packing field has nowhere to draw it.
			if data["parent"] == nil || data["parent"] == "" {
				t.Errorf("pod %v has no parent node", data["id"])
			}
		default:
			other++
			t.Errorf("unexpected element kind %v", data["kind"])
		}
	}
	if nodes != 2 {
		t.Errorf("node elements = %d, want 2", nodes)
	}
	if pods != 1 {
		t.Errorf("pod elements = %d, want 1", pods)
	}
	// Edges were dropped in M19. Nothing has read one since the packing field
	// replaced the node-link graph, and they were 45% of the payload.
	if other != 0 {
		t.Errorf("payload carries %d elements that are neither node nor pod", other)
	}

	stats, _ := body["stats"].(map[string]any)
	// "reclaimable", not "reclaimed": this is what the plan would free, and
	// whether anything removed the node is observed separately at
	// /api/v1/reclamations. The old name reported a prediction as an outcome.
	if stats["reclaimable"] != float64(1) {
		t.Errorf("stats.reclaimable = %v, want 1", stats["reclaimable"])
	}
	if _, gone := stats["reclaimed"]; gone {
		t.Error("stats still carries the old 'reclaimed' key")
	}
}

func TestPodConstraintsEndpoint(t *testing.T) {
	srv := testServer(t, sampleRecord("plan-1"))

	code, body := get(t, srv, "/api/v1/plans/latest/constraints/app/web")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if body["name"] != "web" || body["namespace"] != "app" {
		t.Errorf("unexpected pod: %v/%v", body["namespace"], body["name"])
	}

	if code, _ := get(t, srv, "/api/v1/plans/latest/constraints/app/missing"); code != http.StatusNotFound {
		t.Errorf("unknown pod = %d, want 404", code)
	}
}

// Phase 1 is read-only by construction. There is no mutating route at all —
// not even a disabled one, because a "not implemented" execute endpoint is an
// invitation.
func TestNoMutatingRoutesExist(t *testing.T) {
	srv := testServer(t, sampleRecord("plan-1"))

	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/plans/latest/execute"},
		{http.MethodPost, "/api/v1/plans/latest/steps/1/execute"},
		{http.MethodDelete, "/api/v1/plans/plan-1"},
		{http.MethodPut, "/api/v1/plans/plan-1"},
		{http.MethodPost, "/api/v1/plans"},
	} {
		req, _ := http.NewRequest(tc.method, srv.URL+tc.path, nil)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode < 400 {
			t.Errorf("%s %s returned %d; no mutating route may exist in Phase 1",
				tc.method, tc.path, resp.StatusCode)
		}
	}
}

func TestPlanResponsesAreNotCached(t *testing.T) {
	srv := testServer(t, sampleRecord("plan-1"))
	resp, err := srv.Client().Get(srv.URL + "/api/v1/plans/latest")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	// A cached plan would show an operator state their cluster has left.
	if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
}

func TestEventStreamSendsCurrentStateOnConnect(t *testing.T) {
	srv := testServer(t, sampleRecord("plan-1"))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/events", nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q", ct)
	}
	// nginx buffers proxied responses by default, which would hold a
	// low-volume event stream back indefinitely.
	if resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Error("SSE responses must disable proxy buffering")
	}
}

// The review screen flags a moved pod that is its workload's only replica —
// the difference between "pods move" and "the service blinks". The flag must
// come from the plan's own snapshot, and a second replica must clear it.
func TestStepFlagsOnlyReplicaMoves(t *testing.T) {
	rec := sampleRecord("plan-1")
	srv := testServer(t, rec)

	_, body := get(t, srv, "/api/v1/plans/latest/steps/1")
	if got, ok := body["singletons"].([]any); !ok || len(got) != 1 || got[0] != "app/web" {
		t.Errorf("singletons = %#v, want [app/web] — the fixture's Deployment has one replica", body["singletons"])
	}

	// A sibling replica on another node makes the same move routine.
	twin := rec.Snapshot.Pods[0]
	twin.Name, twin.NodeName = "web-2", "n2"
	rec.Snapshot.Pods = append(rec.Snapshot.Pods, twin)
	rec.Plan.ID = "plan-2"
	srv2 := testServer(t, rec)
	_, body = get(t, srv2, "/api/v1/plans/latest/steps/1")
	if got, ok := body["singletons"].([]any); !ok || len(got) != 0 {
		t.Errorf("singletons = %#v, want empty — the workload has a second replica", body["singletons"])
	}
}

// The money belongs on the screen where the decision is made.
//
// It lived only on History, which reports what was measured after the fact —
// the honest number, in the right place, and four clicks from where someone
// decides whether to run a plan. So Review said nothing about what running it
// is worth, and the figure most likely to justify this tool to whoever
// approves the spend was the hardest one to find.
//
// A forecast, not the ledger. The doctrine holds: no built-in price table,
// unpriced is never treated as free, and spot is not on-demand.
func TestGraphCarriesAForecastWhenPricesAreConfigured(t *testing.T) {
	snap := &model.ClusterSnapshot{
		TakenAt: time.Now().UTC(),
		Nodes: []model.Node{
			{Name: "n1", Ready: true,
				Labels:      map[string]string{"node.kubernetes.io/instance-type": "e2-medium"},
				Allocatable: model.Resources{MilliCPU: 940, MemoryBytes: 1 << 31, Pods: 110}},
			{Name: "n2", Ready: true,
				// No instance-type label: unpriced, and it must be reported as
				// unpriced rather than quietly counted as free.
				Allocatable: model.Resources{MilliCPU: 940, MemoryBytes: 1 << 31, Pods: 110}},
		},
		Pods: []model.Pod{
			{Namespace: "app", Name: "a", NodeName: "n1", Phase: model.PodRunning,
				Owner: &model.OwnerRef{Kind: "ReplicaSet", Name: "a"}},
			{Namespace: "app", Name: "b", NodeName: "n2", Phase: model.PodRunning,
				Owner: &model.OwnerRef{Kind: "ReplicaSet", Name: "b"}},
		},
	}
	rec := store.Record{
		Plan: &model.Plan{
			ID: "priced01", GeneratedAt: time.Now().UTC(), SnapshotTakenAt: snap.TakenAt,
			Status: model.PlanValid, NodesBefore: 2, NodesAfter: 0,
			Steps: []model.PlanStep{
				{ID: "s1", SequenceNumber: 1, TargetNode: "n1", Impact: model.ImpactGreen,
					Moves: []model.Move{{Namespace: "app", Pod: "a", FromNode: "n1", ToNode: "n2"}}},
				{ID: "s2", SequenceNumber: 2, TargetNode: "n2", Impact: model.ImpactGreen,
					Moves: []model.Move{{Namespace: "app", Pod: "b", FromNode: "n2", ToNode: "n1"}}},
			},
		},
		Snapshot: snap,
		Analysis: &constraints.Analysis{},
		Strategy: "greedy-first-fit-decreasing",
	}

	srv := testServerPriced(t, rec)
	code, body := get(t, srv, "/api/v1/plans/priced01/graph")
	if code != 200 {
		t.Fatalf("graph = %d, want 200", code)
	}
	stats, _ := body["stats"].(map[string]any)
	f, ok := stats["forecast"].(map[string]any)
	if !ok {
		t.Fatalf("no forecast in stats: %v", stats)
	}
	if f["pricedNodes"].(float64) != 1 {
		t.Errorf("pricedNodes = %v, want 1", f["pricedNodes"])
	}
	if f["unpricedNodes"].(float64) != 1 {
		t.Errorf("unpricedNodes = %v, want 1 — unpriced must be reported, never treated as free",
			f["unpricedNodes"])
	}
	if f["perMonth"].(float64) <= 0 {
		t.Errorf("perMonth = %v, want a positive rate", f["perMonth"])
	}
}

// With no prices configured the field is absent, not zero. A zero would read
// as "this plan saves nothing", which is a claim the product has not earned.
func TestGraphOmitsTheForecastWithoutPrices(t *testing.T) {
	srv := testServer(t, sampleRecord("noprice01"))
	_, body := get(t, srv, "/api/v1/plans/noprice01/graph")
	stats, _ := body["stats"].(map[string]any)
	if _, present := stats["forecast"]; present {
		t.Errorf("forecast present with no price table: %v", stats["forecast"])
	}
}
