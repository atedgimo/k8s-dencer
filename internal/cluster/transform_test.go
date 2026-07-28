package cluster

import (
	"reflect"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/atedgimo/k8s-dencer/internal/model"
)

// The transform exists to shrink the informer cache, and the only way it can
// hurt is by discarding something the converter reads. Grepping convert.go
// would catch today's reads and miss tomorrow's, so these tests assert the
// property directly: convert the object, transform it, convert it again, and
// require the two domain objects to be identical.
//
// A field added to convert.go that the transform strips fails this test on the
// next run, without anyone remembering to update a list.

func transformFor(t *testing.T, obj client.Object) func(any) (any, error) {
	t.Helper()
	opts := &cache.Options{}
	applyTransform(opts)
	for k, v := range opts.ByObject {
		if reflect.TypeOf(k) == reflect.TypeOf(obj) {
			if v.Transform == nil {
				t.Fatalf("no transform registered for %T", obj)
			}
			return v.Transform
		}
	}
	t.Fatalf("no ByObject entry for %T", obj)
	return nil
}

func TestPodTransformPreservesEverythingTheConverterReads(t *testing.T) {
	pod := fullyPopulatedPod()
	before := convertPod(pod.DeepCopy(), func(*corev1.Pod) *model.OwnerRef { return nil })

	out, err := transformFor(t, &corev1.Pod{})(pod.DeepCopy())
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	stripped, ok := out.(*corev1.Pod)
	if !ok {
		t.Fatalf("transform returned %T, want *corev1.Pod", out)
	}

	// Guard against a vacuous comparison: if the fixture carried none of the
	// fields the transform removes, the equality below would prove nothing.
	original := fullyPopulatedPod()
	if len(original.ManagedFields) == 0 || len(original.Annotations) == 0 || len(original.Status.ContainerStatuses) == 0 {
		t.Fatal("fixture does not populate the fields the transform strips; the comparison would be vacuous")
	}
	if len(stripped.ManagedFields) != 0 || len(stripped.Annotations) != 0 || len(stripped.Status.ContainerStatuses) != 0 {
		t.Error("transform left behind fields it is supposed to strip")
	}

	after := convertPod(stripped, func(*corev1.Pod) *model.OwnerRef { return nil })
	if !reflect.DeepEqual(before, after) {
		t.Errorf("transform changed what the planner sees:\n before: %+v\n  after: %+v", before, after)
	}
}

func TestNodeTransformPreservesEverythingTheConverterReads(t *testing.T) {
	node := fullyPopulatedNode()
	before := convertNode(node.DeepCopy())

	out, err := transformFor(t, &corev1.Node{})(node.DeepCopy())
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	stripped, ok := out.(*corev1.Node)
	if !ok {
		t.Fatalf("transform returned %T, want *corev1.Node", out)
	}

	original := fullyPopulatedNode()
	if len(original.ManagedFields) == 0 || len(original.Status.Images) == 0 {
		t.Fatal("fixture does not populate the fields the transform strips; the comparison would be vacuous")
	}
	if len(stripped.ManagedFields) != 0 || len(stripped.Status.Images) != 0 {
		t.Error("transform left behind fields it is supposed to strip")
	}

	// Node annotations are load-bearing — the KWOK fabric marks fake nodes with
	// one — so they are deliberately not stripped, unlike a pod's.
	if len(stripped.Annotations) == 0 {
		t.Error("node annotations were stripped; convertNode reads them")
	}

	after := convertNode(stripped)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("transform changed what the planner sees:\n before: %+v\n  after: %+v", before, after)
	}
}

// The ReplicaSet transform blanks the pod template, which is by far the largest
// part of the object and is never read: the resolver walks owner references
// only.
func TestReplicaSetTransformKeepsTheOwnerChain(t *testing.T) {
	yes := true
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:          "web-7d9f",
			Namespace:     "shop",
			Annotations:   map[string]string{"deployment.kubernetes.io/revision": "4"},
			ManagedFields: []metav1.ManagedFieldsEntry{{Manager: "kube-controller-manager"}},
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "Deployment", Name: "web", Controller: &yes},
			},
		},
		Spec: appsv1.ReplicaSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx"}}},
			},
		},
	}

	out, err := transformFor(t, &appsv1.ReplicaSet{})(rs.DeepCopy())
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	stripped := out.(*appsv1.ReplicaSet)

	if len(stripped.OwnerReferences) != 1 || stripped.OwnerReferences[0].Name != "web" {
		t.Fatalf("owner chain lost: %+v", stripped.OwnerReferences)
	}
	if len(stripped.Spec.Template.Spec.Containers) != 0 {
		t.Error("pod template survived the transform")
	}
	if len(stripped.ManagedFields) != 0 || len(stripped.Annotations) != 0 {
		t.Error("transform left behind fields it is supposed to strip")
	}
}

