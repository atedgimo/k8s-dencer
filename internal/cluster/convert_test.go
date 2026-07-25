package cluster

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/atedgimo/k8s-dencer/internal/model"
)

func requests(cpu, mem string) corev1.ResourceList {
	rl := corev1.ResourceList{}
	if cpu != "" {
		rl[corev1.ResourceCPU] = resource.MustParse(cpu)
	}
	if mem != "" {
		rl[corev1.ResourceMemory] = resource.MustParse(mem)
	}
	return rl
}

func noOwner(*corev1.Pod) *model.OwnerRef { return nil }

// The effective-request rule is the one piece of conversion arithmetic that is
// easy to get subtly wrong, and getting it wrong misprices every pod the
// planner moves.
func TestEffectiveRequests(t *testing.T) {
	always := corev1.ContainerRestartPolicyAlways

	cases := []struct {
		name    string
		spec    corev1.PodSpec
		wantCPU int64
		wantMem int64
	}{
		{
			name: "app containers are summed",
			spec: corev1.PodSpec{Containers: []corev1.Container{
				{Resources: corev1.ResourceRequirements{Requests: requests("500m", "1Gi")}},
				{Resources: corev1.ResourceRequirements{Requests: requests("250m", "512Mi")}},
			}},
			wantCPU: 750,
			wantMem: 1610612736,
		},
		{
			name: "a large init container dominates a small app sum",
			spec: corev1.PodSpec{
				InitContainers: []corev1.Container{
					{Resources: corev1.ResourceRequirements{Requests: requests("2", "4Gi")}},
				},
				Containers: []corev1.Container{
					{Resources: corev1.ResourceRequirements{Requests: requests("500m", "1Gi")}},
				},
			},
			wantCPU: 2000,
			wantMem: 4294967296,
		},
		{
			name: "init containers are maxed against each other, not summed",
			spec: corev1.PodSpec{
				InitContainers: []corev1.Container{
					{Resources: corev1.ResourceRequirements{Requests: requests("1", "")}},
					{Resources: corev1.ResourceRequirements{Requests: requests("2", "")}},
				},
				Containers: []corev1.Container{
					{Resources: corev1.ResourceRequirements{Requests: requests("100m", "")}},
				},
			},
			wantCPU: 2000,
		},
		{
			name: "sidecars run alongside the app and are added",
			spec: corev1.PodSpec{
				InitContainers: []corev1.Container{
					{RestartPolicy: &always, Resources: corev1.ResourceRequirements{Requests: requests("500m", "")}},
				},
				Containers: []corev1.Container{
					{Resources: corev1.ResourceRequirements{Requests: requests("500m", "")}},
				},
			},
			wantCPU: 1000,
		},
		{
			name: "pod overhead is added on top",
			spec: corev1.PodSpec{
				Overhead: requests("100m", ""),
				Containers: []corev1.Container{
					{Resources: corev1.ResourceRequirements{Requests: requests("500m", "")}},
				},
			},
			wantCPU: 600,
		},
		{
			name:    "a pod with no requests costs nothing but a pod slot",
			spec:    corev1.PodSpec{Containers: []corev1.Container{{}}},
			wantCPU: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := effectiveRequests(&corev1.Pod{Spec: tc.spec})
			if got.MilliCPU != tc.wantCPU {
				t.Errorf("MilliCPU = %d, want %d", got.MilliCPU, tc.wantCPU)
			}
			if tc.wantMem != 0 && got.MemoryBytes != tc.wantMem {
				t.Errorf("MemoryBytes = %d, want %d", got.MemoryBytes, tc.wantMem)
			}
		})
	}
}

func TestConvertNode(t *testing.T) {
	n := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "kwok-node-0",
			Labels: map[string]string{model.LabelZone: "zone-a"},
		},
		Spec: corev1.NodeSpec{
			Unschedulable: true,
			Taints: []corev1.Taint{
				{Key: "kwok.x-k8s.io/node", Value: "fake", Effect: corev1.TaintEffectNoSchedule},
			},
		},
		Status: corev1.NodeStatus{
			Capacity:    corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8"), corev1.ResourcePods: resource.MustParse("110")},
			Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("7900m"), corev1.ResourcePods: resource.MustParse("110")},
			Conditions:  []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}

	got := convertNode(n)

	if got.Allocatable.MilliCPU != 7900 {
		t.Errorf("allocatable cpu = %d, want 7900", got.Allocatable.MilliCPU)
	}
	if got.Capacity.MilliCPU != 8000 {
		t.Errorf("capacity cpu = %d, want 8000", got.Capacity.MilliCPU)
	}
	if !got.Ready {
		t.Error("node should be Ready")
	}
	if !got.Unschedulable {
		t.Error("cordon flag lost")
	}
	if got.Zone() != "zone-a" {
		t.Errorf("zone = %q", got.Zone())
	}
	if len(got.Taints) != 1 || got.Taints[0].Effect != model.TaintEffectNoSchedule {
		t.Errorf("taints = %+v", got.Taints)
	}
}

