package cluster

import (
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/atedgimo/k8s-dencer/internal/model"
)

// Translation from Kubernetes API objects into the k8s-free domain model.
//
// This file is the only place the two vocabularies meet. Everything
// downstream — analyzer, planner, classifier — sees model types exclusively,
// which is what makes them testable from a YAML fixture.

func convertNode(n *corev1.Node) model.Node {
	out := model.Node{
		Name:          n.Name,
		Labels:        n.Labels,
		Annotations:   n.Annotations,
		Capacity:      convertResourceList(n.Status.Capacity),
		Allocatable:   convertResourceList(n.Status.Allocatable),
		Unschedulable: n.Spec.Unschedulable,
		CreatedAt:     n.CreationTimestamp.Time,
	}
	for _, t := range n.Spec.Taints {
		out.Taints = append(out.Taints, model.Taint{
			Key:    t.Key,
			Value:  t.Value,
			Effect: model.TaintEffect(t.Effect),
		})
	}
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			out.Ready = c.Status == corev1.ConditionTrue
			break
		}
	}
	return out
}

func convertPod(p *corev1.Pod, ownerOf ownerResolver) model.Pod {
	out := model.Pod{
		Namespace:    p.Namespace,
		Name:         p.Name,
		NodeName:     p.Spec.NodeName,
		Labels:       p.Labels,
		Phase:        model.PodPhase(p.Status.Phase),
		CreatedAt:    p.CreationTimestamp.Time,
		Requests:     effectiveRequests(p),
		NodeSelector: p.Spec.NodeSelector,
		Terminating:  p.DeletionTimestamp != nil,
	}

	if p.Spec.Priority != nil {
		out.Priority = *p.Spec.Priority
	}
	out.PriorityClass = p.Spec.PriorityClassName

	for _, t := range p.Spec.Tolerations {
		out.Tolerations = append(out.Tolerations, model.Toleration{
			Key:      t.Key,
			Operator: model.TolerationOperator(t.Operator),
			Value:    t.Value,
			Effect:   model.TaintEffect(t.Effect),
		})
	}

	// A pod bound to a PVC cannot always follow its volume to another node.
	// Recorded here so the impact classifier can weigh it; the planner does
	// not treat it as a hard blocker on its own.
	for _, v := range p.Spec.Volumes {
		if v.PersistentVolumeClaim != nil {
			out.HasPersistentVol = true
			break
		}
	}

	out.NodeAffinity = convertNodeAffinity(p.Spec.Affinity)
	out.PodAffinity = convertPodAffinity(p.Spec.Affinity)

	for _, c := range p.Spec.TopologySpreadConstraints {
		out.TopologySpread = append(out.TopologySpread, model.TopologySpreadConstraint{
			MaxSkew:           c.MaxSkew,
			TopologyKey:       c.TopologyKey,
			WhenUnsatisfiable: model.UnsatisfiableAction(c.WhenUnsatisfiable),
			LabelSelector:     convertLabelSelector(c.LabelSelector),
			MinDomains:        c.MinDomains,
		})
	}

	if owner := ownerOf(p); owner != nil {
		out.Owner = owner
	}

	return out
}

func convertPDB(p *policyv1.PodDisruptionBudget) model.PodDisruptionBudget {
	return model.PodDisruptionBudget{
		Namespace: p.Namespace,
		Name:      p.Name,
		Selector:  convertLabelSelector(p.Spec.Selector),
		// Taken from status, not spec. spec.minAvailable says what was asked
		// for; status.disruptionsAllowed is what the API server will actually
		// permit right now, and that is the difference between a PDB that
		// exists and a PDB that blocks.
		DisruptionsAllowed: p.Status.DisruptionsAllowed,
		CurrentHealthy:     p.Status.CurrentHealthy,
		DesiredHealthy:     p.Status.DesiredHealthy,
		ExpectedPods:       p.Status.ExpectedPods,
	}
}