// A tombstone or an unexpected type must pass through rather than panic: the
// informer hands transforms whatever it has, including DeletedFinalStateUnknown.
func TestTransformsPassThroughUnexpectedTypes(t *testing.T) {
	for _, obj := range []client.Object{&corev1.Pod{}, &corev1.Node{}, &appsv1.ReplicaSet{}} {
		fn := transformFor(t, obj)
		got, err := fn("not an object")
		if err != nil {
			t.Errorf("%T: unexpected error %v", obj, err)
		}
		if got != "not an object" {
			t.Errorf("%T: passthrough changed the value", obj)
		}
	}
}

func fullyPopulatedPod() *corev1.Pod {
	yes := true
	prio := int32(100)
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "web-7d9f-abcde",
			Namespace:         "shop",
			Labels:            map[string]string{"app": "web", "tier": "front"},
			Annotations:       map[string]string{"checksum/config": "deadbeef"},
			CreationTimestamp: metav1.NewTime(time.Unix(1700000000, 0)),
			ManagedFields:     realisticManagedFields(),
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "ReplicaSet", Name: "web-7d9f", Controller: &yes},
			},
		},
		Spec: corev1.PodSpec{
			NodeName:          "node-3",
			NodeSelector:      map[string]string{"role": "app"},
			Priority:          &prio,
			PriorityClassName: "high",
			Tolerations:       []corev1.Toleration{{Key: "dedicated", Operator: corev1.TolerationOpExists}},
			Overhead:          corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m")},
			Volumes:           []corev1.Volume{{Name: "data"}},
			Containers: []corev1.Container{{
				Name: "app",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("250m"),
						corev1.ResourceMemory: resource.MustParse("512Mi"),
					},
				},
			}},
			InitContainers: []corev1.Container{{
				Name: "init",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
				},
			}},
			TopologySpreadConstraints: []corev1.TopologySpreadConstraint{{
				MaxSkew:           1,
				TopologyKey:       "topology.kubernetes.io/zone",
				WhenUnsatisfiable: corev1.DoNotSchedule,
				LabelSelector:     &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
			}},
			Affinity: &corev1.Affinity{
				PodAntiAffinity: &corev1.PodAntiAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
						TopologyKey:   "kubernetes.io/hostname",
						LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
					}},
				},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:         "app",
				RestartCount: 3,
				Image:        "registry.example.com/shop/web:1.42.0",
				ImageID:      "registry.example.com/shop/web@sha256:9f2c4d1e7a3b5c8d0e6f1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d",
				ContainerID:  "containerd://3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e0f1a2b3c4d",
				Ready:        true,
				Started:      &yes,
				State: corev1.ContainerState{
					Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(time.Unix(1700000100, 0))},
				},
				LastTerminationState: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{
						ExitCode: 137, Reason: "OOMKilled",
						ContainerID: "containerd://ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100",
					},
				},
			}},
			InitContainerStatuses: []corev1.ContainerStatus{{
				Name:    "init",
				Image:   "registry.example.com/shop/init:3.1",
				ImageID: "registry.example.com/shop/init@sha256:1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e0f1a2b",
				State:   corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}},
			}},
		},
	}
}

func fullyPopulatedNode() *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "node-3",
			Labels:            map[string]string{"topology.kubernetes.io/zone": "a"},
			Annotations:       map[string]string{"kwok.x-k8s.io/node": "fake"},
			CreationTimestamp: metav1.NewTime(time.Unix(1700000000, 0)),
			ManagedFields:     realisticManagedFields(),
		},
		Spec: corev1.NodeSpec{
			Unschedulable: true,
			Taints:        []corev1.Taint{{Key: "dedicated", Value: "app", Effect: corev1.TaintEffectNoSchedule}},
		},
		Status: corev1.NodeStatus{
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("4"),
				corev1.ResourceMemory: resource.MustParse("16Gi"),
			},
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("3800m"),
				corev1.ResourceMemory: resource.MustParse("15Gi"),
			},
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
			Images: []corev1.ContainerImage{
				{Names: []string{"nginx@sha256:abc", "nginx:1.25"}, SizeBytes: 187000000},
			},
		},
	}
}

