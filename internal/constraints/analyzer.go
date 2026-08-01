package constraints

import (
	"fmt"
	"sort"
	"strings"

	"github.com/atedgimo/k8s-dencer/internal/model"
)

// Analyze derives the effective constraint set for every pod in the snapshot.
//
// Explanations are written here once and consumed verbatim downstream. They
// name the specific object responsible and include the live numbers, because
// "a PDB blocks this" is not an explanation — "PodDisruptionBudget
// dencer-demo/payments currently allows 0 disruptions (3 healthy, 3 required)"
// is.
func Analyze(snap *model.ClusterSnapshot) *Analysis {
	placement := NewPlacement(snap)
	pdbIndex := indexPDBs(snap)

	analysis := &Analysis{
		TakenAt: snap.TakenAt,
		Pods:    make([]PodConstraints, 0, len(snap.Pods)),
	}

	for _, pod := range snap.Pods {
		analysis.Pods = append(analysis.Pods, analyzePod(pod, snap, placement, pdbIndex))
	}

	// Sorted so the output is stable regardless of the order the informer
	// cache happened to list pods in.
	sort.Slice(analysis.Pods, func(i, j int) bool {
		return analysis.Pods[i].Key() < analysis.Pods[j].Key()
	})
	analysis.index = make(map[string]int, len(analysis.Pods))
	for i, pc := range analysis.Pods {
		analysis.index[pc.Key()] = i
	}
	return analysis
}

func analyzePod(pod model.Pod, snap *model.ClusterSnapshot, placement *Placement, pdbIndex map[string][]model.PodDisruptionBudget) PodConstraints {
	pc := PodConstraints{
		Namespace: pod.Namespace,
		Name:      pod.Name,
		NodeName:  pod.NodeName,
		Movable:   pod.IsMovable(),
	}

	// The explicit hands-off signal outranks everything: it is the owner
	// saying "do not touch this", in the vocabulary Karpenter and the
	// cluster autoscaler both honour, and this product honours it too.
	if pod.DoNotDisrupt {
		pc.Constraints = append(pc.Constraints, Constraint{
			Kind:     KindDoNotDisrupt,
			Hard:     true,
			Blocking: true,
			Explanation: "Annotated hands-off (karpenter.sh/do-not-disrupt or " +
				"cluster-autoscaler.kubernetes.io/safe-to-evict: \"false\"). " +
				"The owner has explicitly opted this pod out of voluntary disruption, " +
				"and k8s-dencer honours the same convention the autoscalers do.",
		})
	}

	// Controller pinning comes first: if the pod cannot move at all, the rest
	// of the constraint set is context rather than explanation.
	if pod.Owner != nil && pod.Owner.Kind == "DaemonSet" {
		pc.Constraints = append(pc.Constraints, Constraint{
			Kind:     KindControllerPinned,
			Subject:  pod.Owner.Kind + "/" + pod.Owner.Name,
			Hard:     true,
			Blocking: true,
			Explanation: fmt.Sprintf(
				"Managed by DaemonSet %s, so it is pinned to node %s. Draining the node does not free this pod's capacity — it is recreated there.",
				pod.Owner.Name, pod.NodeName),
		})
	}
	if pod.Terminating {
		pc.Constraints = append(pc.Constraints, Constraint{
			Kind:        KindControllerPinned,
			Hard:        true,
			Blocking:    true,
			Explanation: "Pod is already terminating.",
		})
	}

	pc.Constraints = append(pc.Constraints, pdbConstraints(pod, pdbIndex)...)
	pc.Constraints = append(pc.Constraints, resourceConstraint(pod, snap, placement))

	if c, ok := nodeSelectorConstraint(pod, snap); ok {
		pc.Constraints = append(pc.Constraints, c)
	}
	if c, ok := nodeAffinityConstraint(pod, snap); ok {
		pc.Constraints = append(pc.Constraints, c)
	}
	pc.Constraints = append(pc.Constraints, podAffinityConstraints(pod)...)
	pc.Constraints = append(pc.Constraints, topologySpreadConstraints(pod, snap)...)
	if c, ok := taintToleranceConstraint(pod, snap); ok {
		pc.Constraints = append(pc.Constraints, c)
	}
	if pod.HasPersistentVol {
		pc.Constraints = append(pc.Constraints, Constraint{
			Kind:        KindPersistentVolume,
			Hard:        false,
			Blocking:    false,
			Explanation: "Uses a PersistentVolumeClaim. Depending on the volume's topology it may not be able to follow the pod to another node.",
		})
	}

	// A pod already blocked from eviction has no candidate nodes worth
	// computing, and skipping the scan keeps the analysis cheap on large
	// clusters.
	for _, c := range pc.Constraints {
		if c.Blocking {
			pc.Movable = false
		}
	}
	if pc.Movable {
		pc.CandidateNodes = placement.CandidateNodes(pod)
	}

	return pc
}

