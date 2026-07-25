package impact_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/atedgimo/k8s-dencer/internal/constraints"
	"github.com/atedgimo/k8s-dencer/internal/impact"
	"github.com/atedgimo/k8s-dencer/internal/model"
	"github.com/atedgimo/k8s-dencer/internal/planner"
)

func snapshotWith(pods []model.Pod, pdbs []model.PodDisruptionBudget) *model.ClusterSnapshot {
	return &model.ClusterSnapshot{
		Nodes: []model.Node{
			{Name: "a", Ready: true, Allocatable: model.Resources{MilliCPU: 8000, MemoryBytes: 1 << 34, Pods: 110}},
			{Name: "b", Ready: true, Allocatable: model.Resources{MilliCPU: 8000, MemoryBytes: 1 << 34, Pods: 110}},
		},
		Pods: pods,
		PDBs: pdbs,
	}
}

func stepMoving(pods ...model.Pod) model.PlanStep {
	step := model.PlanStep{SequenceNumber: 1, TargetNode: "a"}
	for _, p := range pods {
		step.Moves = append(step.Moves, model.Move{
			Namespace: p.Namespace, Pod: p.Name, FromNode: "a", ToNode: "b",
		})
	}
	return step
}

func classify(t *testing.T, snap *model.ClusterSnapshot, step model.PlanStep, th impact.Thresholds) (model.ImpactRating, string, []model.ImpactReason) {
	t.Helper()
	return impact.New(th).ClassifyStep(step, snap, constraints.Analyze(snap))
}

func deploymentPod(name string) model.Pod {
	return model.Pod{
		Namespace: "app", Name: name, NodeName: "a", Phase: model.PodRunning,
		Labels:   map[string]string{"app": "web"},
		Requests: model.Resources{MilliCPU: 100, MemoryBytes: 1 << 26},
		Owner:    &model.OwnerRef{Kind: "Deployment", Name: "web"},
	}
}

func TestUnconstrainedStepIsGreen(t *testing.T) {
	pod := deploymentPod("web-1")
	snap := snapshotWith([]model.Pod{pod}, nil)

	rating, rationale, reasons := classify(t, snap, stepMoving(pod), impact.DefaultThresholds())

	if rating != model.ImpactGreen {
		t.Errorf("rating = %s, want Green (reasons: %+v)", rating, reasons)
	}
	if len(reasons) != 0 {
		t.Errorf("expected no reasons, got %+v", reasons)
	}
	if !strings.Contains(rationale, "safe to run at any time") {
		t.Errorf("Green rationale should say so plainly, got: %q", rationale)
	}
}

// A pod with no controller is deleted permanently by eviction. Nothing about a
// consolidation step is more consequential.
func TestUnmanagedPodIsRed(t *testing.T) {
	pod := deploymentPod("orphan")
	pod.Owner = nil
	snap := snapshotWith([]model.Pod{pod}, nil)

	rating, rationale, reasons := classify(t, snap, stepMoving(pod), impact.DefaultThresholds())

	if rating != model.ImpactRed {
		t.Fatalf("rating = %s, want Red", rating)
	}
	if reasons[0].Kind != impact.ReasonUnmanagedPod {
		t.Errorf("driving reason = %s, want %s", reasons[0].Kind, impact.ReasonUnmanagedPod)
	}
	if !strings.Contains(rationale, "nothing will recreate it") {
		t.Errorf("rationale must explain the consequence, got: %q", rationale)
	}
	if !strings.Contains(rationale, "maintenance window") {
		t.Error("Red rationale must state the maintenance-window restriction")
	}
}

func TestStatefulSetPodIsRed(t *testing.T) {
	pod := deploymentPod("db-0")
	pod.Owner = &model.OwnerRef{Kind: "StatefulSet", Name: "db"}
	snap := snapshotWith([]model.Pod{pod}, nil)

	rating, rationale, _ := classify(t, snap, stepMoving(pod), impact.DefaultThresholds())

	if rating != model.ImpactRed {
		t.Errorf("rating = %s, want Red", rating)
	}
	if !strings.Contains(rationale, "StatefulSet db") {
		t.Errorf("rationale must name the StatefulSet, got: %q", rationale)
	}
}