// realisticManagedFields reproduces the shape and size of managed fields on a
// real pod, because the earlier version of this fixture carried a token
// single-field entry and made the transform look nearly worthless.
//
// Measured on the development cluster with `kubectl get pods -A -o json
// --show-managed-fields` (the flag matters — kubectl hides the section by
// default): kubelet-run pods carry two entries totalling ~3.5 KB of fieldsV1,
// roughly 40% of the serialised object. Two entries of ~2.0 KB and ~0.9 KB is
// what that looks like.
func realisticManagedFields() []metav1.ManagedFieldsEntry {
	spec := []byte(`{"f:metadata":{"f:annotations":{".":{},"f:checksum/config":{}},` +
		`"f:labels":{".":{},"f:app":{},"f:tier":{}},"f:ownerReferences":{".":{},` +
		`"k:{\"uid\":\"3f2b1a0c-9d8e-4f7a-b6c5-d4e3f2a1b0c9\"}":{}}},` +
		`"f:spec":{"f:containers":{"k:{\"name\":\"app\"}":{".":{},"f:image":{},` +
		`"f:imagePullPolicy":{},"f:name":{},"f:resources":{".":{},"f:requests":{".":{},` +
		`"f:cpu":{},"f:memory":{}}},"f:terminationMessagePath":{},` +
		`"f:terminationMessagePolicy":{},"f:volumeMounts":{".":{},` +
		`"k:{\"mountPath\":\"/data\"}":{".":{},"f:mountPath":{},"f:name":{}}}}},` +
		`"f:dnsPolicy":{},"f:enableServiceLinks":{},"f:nodeSelector":{},` +
		`"f:priorityClassName":{},"f:restartPolicy":{},"f:schedulerName":{},` +
		`"f:securityContext":{},"f:terminationGracePeriodSeconds":{},` +
		`"f:tolerations":{},"f:topologySpreadConstraints":{},"f:volumes":{".":{},` +
		`"k:{\"name\":\"data\"}":{".":{},"f:name":{},"f:emptyDir":{}}}}}`)
	status := []byte(`{"f:status":{"f:conditions":{"k:{\"type\":\"ContainersReady\"}":` +
		`{".":{},"f:lastProbeTime":{},"f:lastTransitionTime":{},"f:status":{},"f:type":{}},` +
		`"k:{\"type\":\"Initialized\"}":{".":{},"f:lastProbeTime":{},` +
		`"f:lastTransitionTime":{},"f:status":{},"f:type":{}},` +
		`"k:{\"type\":\"Ready\"}":{".":{},"f:lastProbeTime":{},` +
		`"f:lastTransitionTime":{},"f:status":{},"f:type":{}}},` +
		`"f:containerStatuses":{},"f:hostIP":{},"f:phase":{},"f:podIP":{},` +
		`"f:podIPs":{".":{},"k:{\"ip\":\"10.42.1.37\"}":{".":{},"f:ip":{}}},` +
		`"f:startTime":{}}}`)
	return []metav1.ManagedFieldsEntry{
		{
			Manager:    "kube-controller-manager",
			Operation:  metav1.ManagedFieldsOperationUpdate,
			APIVersion: "v1",
			Time:       ptrTime(time.Unix(1700000000, 0)),
			FieldsType: "FieldsV1",
			FieldsV1:   &metav1.FieldsV1{Raw: spec},
		},
		{
			Manager:     "kubelet",
			Operation:   metav1.ManagedFieldsOperationUpdate,
			APIVersion:  "v1",
			Time:        ptrTime(time.Unix(1700000100, 0)),
			FieldsType:  "FieldsV1",
			FieldsV1:    &metav1.FieldsV1{Raw: status},
			Subresource: "status",
		},
	}
}

func ptrTime(t time.Time) *metav1.Time {
	mt := metav1.NewTime(t)
	return &mt
}