// pdbConstraints reports every PDB covering the pod. Both the blocking and the
// healthy case are recorded: the classifier needs to distinguish "protected by
// a PDB with room" from "protected by a PDB with none", and only the second is
// a reason a step is Red.
func pdbConstraints(pod model.Pod, index map[string][]model.PodDisruptionBudget) []Constraint {
	var out []Constraint
	for _, pdb := range index[pod.Namespace] {
		if pdb.Selector.IsEmpty() || !pdb.Selector.Matches(pod.Labels) {
			continue
		}
		if pdb.Blocks() {
			out = append(out, Constraint{
				Kind:     KindPDB,
				Subject:  pdb.Key(),
				Hard:     true,
				Blocking: true,
				Explanation: fmt.Sprintf(
					"PodDisruptionBudget %s currently allows 0 disruptions (%d healthy, %d required). The API server will refuse to evict this pod.",
					pdb.Key(), pdb.CurrentHealthy, pdb.DesiredHealthy),
			})
			continue
		}
		out = append(out, Constraint{
			Kind:     KindPDB,
			Subject:  pdb.Key(),
			Hard:     true,
			Blocking: false,
			Explanation: fmt.Sprintf(
				"PodDisruptionBudget %s allows %d more concurrent disruption(s) (%d healthy, %d required).",
				pdb.Key(), pdb.DisruptionsAllowed, pdb.CurrentHealthy, pdb.DesiredHealthy),
		})
	}
	return out
}

func resourceConstraint(pod model.Pod, snap *model.ClusterSnapshot, placement *Placement) Constraint {
	fits := 0
	for _, n := range snap.Nodes {
		if n.Name == pod.NodeName {
			continue
		}
		need := pod.Requests.Add(model.Resources{Pods: 1})
		if need.Fits(placement.Free(n.Name)) {
			fits++
		}
	}
	other := len(snap.Nodes) - 1
	return Constraint{
		Kind:     KindResources,
		Hard:     true,
		Blocking: false,
		Explanation: fmt.Sprintf(
			"Requests %s. %d of %d other nodes have enough free capacity right now.",
			pod.Requests, fits, other),
	}
}

func nodeSelectorConstraint(pod model.Pod, snap *model.ClusterSnapshot) (Constraint, bool) {
	if len(pod.NodeSelector) == 0 {
		return Constraint{}, false
	}
	matching := 0
	for _, n := range snap.Nodes {
		ok := true
		for k, v := range pod.NodeSelector {
			if n.Labels[k] != v {
				ok = false
				break
			}
		}
		if ok {
			matching++
		}
	}
	pairs := make([]string, 0, len(pod.NodeSelector))
	for k, v := range pod.NodeSelector {
		pairs = append(pairs, k+"="+v)
	}
	sort.Strings(pairs)

	return Constraint{
		Kind:     KindNodeSelector,
		Subject:  strings.Join(pairs, ","),
		Hard:     true,
		Blocking: matching == 0,
		Explanation: fmt.Sprintf(
			"Node selector %s restricts placement to %d of %d nodes.",
			strings.Join(pairs, ","), matching, len(snap.Nodes)),
	}, true
}

func nodeAffinityConstraint(pod model.Pod, snap *model.ClusterSnapshot) (Constraint, bool) {
	if pod.NodeAffinity == nil || len(pod.NodeAffinity.RequiredTerms) == 0 {
		return Constraint{}, false
	}
	matching := 0
	for _, n := range snap.Nodes {
		for _, term := range pod.NodeAffinity.RequiredTerms {
			sel := model.LabelSelector{MatchExpressions: term.MatchExpressions}
			if sel.Matches(n.Labels) {
				matching++
				break
			}
		}
	}
	return Constraint{
		Kind:     KindNodeAffinity,
		Subject:  describeTerms(pod.NodeAffinity.RequiredTerms),
		Hard:     true,
		Blocking: matching == 0,
		Explanation: fmt.Sprintf(
			"Required node affinity (%s) restricts placement to %d of %d nodes.",
			describeTerms(pod.NodeAffinity.RequiredTerms), matching, len(snap.Nodes)),
	}, true
}

func podAffinityConstraints(pod model.Pod) []Constraint {
	if pod.PodAffinity == nil {
		return nil
	}
	var out []Constraint
	for _, term := range pod.PodAffinity.RequiredAntiAffinity {
		out = append(out, Constraint{
			Kind:     KindPodAntiAffinity,
			Subject:  term.TopologyKey,
			Hard:     true,
			Blocking: false,
			Explanation: fmt.Sprintf(
				"Required anti-affinity on %s against pods matching %s: this pod cannot share a %s with another matching replica, so it holds its node open.",
				term.TopologyKey, describeSelector(term.LabelSelector), shortTopology(term.TopologyKey)),
		})
	}
	for _, term := range pod.PodAffinity.RequiredAffinity {
		out = append(out, Constraint{
			Kind:     KindPodAffinity,
			Subject:  term.TopologyKey,
			Hard:     true,
			Blocking: false,
			Explanation: fmt.Sprintf(
				"Required pod affinity on %s: must be co-located with pods matching %s.",
				term.TopologyKey, describeSelector(term.LabelSelector)),
		})
	}
	return out
}

