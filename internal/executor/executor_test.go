package executor_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atedgimo/k8s-dencer/internal/constraints"
	"github.com/atedgimo/k8s-dencer/internal/executor"
	"github.com/atedgimo/k8s-dencer/internal/model"
	"github.com/atedgimo/k8s-dencer/internal/safety"
	"github.com/atedgimo/k8s-dencer/internal/store"
	sqlitestore "github.com/atedgimo/k8s-dencer/internal/store/sqlite"
)

// fakeCluster is an in-memory stand-in that records every mutation in order.
//
// The ordering log is the point: "cordon strictly before the first eviction"
// and "uncordon after an abort" are sequencing guarantees, and only a recorded
// transcript can prove them.
type fakeCluster struct {
	mu    sync.Mutex
	snap  model.ClusterSnapshot
	calls []string

	// replaceOnEvict recreates an evicted pod on another node, the way a
	// ReplicaSet would. Off for tests that need a workload to stay down.
	replaceOnEvict bool
	// replacementReady controls whether the recreated pod passes its probes.
	// False models the case that matters on a real cluster: the pod starts,
	// reports Running, and never becomes Ready.
	replacementReady bool
	// evictErr fails eviction of a specific pod key.
	evictErr map[string]error
	// uncordonErr fails uncordon, to exercise the "needs manual repair" path.
	uncordonErr error
	replacement int
}

func (f *fakeCluster) Snapshot(context.Context) (*model.ClusterSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Deep-ish copy: the executor must not be able to mutate our state, and
	// aliasing would hide bugs where it does.
	out := model.ClusterSnapshot{
		Nodes: append([]model.Node(nil), f.snap.Nodes...),
		Pods:  append([]model.Pod(nil), f.snap.Pods...),
		PDBs:  append([]model.PodDisruptionBudget(nil), f.snap.PDBs...),
	}
	return &out, nil
}

func (f *fakeCluster) Cordon(_ context.Context, node string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "cordon:"+node)
	for i := range f.snap.Nodes {
		if f.snap.Nodes[i].Name == node {
			f.snap.Nodes[i].Unschedulable = true
		}
	}
	return nil
}

func (f *fakeCluster) Uncordon(_ context.Context, node string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "uncordon:"+node)
	if f.uncordonErr != nil {
		return f.uncordonErr
	}
	for i := range f.snap.Nodes {
		if f.snap.Nodes[i].Name == node {
			f.snap.Nodes[i].Unschedulable = false
		}
	}
	return nil
}

func (f *fakeCluster) Evict(_ context.Context, namespace, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := namespace + "/" + name
	f.calls = append(f.calls, "evict:"+key)
	if err, ok := f.evictErr[key]; ok {
		return err
	}

	var evicted *model.Pod
	kept := f.snap.Pods[:0]
	for _, p := range f.snap.Pods {
		if p.Namespace == namespace && p.Name == name {
			cp := p
			evicted = &cp
			continue
		}
		kept = append(kept, p)
	}
	f.snap.Pods = append([]model.Pod(nil), kept...)

	if evicted != nil && f.replaceOnEvict {
		// A controller recreates it elsewhere, under a new name.
		f.replacement++
		repl := *evicted
		repl.Name = fmt.Sprintf("%s-r%d", evicted.Name, f.replacement)
		repl.NodeName = f.someOtherSchedulableNode(evicted.NodeName)
		repl.Phase = model.PodRunning
		repl.Ready = f.replacementReady
		f.snap.Pods = append(f.snap.Pods, repl)
	}
	return nil
}

func (f *fakeCluster) someOtherSchedulableNode(exclude string) string {
	for _, n := range f.snap.Nodes {
		if n.Name != exclude && !n.Unschedulable && n.Ready {
			return n.Name
		}
	}
	return ""
}

func (f *fakeCluster) transcript() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeCluster) nodeCordoned(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, n := range f.snap.Nodes {
		if n.Name == name {
			return n.Unschedulable
		}
	}
	return false
}

// ---------------------------------------------------------------- fixtures

