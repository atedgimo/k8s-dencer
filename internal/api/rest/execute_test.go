package rest_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atedgimo/k8s-dencer/internal/api/rest"
	"github.com/atedgimo/k8s-dencer/internal/auth"
	"github.com/atedgimo/k8s-dencer/internal/model"
	"github.com/atedgimo/k8s-dencer/internal/store"
	sqlitestore "github.com/atedgimo/k8s-dencer/internal/store/sqlite"
)

// denyExecute authorizes reads and refuses execution, so the two permissions
// can be shown to be genuinely separate rather than one "authenticated" check.
type denyExecute struct{ identity auth.Identity }

func (d denyExecute) Require(res auth.Resource, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if res == auth.ExecuteConsolidations {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"code":"forbidden","error":"may not create consolidations.dencer.io"}`))
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), d.identity)))
	})
}

// allowAll stands in for a caller holding both permissions.
type allowAll struct{ identity auth.Identity }

func (a allowAll) Require(_ auth.Resource, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), a.identity)))
	})
}

func executableServer(t *testing.T, guard rest.Guard, steps ...model.PlanStep) (*httptest.Server, *sqlitestore.Store) {
	t.Helper()
	db, err := sqlitestore.Open(filepath.Join(t.TempDir(), "exec-api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}

	rec := sampleRecord("plan-1")
	rec.Plan.Steps = steps
	if _, err := db.Save(context.Background(), rec); err != nil {
		t.Fatal(err)
	}

	api := rest.New(db, slog.New(slog.DiscardHandler), "test", guard,
		auth.Config{Enabled: true}.Describe()).WithExecution(db)
	mux := http.NewServeMux()
	api.Routes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, db
}

func planStep(seq int, node string, impact model.ImpactRating) model.PlanStep {
	return model.PlanStep{
		ID: "s", SequenceNumber: seq, TargetNode: node, Impact: impact,
		Rationale: "rationale for step",
		Moves:     []model.Move{{Namespace: "app", Pod: "web", FromNode: node, ToNode: "other"}},
	}
}

func postExecute(t *testing.T, srv *httptest.Server, body string) (*http.Response, map[string]any) {
	t.Helper()
	res, err := http.Post(srv.URL+"/api/v1/plans/plan-1/execute", "application/json",
		bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	var parsed map[string]any
	_ = json.NewDecoder(res.Body).Decode(&parsed)
	return res, parsed
}

var operator = auth.Identity{Username: "alice@example.com", Groups: []string{"oidc:sre"}, Source: auth.SourceToken}

// Reading a plan and draining a node are different grants. A caller with the
// first must not get the second.
func TestExecuteRequiresItsOwnPermission(t *testing.T) {
	srv, _ := executableServer(t, denyExecute{operator}, planStep(1, "a", model.ImpactGreen))

	res, body := postExecute(t, srv, `{"steps":[1]}`)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("got %d, want 403", res.StatusCode)
	}
	if body["code"] != "forbidden" {
		t.Errorf("unexpected body: %v", body)
	}

	// And the read path still works for the same caller.
	readRes, err := http.Get(srv.URL + "/api/v1/plans/latest")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = readRes.Body.Close() }()
	if readRes.StatusCode != http.StatusOK {
		t.Errorf("read denied too: %d", readRes.StatusCode)
	}
}

func TestExecuteQueuesARunAndRecordsTheRequester(t *testing.T) {
	srv, db := executableServer(t, allowAll{operator},
		planStep(1, "a", model.ImpactGreen), planStep(2, "b", model.ImpactYellow))

	res, body := postExecute(t, srv, `{"steps":[2,1]}`)
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("got %d, want 202: %v", res.StatusCode, body)
	}

	runID, _ := body["runId"].(string)
	run, err := db.RunByID(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != store.RunPending {
		t.Errorf("status = %s, want Pending", run.Status)
	}
	// Steps are ordered ascending whatever order they arrived in: later steps
	// assume earlier ones freed capacity.
	if len(run.Steps) != 2 || run.Steps[0] != 1 || run.Steps[1] != 2 {
		t.Errorf("steps not normalised to ascending order: %v", run.Steps)
	}
	// The audit trail must name a person, not the service account.
	if run.Actor != "alice@example.com" || len(run.ActorGroups) != 1 {
		t.Errorf("requester not recorded: %+v", run)
	}
}

// The API must NOT judge Red.
//
// It used to refuse Red at admission as a courtesy. Once maintenance windows
// existed that became a false negative — refusing steps a window had
// legitimately authorised — because whether Red may run depends on live window
// state evaluated against the step's own node, and only the Safety Guard
// inside the executor knows both.
//
// So Red is queued here and adjudicated there. One authority rather than two
// that can disagree, which is what doc §9 asks for.
func TestExecuteDoesNotAdjudicateRed(t *testing.T) {
	srv, db := executableServer(t, allowAll{operator},
		planStep(1, "a", model.ImpactGreen), planStep(2, "b", model.ImpactRed))

	res, body := postExecute(t, srv, `{"steps":[1,2]}`)
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("got %d, want 202 — the guard decides about Red, not this route: %v",
			res.StatusCode, body)
	}

	run, err := db.RunByID(context.Background(), body["runId"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Steps) != 2 {
		t.Errorf("the Red step was dropped rather than queued: %v", run.Steps)
	}
}

func TestExecuteValidatesTheRequest(t *testing.T) {
	srv, _ := executableServer(t, allowAll{operator}, planStep(1, "a", model.ImpactGreen))

	for _, tc := range []struct{ name, body, want string }{
		{"no steps", `{"steps":[]}`, "no steps requested"},
		{"unknown step", `{"steps":[42]}`, "no step 42"},
		{"malformed", `not json`, "not valid JSON"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, body := postExecute(t, srv, tc.body)
			if res.StatusCode != http.StatusBadRequest {
				t.Fatalf("got %d, want 400", res.StatusCode)
			}
			if msg, _ := body["error"].(string); !strings.Contains(msg, tc.want) {
				t.Errorf("error %q does not mention %q", msg, tc.want)
			}
		})
	}
}

// Two consolidations in flight would each invalidate the other's placement
// decisions.
func TestOnlyOneRunMayBeInFlight(t *testing.T) {
	srv, _ := executableServer(t, allowAll{operator},
		planStep(1, "a", model.ImpactGreen), planStep(2, "b", model.ImpactGreen))

	if res, _ := postExecute(t, srv, `{"steps":[1]}`); res.StatusCode != http.StatusAccepted {
		t.Fatalf("first request got %d", res.StatusCode)
	}
	res, body := postExecute(t, srv, `{"steps":[2]}`)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("second request got %d, want 409", res.StatusCode)
	}
	if body["code"] != "run_in_progress" || body["runId"] == nil {
		t.Errorf("conflict should identify the run in progress: %v", body)
	}
}

func TestDryRunIsCarriedThrough(t *testing.T) {
	srv, db := executableServer(t, allowAll{operator}, planStep(1, "a", model.ImpactGreen))

	_, body := postExecute(t, srv, `{"steps":[1],"dryRun":true}`)
	run, err := db.RunByID(context.Background(), body["runId"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if !run.DryRun {
		t.Error("dryRun was dropped between the request and the queue")
	}
}

// An install without an executor must have no working execute path at all,
// rather than one that queues work nothing will ever claim.
func TestExecuteIsNotImplementedWithoutAnExecutor(t *testing.T) {
	db, err := sqlitestore.Open(filepath.Join(t.TempDir(), "no-exec.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	rec := sampleRecord("plan-1")
	rec.Plan.Steps = []model.PlanStep{planStep(1, "a", model.ImpactGreen)}
	if _, err := db.Save(context.Background(), rec); err != nil {
		t.Fatal(err)
	}

	// Note the missing WithExecution.
	api := rest.New(db, slog.New(slog.DiscardHandler), "test", allowAll{operator},
		auth.Config{Enabled: true}.Describe())
	mux := http.NewServeMux()
	api.Routes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	res, _ := postExecute(t, srv, `{"steps":[1]}`)
	if res.StatusCode != http.StatusNotImplemented {
		t.Errorf("got %d, want 501", res.StatusCode)
	}
}