func topologySpreadConstraints(pod model.Pod, snap *model.ClusterSnapshot) []Constraint {
	var out []Constraint
	for _, tsc := range pod.TopologySpread {
		domains := map[string]struct{}{}
		for _, n := range snap.Nodes {
			if v, ok := n.Labels[tsc.TopologyKey]; ok {
				domains[v] = struct{}{}
			}
		}
		if tsc.IsHard() {
			out = append(out, Constraint{
				Kind:     KindTopologySpread,
				Subject:  tsc.TopologyKey,
				Hard:     true,
				Blocking: false,
				Explanation: fmt.Sprintf(
					"Topology spread across %s with maxSkew %d (DoNotSchedule) over %d domain(s): any move must keep the per-domain counts within %d of each other.",
					tsc.TopologyKey, tsc.MaxSkew, len(domains), tsc.MaxSkew),
			})
			continue
		}
		out = append(out, Constraint{
			Kind:     KindTopologySpread,
			Subject:  tsc.TopologyKey,
			Hard:     false,
			Blocking: false,
			Explanation: fmt.Sprintf(
				"Topology spread across %s with maxSkew %d (ScheduleAnyway): a preference, so it cannot block a move.",
				tsc.TopologyKey, tsc.MaxSkew),
		})
	}
	return out
}

// taintToleranceConstraint records tolerations that unlock otherwise
// unreachable nodes. Universal tolerations are skipped: reporting "this pod
// tolerates everything" as a placement constraint is noise.
func taintToleranceConstraint(pod model.Pod, snap *model.ClusterSnapshot) (Constraint, bool) {
	unlocked := map[string]struct{}{}
	for _, n := range snap.Nodes {
		for _, taint := range n.Taints {
			if taint.Effect == model.TaintEffectPreferNoSchedule {
				continue
			}
			for _, tol := range pod.Tolerations {
				if tol.Key != "" && tol.ToleratesTaint(taint) {
					unlocked[taint.Key+valueSuffix(taint.Value)] = struct{}{}
				}
			}
		}
	}
	if len(unlocked) == 0 {
		return Constraint{}, false
	}
	keys := make([]string, 0, len(unlocked))
	for k := range unlocked {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	return Constraint{
		Kind:     KindTaint,
		Subject:  strings.Join(keys, ","),
		Hard:     false,
		Blocking: false,
		Explanation: fmt.Sprintf(
			"Tolerates %s, so it may be placed on nodes carrying those taints.",
			strings.Join(keys, ", ")),
	}, true
}

func indexPDBs(snap *model.ClusterSnapshot) map[string][]model.PodDisruptionBudget {
	out := make(map[string][]model.PodDisruptionBudget)
	for _, pdb := range snap.PDBs {
		out[pdb.Namespace] = append(out[pdb.Namespace], pdb)
	}
	return out
}

func describeSelector(s *model.LabelSelector) string {
	if s.IsEmpty() {
		return "any pod"
	}
	parts := make([]string, 0, len(s.MatchLabels)+len(s.MatchExpressions))
	for k, v := range s.MatchLabels {
		parts = append(parts, k+"="+v)
	}
	for _, e := range s.MatchExpressions {
		parts = append(parts, fmt.Sprintf("%s %s %v", e.Key, strings.ToLower(string(e.Operator)), e.Values))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func describeTerms(terms []model.NodeSelectorTerm) string {
	parts := make([]string, 0, len(terms))
	for _, t := range terms {
		sub := make([]string, 0, len(t.MatchExpressions))
		for _, e := range t.MatchExpressions {
			if len(e.Values) == 0 {
				sub = append(sub, fmt.Sprintf("%s %s", e.Key, strings.ToLower(string(e.Operator))))
				continue
			}
			sub = append(sub, fmt.Sprintf("%s %s %v", e.Key, strings.ToLower(string(e.Operator)), e.Values))
		}
		sort.Strings(sub)
		parts = append(parts, strings.Join(sub, " and "))
	}
	sort.Strings(parts)
	return strings.Join(parts, " or ")
}

func shortTopology(key string) string {
	switch key {
	case model.LabelHostname:
		return "node"
	case model.LabelZone:
		return "zone"
	case model.LabelRegion:
		return "region"
	default:
		return key
	}
}