func testNode(name string) model.Node {
	return model.Node{
		Name: name, Ready: true,
		Labels:      map[string]string{model.LabelZone: "z1"},
		Allocatable: model.Resources{MilliCPU: 8000, MemoryBytes: 1 << 34, Pods: 110},
	}
}

func testPod(ns, name, node, owner string) model.Pod {
	return model.Pod{
		Namespace: ns, Name: name, NodeName: node, Phase: model.PodRunning,
		// A healthy cluster: Running and passing probes. The executor compares
		// readiness before and after, so a fixture where nothing is ever Ready
		// would make the recovery check vacuous.
		Ready:    true,
		Labels:   map[string]string{"app": owner},
		Requests: model.Resources{MilliCPU: 500, MemoryBytes: 1 << 28},
		Owner:    &model.OwnerRef{Kind: "ReplicaSet", Name: owner},
	}
}

type harness struct {
	cluster *fakeCluster
	db      *sqlitestore.Store
	exec    *executor.Executor
	planID  string
}

func newHarness(t *testing.T, snap model.ClusterSnapshot, steps []model.PlanStep, limits safety.Limits) *harness {
	t.Helper()
	// Deliberately leaves Readiness unset, so every existing test also asserts
	// that the default is the strict criterion.
	return newHarnessWithReadiness(t, snap, steps, limits, "")
}