// effectiveRequests computes a pod's scheduling footprint.
//
// Kubernetes' rule: per resource, the effective request is the greater of the
// summed app-container requests and the largest single init-container request,
// since init containers run to completion before the app containers start.
// Sidecars (init containers with restartPolicy Always) run alongside the app
// containers and are therefore added rather than maxed.
func effectiveRequests(p *corev1.Pod) model.Resources {
	var appSum model.Resources
	for _, c := range p.Spec.Containers {
		appSum = appSum.Add(convertResourceList(c.Resources.Requests))
	}

	var initMax model.Resources
	for _, c := range p.Spec.InitContainers {
		r := convertResourceList(c.Resources.Requests)
		if c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			appSum = appSum.Add(r)
			continue
		}
		initMax = maxResources(initMax, r)
	}

	effective := maxResources(appSum, initMax)

	if p.Spec.Overhead != nil {
		effective = effective.Add(convertResourceList(p.Spec.Overhead))
	}
	return effective
}

func maxResources(a, b model.Resources) model.Resources {
	out := a
	if b.MilliCPU > out.MilliCPU {
		out.MilliCPU = b.MilliCPU
	}
	if b.MemoryBytes > out.MemoryBytes {
		out.MemoryBytes = b.MemoryBytes
	}
	if b.Pods > out.Pods {
		out.Pods = b.Pods
	}
	return out
}

func convertResourceList(rl corev1.ResourceList) model.Resources {
	var out model.Resources
	if cpu, ok := rl[corev1.ResourceCPU]; ok {
		out.MilliCPU = cpu.MilliValue()
	}
	if mem, ok := rl[corev1.ResourceMemory]; ok {
		out.MemoryBytes = mem.Value()
	}
	if pods, ok := rl[corev1.ResourcePods]; ok {
		out.Pods = pods.Value()
	}
	return out
}

func convertNodeAffinity(a *corev1.Affinity) *model.NodeAffinity {
	if a == nil || a.NodeAffinity == nil ||
		a.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		return nil
	}
	sel := a.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	out := &model.NodeAffinity{}
	for _, term := range sel.NodeSelectorTerms {
		out.RequiredTerms = append(out.RequiredTerms, model.NodeSelectorTerm{
			MatchExpressions: convertSelectorRequirements(term.MatchExpressions),
			MatchFields:      convertSelectorRequirements(term.MatchFields),
		})
	}
	if len(out.RequiredTerms) == 0 {
		return nil
	}
	return out
}

func convertPodAffinity(a *corev1.Affinity) *model.PodAffinity {
	if a == nil {
		return nil
	}
	out := &model.PodAffinity{}
	if a.PodAffinity != nil {
		out.RequiredAffinity = convertPodAffinityTerms(
			a.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution)
	}
	if a.PodAntiAffinity != nil {
		out.RequiredAntiAffinity = convertPodAffinityTerms(
			a.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution)
	}
	if len(out.RequiredAffinity) == 0 && len(out.RequiredAntiAffinity) == 0 {
		return nil
	}
	return out
}

func convertPodAffinityTerms(terms []corev1.PodAffinityTerm) []model.PodAffinityTerm {
	var out []model.PodAffinityTerm
	for _, t := range terms {
		out = append(out, model.PodAffinityTerm{
			LabelSelector: convertLabelSelector(t.LabelSelector),
			TopologyKey:   t.TopologyKey,
			Namespaces:    t.Namespaces,
		})
	}
	return out
}

func convertLabelSelector(s *metav1.LabelSelector) *model.LabelSelector {
	if s == nil {
		return nil
	}
	out := &model.LabelSelector{MatchLabels: s.MatchLabels}
	for _, e := range s.MatchExpressions {
		out.MatchExpressions = append(out.MatchExpressions, model.SelectorRequirement{
			Key:      e.Key,
			Operator: model.SelectorOperator(e.Operator),
			Values:   e.Values,
		})
	}
	return out
}

func convertSelectorRequirements(reqs []corev1.NodeSelectorRequirement) []model.SelectorRequirement {
	var out []model.SelectorRequirement
	for _, r := range reqs {
		out = append(out, model.SelectorRequirement{
			Key:      r.Key,
			Operator: model.SelectorOperator(r.Operator),
			Values:   r.Values,
		})
	}
	return out
}