// The distinction the whole product rests on: a PDB with no headroom is Red,
// the same PDB with headroom is not.
func TestPDBHeadroomDrivesTheRating(t *testing.T) {
	pod := deploymentPod("web-1")
	selector := &model.LabelSelector{MatchLabels: map[string]string{"app": "web"}}

	cases := []struct {
		name       string
		allowed    int32
		want       model.ImpactRating
		wantReason string
	}{
		{"zero headroom is Red", 0, model.ImpactRed, impact.ReasonPDBZeroHeadroom},
		{"one disruption is tight, Yellow", 1, model.ImpactYellow, impact.ReasonPDBTightHeadroom},
		{"comfortable headroom is Green", 5, model.ImpactGreen, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap := snapshotWith([]model.Pod{pod}, []model.PodDisruptionBudget{{
				Namespace: "app", Name: "web", Selector: selector,
				DisruptionsAllowed: tc.allowed, CurrentHealthy: 3, DesiredHealthy: 3, ExpectedPods: 3,
			}})

			rating, rationale, reasons := classify(t, snap, stepMoving(pod), impact.DefaultThresholds())

			if rating != tc.want {
				t.Errorf("rating = %s, want %s (reasons %+v)", rating, tc.want, reasons)
			}
			if tc.wantReason == "" {
				return
			}
			if reasons[0].Kind != tc.wantReason {
				t.Errorf("driving reason = %s, want %s", reasons[0].Kind, tc.wantReason)
			}
			if !strings.Contains(rationale, "app/web") {
				t.Errorf("rationale must name the PDB, got: %q", rationale)
			}
		})
	}
}

// Doc §10: where headroom stops being acceptable differs per cluster, so the
// boundary has to move with configuration rather than be baked in.
func TestTightHeadroomThresholdIsTunable(t *testing.T) {
	pod := deploymentPod("web-1")
	snap := snapshotWith([]model.Pod{pod}, []model.PodDisruptionBudget{{
		Namespace: "app", Name: "web",
		Selector:           &model.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
		DisruptionsAllowed: 3, CurrentHealthy: 6, DesiredHealthy: 3,
	}})

	if rating, _, _ := classify(t, snap, stepMoving(pod), impact.DefaultThresholds()); rating != model.ImpactGreen {
		t.Errorf("3 disruptions of headroom should be Green by default, got %s", rating)
	}

	strict := impact.Thresholds{TightPDBHeadroom: 5}
	if rating, _, _ := classify(t, snap, stepMoving(pod), strict); rating != model.ImpactYellow {
		t.Errorf("with TightPDBHeadroom=5 the same step should be Yellow, got %s", rating)
	}
}

func TestBlastRadiusThresholds(t *testing.T) {
	makePods := func(n int) []model.Pod {
		var pods []model.Pod
		for i := 0; i < n; i++ {
			pods = append(pods, deploymentPod("web-"+string(rune('a'+i))))
		}
		return pods
	}

	cases := []struct {
		pods int
		want model.ImpactRating
	}{
		{2, model.ImpactGreen},
		{5, model.ImpactYellow},
		{15, model.ImpactRed},
	}
	for _, tc := range cases {
		pods := makePods(tc.pods)
		snap := snapshotWith(pods, nil)
		rating, _, reasons := classify(t, snap, stepMoving(pods...), impact.DefaultThresholds())
		if rating != tc.want {
			t.Errorf("%d pods rated %s, want %s (reasons %+v)", tc.pods, rating, tc.want, reasons)
		}
	}
}

func TestAntiAffinityAndSpreadAreYellow(t *testing.T) {
	anti := deploymentPod("cache-1")
	anti.PodAffinity = &model.PodAffinity{RequiredAntiAffinity: []model.PodAffinityTerm{{
		TopologyKey:   model.LabelHostname,
		LabelSelector: &model.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
	}}}

	snap := snapshotWith([]model.Pod{anti}, nil)
	rating, rationale, reasons := classify(t, snap, stepMoving(anti), impact.DefaultThresholds())
	if rating != model.ImpactYellow {
		t.Errorf("required anti-affinity should be Yellow, got %s", rating)
	}
	if reasons[0].Kind != impact.ReasonAntiAffinity {
		t.Errorf("driving reason = %s", reasons[0].Kind)
	}
	// The rationale must quote the analyzer's text, not invent its own.
	if !strings.Contains(rationale, "anti-affinity") {
		t.Errorf("rationale should carry the analyzer's explanation, got: %q", rationale)
	}
}

// A ScheduleAnyway spread constraint is a preference. Rating a step Yellow for
// a preference the scheduler would happily violate is a false alarm, and false
// alarms are how a tool gets ignored.
func TestSoftTopologySpreadDoesNotAffectRating(t *testing.T) {
	pod := deploymentPod("web-1")
	pod.TopologySpread = []model.TopologySpreadConstraint{{
		MaxSkew: 1, TopologyKey: model.LabelZone, WhenUnsatisfiable: model.ScheduleAnyway,
	}}
	snap := snapshotWith([]model.Pod{pod}, nil)

	rating, _, reasons := classify(t, snap, stepMoving(pod), impact.DefaultThresholds())
	if rating != model.ImpactGreen {
		t.Errorf("soft spread should not change the rating, got %s (%+v)", rating, reasons)
	}
}