func newHarnessWithReadiness(t *testing.T, snap model.ClusterSnapshot, steps []model.PlanStep,
	limits safety.Limits, readiness executor.Readiness) *harness {
	t.Helper()

	db, err := sqlitestore.Open(filepath.Join(t.TempDir(), "exec.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}

	plan := &model.Plan{
		ID: "plan-1", Status: model.PlanValid,
		GeneratedAt: time.Now().UTC(), SnapshotTakenAt: time.Now().UTC(),
		Steps: steps, NodesBefore: len(snap.Nodes), NodesAfter: len(snap.Nodes) - len(steps),
	}
	snapCopy := snap
	if _, err := db.Save(context.Background(), store.Record{
		Plan: plan, Snapshot: &snapCopy, Analysis: &constraints.Analysis{}, Strategy: "test",
	}); err != nil {
		t.Fatal(err)
	}

	cluster := &fakeCluster{
		snap: snap, replaceOnEvict: true, replacementReady: true,
		evictErr: map[string]error{},
	}
	return &harness{
		cluster: cluster,
		db:      db,
		planID:  plan.ID,
		exec: executor.New(cluster, db, db, slog.New(slog.DiscardHandler), executor.Options{
			Worker: "test-worker", Limits: limits,
			StepTimeout: 5 * time.Second, SettleTimeout: 2 * time.Second,
			PollInterval: time.Millisecond, Readiness: readiness,
		}),
	}
}

func (h *harness) request(t *testing.T, steps []int, dryRun bool) store.Run {
	t.Helper()
	ctx := context.Background()
	id, err := h.db.Enqueue(ctx, store.Run{
		PlanID: h.planID, Steps: steps, DryRun: dryRun, Actor: "alice@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.exec.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	run, err := h.db.RunByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func (h *harness) events(t *testing.T, runID string) []store.RunEvent {
	t.Helper()
	ev, err := h.db.Events(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

func step(seq int, node string, impact model.ImpactRating) model.PlanStep {
	return model.PlanStep{
		ID: fmt.Sprintf("s%d", seq), SequenceNumber: seq, TargetNode: node,
		Impact: impact, Rationale: "because the node is underused",
	}
}

func permissive() safety.Limits {
	return safety.Limits{MaxNodesPerRun: 10, MinReadyNodes: 0}
}

// ------------------------------------------------------------------- tests

func TestSuccessfulDrainCordonsThenEvictsThenVerifies(t *testing.T) {
	h := newHarness(t, model.ClusterSnapshot{
		Nodes: []model.Node{testNode("a"), testNode("b"), testNode("c")},
		Pods: []model.Pod{
			testPod("app", "web-1", "a", "web"),
			testPod("app", "web-2", "b", "web"),
		},
	}, []model.PlanStep{step(1, "a", model.ImpactGreen)}, permissive())

	run := h.request(t, []int{1}, false)
	if run.Status != store.RunSucceeded {
		t.Fatalf("status = %s (%s)", run.Status, run.Summary)
	}

	// Cordon must strictly precede the first eviction, or the scheduler can
	// put a new pod onto a node we are in the middle of emptying.
	got := h.cluster.transcript()
	want := []string{"cordon:a", "evict:app/web-1"}
	if len(got) != len(want) {
		t.Fatalf("transcript = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("transcript[%d] = %s, want %s", i, got[i], want[i])
		}
	}

	// A drained node stays cordoned: it is meant to be removed, and letting
	// the scheduler refill it would undo the work.
	if !h.cluster.nodeCordoned("a") {
		t.Error("node was uncordoned after a successful drain")
	}

	if !hasEvent(h.events(t, run.ID), "Verify") {
		t.Error("no Verify event; recovery was never confirmed")
	}
}

// The most important refusal in the product. Red must never reach a cordon.
func TestRedStepIsRefusedWithoutTouchingTheCluster(t *testing.T) {
	h := newHarness(t, model.ClusterSnapshot{
		Nodes: []model.Node{testNode("a"), testNode("b")},
		Pods:  []model.Pod{testPod("app", "web-1", "a", "web")},
	}, []model.PlanStep{step(1, "a", model.ImpactRed)}, permissive())

	run := h.request(t, []int{1}, false)

	if run.Status != store.RunBlocked {
		t.Errorf("status = %s, want Blocked", run.Status)
	}
	if calls := h.cluster.transcript(); len(calls) != 0 {
		t.Errorf("a Red step touched the cluster: %v", calls)
	}

	events := h.events(t, run.ID)
	blocked := findEvent(events, "Guard")
	if blocked == nil {
		t.Fatal("no Guard event recorded")
	}
	if blocked.Rule != string(safety.RuleRedRequiresWindow) {
		t.Errorf("rule = %q, want RedRequiresWindow", blocked.Rule)
	}
	// The audit entry has to quote the classifier's own words.
	if !strings.Contains(blocked.Message, "because the node is underused") {
		t.Errorf("refusal lost the rationale: %s", blocked.Message)
	}
}

// A PDB that runs out of headroom part-way through must stop the drain and
// leave the node usable.
func TestPDBExhaustionMidDrainAbortsAndUncordons(t *testing.T) {
	h := newHarness(t, model.ClusterSnapshot{
		Nodes: []model.Node{testNode("a"), testNode("b")},
		Pods: []model.Pod{
			testPod("app", "web-1", "a", "web"),
			testPod("app", "db-1", "a", "db"),
		},
		PDBs: []model.PodDisruptionBudget{{
			Namespace: "app", Name: "db",
			Selector:           &model.LabelSelector{MatchLabels: map[string]string{"app": "db"}},
			DisruptionsAllowed: 0, CurrentHealthy: 1, DesiredHealthy: 1,
		}},
	}, []model.PlanStep{step(1, "a", model.ImpactGreen)}, permissive())

	run := h.request(t, []int{1}, false)

	if run.Status != store.RunBlocked {
		t.Fatalf("status = %s (%s), want Blocked", run.Status, run.Summary)
	}
	transcript := h.cluster.transcript()
	if transcript[0] != "cordon:a" {
		t.Errorf("expected a cordon first, got %v", transcript)
	}
	// The node must not be left cordoned after an abort — that would strand
	// capacity nobody asked to remove.
	if last := transcript[len(transcript)-1]; last != "uncordon:a" {
		t.Errorf("run aborted without uncordoning: %v", transcript)
	}
	if h.cluster.nodeCordoned("a") {
		t.Error("node left unschedulable after an abort")
	}

	if ev := findEvent(h.events(t, run.ID), "Guard"); ev == nil ||
		ev.Rule != string(safety.RulePDBHeadroom) {
		t.Errorf("PDB refusal not recorded with its rule: %+v", ev)
	}
}

// The abort message must be honest: evicted pods are gone for good.
func TestAbortSaysEvictionCannotBeUndone(t *testing.T) {
	h := newHarness(t, model.ClusterSnapshot{
		Nodes: []model.Node{testNode("a"), testNode("b")},
		Pods:  []model.Pod{testPod("app", "web-1", "a", "web")},
	}, []model.PlanStep{step(1, "a", model.ImpactGreen)}, permissive())
	h.cluster.evictErr["app/web-1"] = errors.New("apiserver said no")

	run := h.request(t, []int{1}, false)
	if run.Status != store.RunFailed {
		t.Fatalf("status = %s, want Failed", run.Status)
	}

	ev := findEvent(h.events(t, run.ID), "Uncordon")
	if ev == nil {
		t.Fatal("no Uncordon event after a failed eviction")
	}
	if !strings.Contains(ev.Message, "cannot be undone") {
		t.Errorf("abort message oversells recovery: %s", ev.Message)
	}
}

// If uncordon itself fails the operator must be told the node needs manual
// repair, rather than the failure being swallowed.
func TestFailedUncordonIsReportedAsNeedingManualRepair(t *testing.T) {
	h := newHarness(t, model.ClusterSnapshot{
		Nodes: []model.Node{testNode("a"), testNode("b")},
		Pods:  []model.Pod{testPod("app", "web-1", "a", "web")},
	}, []model.PlanStep{step(1, "a", model.ImpactGreen)}, permissive())
	h.cluster.evictErr["app/web-1"] = errors.New("apiserver said no")
	h.cluster.uncordonErr = errors.New("conflict")

	run := h.request(t, []int{1}, false)

	ev := findEvent(h.events(t, run.ID), "Uncordon")
	if ev == nil || ev.Level != store.EventError {
		t.Fatalf("a failed uncordon was not recorded as an error: %+v", ev)
	}
	if !strings.Contains(ev.Message, "kubectl uncordon a") {
		t.Errorf("message does not tell the operator how to fix it: %s", ev.Message)
	}
}

// The reality check. The guard predicts pods will fit; if they then do not
// come back, the run must stop rather than drain a second node.
func TestWorkloadThatDoesNotRecoverFailsTheStep(t *testing.T) {
	h := newHarness(t, model.ClusterSnapshot{
		Nodes: []model.Node{testNode("a"), testNode("b")},
		Pods:  []model.Pod{testPod("app", "web-1", "a", "web")},
	}, []model.PlanStep{step(1, "a", model.ImpactGreen)}, permissive())
	h.cluster.replaceOnEvict = false // nothing recreates the pod

	run := h.request(t, []int{1}, false)

	if run.Status != store.RunFailed {
		t.Fatalf("status = %s (%s), want Failed", run.Status, run.Summary)
	}
	if !strings.Contains(run.Summary, "did not recover") {
		t.Errorf("summary does not explain the failure: %s", run.Summary)
	}
	if h.cluster.nodeCordoned("a") {
		t.Error("node left cordoned after a failed verification")
	}
}

func TestDryRunTouchesNothingButEmitsTheSameEvents(t *testing.T) {
	h := newHarness(t, model.ClusterSnapshot{
		Nodes: []model.Node{testNode("a"), testNode("b")},
		Pods: []model.Pod{
			testPod("app", "web-1", "a", "web"),
			testPod("app", "web-2", "a", "web"),
		},
	}, []model.PlanStep{step(1, "a", model.ImpactGreen)}, permissive())

	run := h.request(t, []int{1}, true)

	if run.Status != store.RunSucceeded {
		t.Fatalf("status = %s (%s)", run.Status, run.Summary)
	}
	if calls := h.cluster.transcript(); len(calls) != 0 {
		t.Errorf("a dry run mutated the cluster: %v", calls)
	}

	// Same event shapes as a real run: the UI renders both with one component,
	// so a rehearsal must look like the thing it rehearses.
	events := h.events(t, run.ID)
	for _, action := range []string{"Cordon", "Evict", "Drained"} {
		if !hasEvent(events, action) {
			t.Errorf("dry run emitted no %s event", action)
		}
	}
	if ev := findEvent(events, "Evict"); ev != nil && !strings.Contains(ev.Message, "dry run") {
		t.Errorf("dry-run event is not labelled as one: %s", ev.Message)
	}
}

// A dry run must still be refused by the guard — rehearsing a Red step and
// being told it would succeed is worse than useless.
func TestDryRunStillHonoursTheGuard(t *testing.T) {
	h := newHarness(t, model.ClusterSnapshot{
		Nodes: []model.Node{testNode("a"), testNode("b")},
		Pods:  []model.Pod{testPod("app", "web-1", "a", "web")},
	}, []model.PlanStep{step(1, "a", model.ImpactRed)}, permissive())

	if run := h.request(t, []int{1}, true); run.Status != store.RunBlocked {
		t.Errorf("dry run of a Red step: status = %s, want Blocked", run.Status)
	}
}

// Partial execution, per doc §5: steps 1–3 of many, and a rail that trips
// part-way leaves the earlier steps done.
func TestRunStopsAtTheStepThatViolatesACapLeavingEarlierStepsDone(t *testing.T) {
	h := newHarness(t, model.ClusterSnapshot{
		Nodes: []model.Node{testNode("a"), testNode("b"), testNode("c"), testNode("d")},
		Pods: []model.Pod{
			testPod("app", "web-1", "a", "web"),
			testPod("app", "web-2", "b", "web"),
			testPod("app", "web-3", "c", "web"),
		},
	}, []model.PlanStep{
		step(1, "a", model.ImpactGreen),
		step(2, "b", model.ImpactGreen),
		step(3, "c", model.ImpactGreen),
	}, safety.Limits{MaxNodesPerRun: 2})

	run := h.request(t, []int{1, 2, 3}, false)

	if run.Status != store.RunBlocked {
		t.Fatalf("status = %s (%s), want Blocked", run.Status, run.Summary)
	}
	if !strings.Contains(run.Summary, "stopped at step 3") {
		t.Errorf("summary should name the step that stopped: %s", run.Summary)
	}
	// The first two really did happen.
	if !h.cluster.nodeCordoned("a") || !h.cluster.nodeCordoned("b") {
		t.Error("earlier steps were rolled back; they should stand")
	}
	if h.cluster.nodeCordoned("c") {
		t.Error("the blocked step touched its node")
	}
}

func TestUnknownStepFailsBeforeAnythingIsTouched(t *testing.T) {
	h := newHarness(t, model.ClusterSnapshot{
		Nodes: []model.Node{testNode("a"), testNode("b")},
	}, []model.PlanStep{step(1, "a", model.ImpactGreen)}, permissive())

	run := h.request(t, []int{99}, false)
	if run.Status != store.RunFailed {
		t.Errorf("status = %s, want Failed", run.Status)
	}
	if calls := h.cluster.transcript(); len(calls) != 0 {
		t.Errorf("an unknown step touched the cluster: %v", calls)
	}
}

func TestPollReportsNoWorkOnAnEmptyQueue(t *testing.T) {
	h := newHarness(t, model.ClusterSnapshot{Nodes: []model.Node{testNode("a")}},
		[]model.PlanStep{step(1, "a", model.ImpactGreen)}, permissive())

	worked, err := h.exec.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if worked {
		t.Error("Poll claimed work from an empty queue")
	}
}

// Every run records who asked for it, so an action stays attributable long
// after the token that authorized it has expired.
func TestAuditTrailNamesTheActor(t *testing.T) {
	h := newHarness(t, model.ClusterSnapshot{
		Nodes: []model.Node{testNode("a"), testNode("b")},
		Pods:  []model.Pod{testPod("app", "web-1", "a", "web")},
	}, []model.PlanStep{step(1, "a", model.ImpactGreen)}, permissive())

	run := h.request(t, []int{1}, false)
	claim := findEvent(h.events(t, run.ID), "Claim")
	if claim == nil || !strings.Contains(claim.Message, "alice@example.com") {
		t.Errorf("claim event does not name the requester: %+v", claim)
	}
	if run.Actor != "alice@example.com" {
		t.Errorf("run lost its actor: %s", run.Actor)
	}
}

func findEvent(events []store.RunEvent, action string) *store.RunEvent {
	for i := range events {
		if events[i].Action == action {
			return &events[i]
		}
	}
	return nil
}

func hasEvent(events []store.RunEvent, action string) bool {
	return findEvent(events, action) != nil
}

// The API server is the real PDB authority, and it can refuse an eviction that
// passed our pre-flight check — state moves between the two. That refusal must
// be recorded as a guard verdict and end the run as Blocked, not Failed.
func TestApiServerEvictionRefusalIsTreatedAsAGuardBlock(t *testing.T) {
	h := newHarness(t, model.ClusterSnapshot{
		Nodes: []model.Node{testNode("a"), testNode("b")},
		Pods:  []model.Pod{testPod("app", "web-1", "a", "web")},
	}, []model.PlanStep{step(1, "a", model.ImpactGreen)}, permissive())

	// No PDB in our snapshot, so the pre-flight check passes; the "API server"
	// refuses anyway, exactly as a 429 would.
	h.cluster.evictErr["app/web-1"] = &safety.Blocked{
		Rule:   safety.RulePDBHeadroom,
		Reason: "the API server refused to evict app/web-1: a PodDisruptionBudget has no headroom",
	}

	run := h.request(t, []int{1}, false)

	if run.Status != store.RunBlocked {
		t.Fatalf("status = %s (%s), want Blocked", run.Status, run.Summary)
	}
	ev := findEvent(h.events(t, run.ID), "Guard")
	if ev == nil || ev.Rule != string(safety.RulePDBHeadroom) {
		t.Errorf("API-server refusal not audited with its rule: %+v", ev)
	}
	if h.cluster.nodeCordoned("a") {
		t.Error("node left cordoned after an API-server refusal")
	}
}

// The gap M15 closes, and the only one in this product that can cause an
// outage rather than an inconvenience.
//
// A replacement pod that starts and then fails its probes is Running but not
// Ready. Verifying at Running would call that recovered and drain the next
// node while the service is still down.
func TestReplacementThatNeverBecomesReadyFailsTheStep(t *testing.T) {
	h := newHarness(t, model.ClusterSnapshot{
		Nodes: []model.Node{testNode("a"), testNode("b")},
		Pods:  []model.Pod{testPod("app", "web-1", "a", "web")},
	}, []model.PlanStep{step(1, "a", model.ImpactGreen)}, permissive())

	// The pod comes back, and stays unhealthy.
	h.cluster.replacementReady = false

	run := h.request(t, []int{1}, false)

	if run.Status != store.RunFailed {
		t.Fatalf("status = %s (%s), want Failed — the workload never became ready",
			run.Status, run.Summary)
	}
	if !strings.Contains(run.Summary, "did not recover") {
		t.Errorf("summary should explain the failure: %s", run.Summary)
	}
	if h.cluster.nodeCordoned("a") {
		t.Error("node left cordoned after a failed verification")
	}
}

// The KWOK escape hatch. On the fabric a fake pod reaches Running and never
// becomes Ready, so the strict criterion would hang every demo drain forever.
func TestRunningCriterionAcceptsAPodThatIsNeverReady(t *testing.T) {
	h := newHarnessWithReadiness(t, model.ClusterSnapshot{
		Nodes: []model.Node{testNode("a"), testNode("b")},
		Pods:  []model.Pod{testPod("app", "web-1", "a", "web")},
	}, []model.PlanStep{step(1, "a", model.ImpactGreen)}, permissive(), executor.ReadinessRunning)

	h.cluster.replacementReady = false

	if run := h.request(t, []int{1}, false); run.Status != store.RunSucceeded {
		t.Errorf("status = %s (%s), want Succeeded under the Running criterion",
			run.Status, run.Summary)
	}
}

// An unset criterion must never silently mean the weaker one.
func TestReadinessDefaultsToTheStrictCriterion(t *testing.T) {
	h := newHarness(t, model.ClusterSnapshot{
		Nodes: []model.Node{testNode("a"), testNode("b")},
		Pods:  []model.Pod{testPod("app", "web-1", "a", "web")},
	}, []model.PlanStep{step(1, "a", model.ImpactGreen)}, permissive())
	h.cluster.replacementReady = false

	// newHarness leaves Options.Readiness unset.
	if run := h.request(t, []int{1}, false); run.Status != store.RunFailed {
		t.Errorf("an unset readiness criterion behaved as Running; it must default to Ready (%s)",
			run.Status)
	}
}