func TestConvertNodeNotReadyWithoutCondition(t *testing.T) {
	// A node with no Ready condition must not default to ready: treating an
	// unknown node as schedulable is how a planner proposes moving pods onto
	// something that cannot accept them.
	got := convertNode(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n"}})
	if got.Ready {
		t.Error("node without a Ready condition must not report ready")
	}
}

// The distinction between what a PDB asks for and what it currently permits is
// the difference between "a PDB exists" and "this drain will be refused".
func TestConvertPDBUsesLiveHeadroomNotSpec(t *testing.T) {
	minAvailable := intstr.FromInt32(3)
	p := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Namespace: "dencer-demo", Name: "payments"},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MinAvailable: &minAvailable,
			Selector:     &metav1.LabelSelector{MatchLabels: map[string]string{"app": "payments"}},
		},
		Status: policyv1.PodDisruptionBudgetStatus{
			DisruptionsAllowed: 0,
			CurrentHealthy:     3,
			DesiredHealthy:     3,
			ExpectedPods:       3,
		},
	}

	got := convertPDB(p)

	if !got.Blocks() {
		t.Error("a PDB with zero disruptions allowed must block")
	}
	if got.Key() != "dencer-demo/payments" {
		t.Errorf("key = %q", got.Key())
	}
	if !got.Selector.Matches(map[string]string{"app": "payments"}) {
		t.Error("selector did not survive conversion")
	}

	p.Status.DisruptionsAllowed = 2
	if convertPDB(p).Blocks() {
		t.Error("a PDB with headroom must not block")
	}
}

func TestConvertPodConstraints(t *testing.T) {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "dencer-demo",
			Name:      "cache-0",
			Labels:    map[string]string{"app": "dencer-cache"},
		},
		Spec: corev1.PodSpec{
			NodeName:     "kwok-node-3",
			NodeSelector: map[string]string{"pool": "batch"},
			Tolerations: []corev1.Toleration{
				{Key: "kwok.x-k8s.io/node", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
			},
			Affinity: &corev1.Affinity{
				NodeAffinity: &corev1.NodeAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
						NodeSelectorTerms: []corev1.NodeSelectorTerm{{
							MatchExpressions: []corev1.NodeSelectorRequirement{
								{Key: "type", Operator: corev1.NodeSelectorOpIn, Values: []string{"kwok"}},
							},
						}},
					},
				},
				PodAntiAffinity: &corev1.PodAntiAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
						TopologyKey:   model.LabelHostname,
						LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "dencer-cache"}},
					}},
				},
			},
			TopologySpreadConstraints: []corev1.TopologySpreadConstraint{{
				MaxSkew:           1,
				TopologyKey:       model.LabelZone,
				WhenUnsatisfiable: corev1.DoNotSchedule,
			}},
			Volumes: []corev1.Volume{{
				Name:         "data",
				VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "c"}},
			}},
			Containers: []corev1.Container{
				{Resources: corev1.ResourceRequirements{Requests: requests("1", "2Gi")}},
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}

	got := convertPod(p, noOwner)

	if got.Key() != "dencer-demo/cache-0" {
		t.Errorf("key = %q", got.Key())
	}
	if got.Requests.MilliCPU != 1000 {
		t.Errorf("cpu = %d", got.Requests.MilliCPU)
	}
	if got.NodeAffinity == nil || len(got.NodeAffinity.RequiredTerms) != 1 {
		t.Fatalf("node affinity lost: %+v", got.NodeAffinity)
	}
	if got.PodAffinity == nil || len(got.PodAffinity.RequiredAntiAffinity) != 1 {
		t.Fatalf("anti-affinity lost: %+v", got.PodAffinity)
	}
	if got.PodAffinity.RequiredAntiAffinity[0].TopologyKey != model.LabelHostname {
		t.Error("anti-affinity topology key lost")
	}
	if len(got.TopologySpread) != 1 || !got.TopologySpread[0].IsHard() {
		t.Errorf("topology spread = %+v", got.TopologySpread)
	}
	if !got.HasPersistentVol {
		t.Error("PVC not detected")
	}
	if len(got.Tolerations) != 1 || !got.Tolerations[0].ToleratesTaint(
		model.Taint{Key: "kwok.x-k8s.io/node", Value: "fake", Effect: model.TaintEffectNoSchedule}) {
		t.Error("toleration did not survive conversion")
	}
}

// Preferred affinity affects scoring, never feasibility. Converting it into
// the required set would make the planner refuse moves the scheduler allows.
func TestPreferredAffinityIsNotTreatedAsRequired(t *testing.T) {
	p := &corev1.Pod{Spec: corev1.PodSpec{
		Affinity: &corev1.Affinity{
			NodeAffinity: &corev1.NodeAffinity{
				PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{{Weight: 10}},
			},
			PodAntiAffinity: &corev1.PodAntiAffinity{
				PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{Weight: 10}},
			},
		},
	}}

	got := convertPod(p, noOwner)

	if got.NodeAffinity != nil {
		t.Errorf("preferred node affinity must not become required: %+v", got.NodeAffinity)
	}
	if got.PodAffinity != nil {
		t.Errorf("preferred anti-affinity must not become required: %+v", got.PodAffinity)
	}
}