func TestPersistentVolumeIsYellow(t *testing.T) {
	pod := deploymentPod("web-1")
	pod.HasPersistentVol = true
	snap := snapshotWith([]model.Pod{pod}, nil)

	rating, rationale, _ := classify(t, snap, stepMoving(pod), impact.DefaultThresholds())
	if rating != model.ImpactYellow {
		t.Errorf("rating = %s, want Yellow", rating)
	}
	if !strings.Contains(rationale, "PersistentVolumeClaim") {
		t.Errorf("rationale should name the volume concern, got: %q", rationale)
	}
}

// The worst factor decides, and it must be the one the rationale leads with —
// that is the question an operator is actually asking.
func TestHighestSeverityWinsAndLeadsTheRationale(t *testing.T) {
	orphan := deploymentPod("orphan")
	orphan.Owner = nil
	orphan.HasPersistentVol = true

	snap := snapshotWith([]model.Pod{orphan}, nil)
	rating, rationale, reasons := classify(t, snap, stepMoving(orphan), impact.DefaultThresholds())

	if rating != model.ImpactRed {
		t.Fatalf("rating = %s, want Red", rating)
	}
	if reasons[0].Kind != impact.ReasonUnmanagedPod {
		t.Errorf("Red reason must sort first, got %s", reasons[0].Kind)
	}
	if !strings.Contains(rationale, "Also:") {
		t.Error("supporting reasons should still be reported")
	}
	if strings.Index(rationale, "no controller") > strings.Index(rationale, "Also:") {
		t.Error("the driving reason must come before the supporting ones")
	}
}

func TestRatingIsDeterministic(t *testing.T) {
	pod := deploymentPod("web-1")
	pod.HasPersistentVol = true
	pod.PodAffinity = &model.PodAffinity{RequiredAntiAffinity: []model.PodAffinityTerm{{
		TopologyKey:   model.LabelHostname,
		LabelSelector: &model.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
	}}}
	snap := snapshotWith([]model.Pod{pod}, nil)

	_, first, _ := classify(t, snap, stepMoving(pod), impact.DefaultThresholds())
	_, second, _ := classify(t, snap, stepMoving(pod), impact.DefaultThresholds())
	if first != second {
		t.Errorf("rationale differs across runs:\n%q\n%q", first, second)
	}
}

func TestZeroThresholdsFallBackToDefaults(t *testing.T) {
	c := impact.New(impact.Thresholds{})
	want := impact.DefaultThresholds()
	if c.Thresholds != want {
		t.Errorf("zero thresholds = %+v, want defaults %+v", c.Thresholds, want)
	}
}

// End to end over a real captured cluster: every step must be rated and
// explained, with no blank rationale reaching the UI or the agent.
func TestClassifyPlanOverFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "test", "fixtures", "b-pdb-blocked.yaml"))
	if err != nil {
		t.Skipf("fixture unavailable: %v", err)
	}
	var snap model.ClusterSnapshot
	if err := yaml.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}

	analysis := constraints.Analyze(&snap)
	opts := planner.DefaultOptions()
	opts.MinNodeAge = 0
	plan, err := planner.Greedy{}.Plan(&snap, analysis, opts)
	if err != nil {
		t.Fatal(err)
	}

	impact.New(impact.Thresholds{}).ClassifyPlan(plan, &snap, analysis)

	counts := plan.CountByRating()
	for _, step := range plan.Steps {
		if step.Impact == "" {
			t.Errorf("step %d has no rating", step.SequenceNumber)
		}
		if len(strings.TrimSpace(step.Rationale)) < 40 {
			t.Errorf("step %d rationale is too thin to be useful: %q", step.SequenceNumber, step.Rationale)
		}
		if step.Impact == model.ImpactRed && !step.RequiresMaintenanceWindow() {
			t.Errorf("step %d is Red but not window-confined", step.SequenceNumber)
		}
	}
	t.Logf("%d steps: %d Green, %d Yellow, %d Red",
		len(plan.Steps), counts[model.ImpactGreen], counts[model.ImpactYellow], counts[model.ImpactRed])
	if len(plan.Steps) > 0 {
		t.Logf("example: %s", plan.Steps[0].Rationale)
	}
}
