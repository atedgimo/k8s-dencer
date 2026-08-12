package agenttools_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/atedgimo/k8s-dencer/internal/api/agenttools"
	"github.com/atedgimo/k8s-dencer/internal/constraints"
	"github.com/atedgimo/k8s-dencer/internal/model"
	"github.com/atedgimo/k8s-dencer/internal/store"
	sqlitestore "github.com/atedgimo/k8s-dencer/internal/store/sqlstore"
)

// connect starts the MCP endpoint over HTTP and returns a real MCP client
// session. Driving the actual protocol rather than calling handlers directly
// is the point: a protocol mistake here shows up as Kagent silently failing to
// connect, which a handler-level test would never catch.
func connect(t *testing.T, records ...store.Record) *mcp.ClientSession {
	t.Helper()

	db, err := sqlitestore.Open(filepath.Join(t.TempDir(), "agent.db"))
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

	srv := httptest.NewServer(agenttools.New(db, slog.New(slog.DiscardHandler), "test").Handler())
	t.Cleanup(srv.Close)

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(),
		&mcp.StreamableClientTransport{Endpoint: srv.URL}, nil)
	if err != nil {
		t.Fatalf("MCP connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func call(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String(), res.IsError
}

func sampleRecord() store.Record {
	snap := &model.ClusterSnapshot{
		TakenAt: time.Now().UTC(),
		Nodes: []model.Node{
			{Name: "worker-1", Ready: true, Labels: map[string]string{model.LabelZone: "z1"},
				Allocatable: model.Resources{MilliCPU: 4000, MemoryBytes: 1 << 33, Pods: 110}},
			{Name: "worker-2", Ready: true, Labels: map[string]string{model.LabelZone: "z2"},
				Allocatable: model.Resources{MilliCPU: 4000, MemoryBytes: 1 << 33, Pods: 110}},
			{Name: "control", Ready: true,
				Labels:      map[string]string{"node-role.kubernetes.io/control-plane": ""},
				Allocatable: model.Resources{MilliCPU: 4000, MemoryBytes: 1 << 33, Pods: 110}},
		},
		Pods: []model.Pod{
			{Namespace: "app", Name: "orphan", NodeName: "worker-1", Phase: model.PodRunning,
				Requests: model.Resources{MilliCPU: 500, MemoryBytes: 1 << 28}},
		},
	}
	return store.Record{
		Plan: &model.Plan{
			ID: "plan-1", Status: model.PlanValid,
			GeneratedAt: time.Now().UTC(), SnapshotTakenAt: snap.TakenAt,
			NodesBefore: 2, NodesAfter: 1,
			Steps: []model.PlanStep{{
				ID: "s1", SequenceNumber: 1, TargetNode: "worker-1",
				Moves:  []model.Move{{Namespace: "app", Pod: "orphan", FromNode: "worker-1", ToNode: "worker-2"}},
				Impact: model.ImpactRed,
				Rationale: "Draining worker-1 moves 1 pod(s). Rated Red because: app/orphan has " +
					"no controller. Evicting it deletes it permanently; nothing will recreate it. " +
					"Red steps may only execute inside an approved maintenance window.",
				Reasons: []model.ImpactReason{{Kind: "UnmanagedPod", Subject: "app/orphan",
					Detail: "app/orphan has no controller."}},
			}},
		},
		Snapshot: snap,
		Analysis: constraints.Analyze(snap),
		Strategy: "greedy-first-fit-decreasing",
	}
}

// The four tools are a contract with the Agent CR, which names them explicitly
// in toolNames. A rename that the chart doesn't follow leaves the agent with a
// tool it cannot call.
func TestExposesExactlyTheFourDocumentedTools(t *testing.T) {
	session := connect(t, sampleRecord())

	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, tool := range res.Tools {
		got[tool.Name] = true
		if tool.Description == "" {
			t.Errorf("tool %s has no description; the model selects tools by description", tool.Name)
		}
	}
	for _, want := range []string{"list_plan_steps", "explain_step", "get_node_constraints", "why_not_drained"} {
		if !got[want] {
			t.Errorf("missing tool %s", want)
		}
		delete(got, want)
	}
	for extra := range got {
		t.Errorf("unexpected tool %s — the surface must stay read-only and minimal", extra)
	}
}

func TestListPlanSteps(t *testing.T) {
	session := connect(t, sampleRecord())

	out, isErr := call(t, session, "list_plan_steps", nil)
	if isErr {
		t.Fatalf("unexpected error: %s", out)
	}
	var parsed struct {
		PlanID    string `json:"planId"`
		Reclaimed int    `json:"nodesReclaimed"`
		Red       int    `json:"redSteps"`
		ReadOnly  string `json:"readOnly"`
		Steps     []struct {
			Step       int    `json:"step"`
			Impact     string `json:"impact"`
			WindowOnly bool   `json:"maintenanceWindowOnly"`
		} `json:"steps"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("output is not the structured shape: %v\n%s", err, out)
	}
	if parsed.PlanID != "plan-1" || parsed.Reclaimed != 1 || parsed.Red != 1 {
		t.Errorf("unexpected summary: %+v", parsed)
	}
	if len(parsed.Steps) != 1 || !parsed.Steps[0].WindowOnly {
		t.Errorf("Red step must be flagged maintenance-window-only: %+v", parsed.Steps)
	}
	// Every answer states the read-only guarantee, so an operator asking the
	// agent to drain something is told plainly that it cannot.
	if !strings.Contains(parsed.ReadOnly, "cannot drain") {
		t.Errorf("readOnly note missing or reworded: %q", parsed.ReadOnly)
	}
}

// The agent must answer with the classifier's exact words. If the tool
// paraphrased, the agent and the UI's inspector could describe the same step
// differently and an operator would not know which to trust.
func TestExplainStepQuotesTheClassifierVerbatim(t *testing.T) {
	rec := sampleRecord()
	session := connect(t, rec)

	out, isErr := call(t, session, "explain_step", map[string]any{"step": 1})
	if isErr {
		t.Fatalf("unexpected error: %s", out)
	}
	var parsed struct {
		Impact     string `json:"impact"`
		Rationale  string `json:"rationale"`
		WindowOnly bool   `json:"maintenanceWindowOnly"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Impact != "Red" || !parsed.WindowOnly {
		t.Errorf("rating lost: %+v", parsed)
	}
	if parsed.Rationale != rec.Plan.Steps[0].Rationale {
		t.Errorf("rationale was altered:\nstored: %q\nserved: %q",
			rec.Plan.Steps[0].Rationale, parsed.Rationale)
	}
}

// A wrong step number should teach the model the valid range rather than
// failing the call — it can then correct itself in the same conversation.
func TestUnknownStepReturnsAGuidingError(t *testing.T) {
	session := connect(t, sampleRecord())

	out, isErr := call(t, session, "explain_step", map[string]any{"step": 99})
	if !isErr {
		t.Fatal("expected a tool error for an unknown step")
	}
	if !strings.Contains(out, "no step 99") || !strings.Contains(out, "1 to 1") {
		t.Errorf("error should state the valid range, got: %q", out)
	}
}

func TestWhyNotDrainedAnswersEachCase(t *testing.T) {
	session := connect(t, sampleRecord())

	cases := []struct {
		node string
		want string
	}{
		{"worker-1", "IS being drained"},
		{"control", "control-plane"},
		{"worker-2", ""}, // a destination node; any coherent answer will do
	}
	for _, tc := range cases {
		t.Run(tc.node, func(t *testing.T) {
			out, isErr := call(t, session, "why_not_drained", map[string]any{"node": tc.node})
			if isErr {
				t.Fatalf("unexpected error: %s", out)
			}
			var parsed struct {
				Answer    string `json:"answer"`
				IsDrained bool   `json:"isDrainedByPlan"`
			}
			if err := json.Unmarshal([]byte(out), &parsed); err != nil {
				t.Fatal(err)
			}
			if len(parsed.Answer) < 30 {
				t.Errorf("answer too thin to be useful: %q", parsed.Answer)
			}
			if tc.want != "" && !strings.Contains(parsed.Answer, tc.want) {
				t.Errorf("answer for %s should mention %q, got: %q", tc.node, tc.want, parsed.Answer)
			}
		})
	}
}

// A guessed node name is the likeliest model mistake; the error names real
// nodes so the next attempt can succeed.
func TestUnknownNodeSuggestsRealNames(t *testing.T) {
	session := connect(t, sampleRecord())

	out, isErr := call(t, session, "get_node_constraints", map[string]any{"node": "nope"})
	if !isErr {
		t.Fatal("expected a tool error for an unknown node")
	}
	if !strings.Contains(out, "worker-1") {
		t.Errorf("error should list known node names, got: %q", out)
	}
}

// A fresh install has no plan. The agent should say so in words an operator
// understands, not surface a database error.
func TestEmptyStoreExplainsRatherThanFailing(t *testing.T) {
	session := connect(t)

	out, isErr := call(t, session, "list_plan_steps", nil)
	if !isErr {
		t.Fatal("expected a tool error when no plan exists")
	}
	if !strings.Contains(out, "No consolidation plan has been produced yet") {
		t.Errorf("unhelpful empty-store message: %q", out)
	}
	if strings.Contains(strings.ToLower(out), "sql") {
		t.Errorf("internal detail leaked to the model: %q", out)
	}
}

func TestGetNodeConstraintsReportsUtilisationAndDrainStep(t *testing.T) {
	session := connect(t, sampleRecord())

	out, isErr := call(t, session, "get_node_constraints", map[string]any{"node": "worker-1"})
	if isErr {
		t.Fatalf("unexpected error: %s", out)
	}
	var parsed struct {
		Node        string `json:"node"`
		Zone        string `json:"zone"`
		DrainedBy   int    `json:"drainedByStep"`
		Impact      string `json:"drainImpact"`
		Utilization string `json:"utilization"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Node != "worker-1" || parsed.Zone != "z1" {
		t.Errorf("node detail wrong: %+v", parsed)
	}
	if parsed.DrainedBy != 1 || parsed.Impact != "Red" {
		t.Errorf("drain step not linked: %+v", parsed)
	}
	if parsed.Utilization == "" {
		t.Error("utilisation missing")
	}
}
