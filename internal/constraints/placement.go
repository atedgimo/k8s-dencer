package constraints

import (
	"fmt"

	"github.com/atedgimo/k8s-dencer/internal/model"
)

// Placement is a mutable working assignment of pods to nodes.
//
// It exists so the planner can ask "could this pod go there?" against a
// hypothetical packing rather than only against live cluster state. The
// feasibility rules live here rather than in the packer so that the analyzer's
// explanations and the packer's decisions are computed by the same code — an
// explanation that disagrees with the plan is worse than no explanation.
//
// Not safe for concurrent mutation. Clone per candidate packing.
type Placement struct {
	nodes     map[string]*nodeState
	nodeOrder []string
	// spread memoises per-domain pod counts for topology spread checks. See
	// spreadindex.go — recomputing them per candidate node was the cubic term.
	spread *spreadIndex
}

type nodeState struct {
	node      model.Node
	free      model.Resources
	occupants []model.Pod
}

// NewPlacement builds a working assignment from a snapshot's current state.
func NewPlacement(snap *model.ClusterSnapshot) *Placement {
	return NewPlacementCeiling(snap, 0)
}

// NewPlacementCeiling is NewPlacement with a packing ceiling: each node's
// capacity basis becomes ceiling × allocatable (CPU and memory; the pod
// count stays whole — it is a kubelet limit, not a utilisation target).
// Nothing sane packs production nodes to 100%: a node with zero headroom
// cannot absorb a burst, a failed neighbour, or the next rollout's surge.
// A ceiling of 0, or ≥ 1, means the full allocatable.
//
// A node already above its ceiling simply accepts nothing new — free goes
// negative and every CanPlace fails — which is the correct reading: it has
// no headroom to sell.
func NewPlacementCeiling(snap *model.ClusterSnapshot, ceiling float64) *Placement {
	p := &Placement{
		spread:    newSpreadIndex(),
		nodes:     make(map[string]*nodeState, len(snap.Nodes)),
		nodeOrder: make([]string, 0, len(snap.Nodes)),
	}
	for _, n := range snap.Nodes {
		free := n.Allocatable
		if ceiling > 0 && ceiling < 1 {
			free.MilliCPU = int64(float64(free.MilliCPU) * ceiling)
			free.MemoryBytes = int64(float64(free.MemoryBytes) * ceiling)
		}
		p.nodes[n.Name] = &nodeState{node: n, free: free}
		p.nodeOrder = append(p.nodeOrder, n.Name)
	}
	for _, pod := range snap.Pods {
		if pod.NodeName == "" {
			continue
		}
		ns, ok := p.nodes[pod.NodeName]
		if !ok {
			continue
		}
		ns.occupants = append(ns.occupants, pod)
		ns.free = ns.free.Sub(pod.Requests.Add(model.Resources{Pods: 1}))
	}
	return p
}

// Clone returns an independent copy, so a candidate packing can be explored
// without disturbing the caller's state.
func (p *Placement) Clone() *Placement {
	out := &Placement{
		nodes:     make(map[string]*nodeState, len(p.nodes)),
		nodeOrder: append([]string(nil), p.nodeOrder...),
		// Cloned rather than shared: a trial placement must not write its
		// speculative counts back into the placement it branched from.
		spread: p.spread.clone(),
	}
	for name, ns := range p.nodes {
		out.nodes[name] = &nodeState{
			node:      ns.node,
			free:      ns.free,
			occupants: append([]model.Pod(nil), ns.occupants...),
		}
	}
	return out
}

// NodeNames returns node names in snapshot order.
func (p *Placement) NodeNames() []string { return p.nodeOrder }

// Free returns remaining allocatable capacity on a node.
func (p *Placement) Free(nodeName string) model.Resources {
	if ns, ok := p.nodes[nodeName]; ok {
		return ns.free
	}
	return model.Resources{}
}

// Occupants returns the pods currently assigned to a node.
func (p *Placement) Occupants(nodeName string) []model.Pod {
	if ns, ok := p.nodes[nodeName]; ok {
		return ns.occupants
	}
	return nil
}

// IsEmpty reports whether a node holds no movable workload. DaemonSet pods do
// not count: they are recreated on every node regardless, so a node carrying
// only DaemonSet pods is still reclaimable.
func (p *Placement) IsEmpty(nodeName string) bool {
	for _, pod := range p.Occupants(nodeName) {
		if pod.IsMovable() {
			return false
		}
	}
	return true
}

// Place assigns a pod to a node without checking feasibility. Call CanPlace
// first unless deliberately reconstructing a known-good state.
func (p *Placement) Place(pod model.Pod, nodeName string) {
	ns, ok := p.nodes[nodeName]
	if !ok {
		return
	}
	pod.NodeName = nodeName
	ns.occupants = append(ns.occupants, pod)
	ns.free = ns.free.Sub(pod.Requests.Add(model.Resources{Pods: 1}))
	p.adjustSpread(pod, nodeName, +1)
}

// Remove unassigns a pod from its current node, returning its freed resources.
func (p *Placement) Remove(pod model.Pod) {
	ns, ok := p.nodes[pod.NodeName]
	if !ok {
		return
	}
	for i, occupant := range ns.occupants {
		if occupant.Namespace == pod.Namespace && occupant.Name == pod.Name {
			node := pod.NodeName
			ns.occupants = append(ns.occupants[:i], ns.occupants[i+1:]...)
			ns.free = ns.free.Add(pod.Requests).Add(model.Resources{Pods: 1})
			// Matched on the occupant's own labels, not the caller's copy:
			// Place stores the pod as given, and a caller passing a
			// differently-labelled copy to Remove would otherwise leave the
			// index counting a pod that is no longer there.
			p.adjustSpread(occupant, node, -1)
			return
		}
	}
}

// CanPlace reports whether pod may legally occupy node, and if not, a
// human-readable reason.
//
// The checks mirror the scheduler's hard predicates in roughly the order the
// scheduler applies them, cheapest first. Only hard constraints are evaluated:
// a preference the scheduler would happily violate must never make the planner
// declare a move impossible.
func (p *Placement) CanPlace(pod model.Pod, nodeName string) (bool, string) {
	ns, ok := p.nodes[nodeName]
	if !ok {
		return false, fmt.Sprintf("Node %s is not in the snapshot.", nodeName)
	}
	node := ns.node

	if node.Unschedulable {
		return false, fmt.Sprintf("Node %s is cordoned.", nodeName)
	}
	if !node.Ready {
		return false, fmt.Sprintf("Node %s is not Ready.", nodeName)
	}

	if ok, reason := fitsTaints(pod, node); !ok {
		return false, reason
	}
	if ok, reason := fitsNodeSelector(pod, node); !ok {
		return false, reason
	}
	if ok, reason := fitsNodeAffinity(pod, node); !ok {
		return false, reason
	}
	if ok, reason := p.fitsResources(pod, ns); !ok {
		return false, reason
	}
	if ok, reason := p.fitsPodAntiAffinity(pod, ns); !ok {
		return false, reason
	}
	if ok, reason := p.fitsPodAffinity(pod, ns); !ok {
		return false, reason
	}
	if ok, reason := p.fitsTopologySpread(pod, nodeName); !ok {
		return false, reason
	}
	return true, ""
}

// CandidateNodes lists every node other than the pod's current one that could
// accept it.
func (p *Placement) CandidateNodes(pod model.Pod) []string {
	var out []string
	for _, name := range p.nodeOrder {
		if name == pod.NodeName {
			continue
		}
		if ok, _ := p.CanPlace(pod, name); ok {
			out = append(out, name)
		}
	}
	return out
}

func fitsTaints(pod model.Pod, node model.Node) (bool, string) {
	for _, taint := range node.Taints {
		// PreferNoSchedule is a soft signal; the scheduler will still place
		// here when it must, so it cannot block a move.
		if taint.Effect == model.TaintEffectPreferNoSchedule {
			continue
		}
		tolerated := false
		for _, tol := range pod.Tolerations {
			if tol.ToleratesTaint(taint) {
				tolerated = true
				break
			}
		}
		if !tolerated {
			return false, fmt.Sprintf("Node %s carries taint %s%s:%s which this pod does not tolerate.",
				node.Name, taint.Key, valueSuffix(taint.Value), taint.Effect)
		}
	}
	return true, ""
}

func fitsNodeSelector(pod model.Pod, node model.Node) (bool, string) {
	for k, v := range pod.NodeSelector {
		if node.Labels[k] != v {
			return false, fmt.Sprintf("Node %s does not carry the required label %s=%s.", node.Name, k, v)
		}
	}
	return true, ""
}

func fitsNodeAffinity(pod model.Pod, node model.Node) (bool, string) {
	if pod.NodeAffinity == nil || len(pod.NodeAffinity.RequiredTerms) == 0 {
		return true, ""
	}
	// Terms are OR-ed: satisfying any one is enough.
	for _, term := range pod.NodeAffinity.RequiredTerms {
		sel := model.LabelSelector{MatchExpressions: term.MatchExpressions}
		if sel.Matches(node.Labels) {
			return true, ""
		}
	}
	return false, fmt.Sprintf("Node %s does not satisfy the pod's required node affinity.", node.Name)
}

func (p *Placement) fitsResources(pod model.Pod, ns *nodeState) (bool, string) {
	need := pod.Requests.Add(model.Resources{Pods: 1})
	if need.Fits(ns.free) {
		return true, ""
	}
	return false, fmt.Sprintf("Node %s has %s free, which cannot hold this pod's %s.",
		ns.node.Name, ns.free, pod.Requests)
}

func (p *Placement) fitsPodAntiAffinity(pod model.Pod, ns *nodeState) (bool, string) {
	if pod.PodAffinity == nil {
		return true, ""
	}
	for _, term := range pod.PodAffinity.RequiredAntiAffinity {
		for _, occupant := range ns.occupants {
			if sameKey(pod, occupant) {
				continue
			}
			if !p.inSameTopologyDomain(ns.node.Name, occupant.NodeName, term.TopologyKey) {
				continue
			}
			if !termMatchesPod(term, occupant, pod.Namespace) {
				continue
			}
			return false, fmt.Sprintf(
				"Required anti-affinity on %s: %s already runs there and matches this pod's selector.",
				term.TopologyKey, occupant.Key())
		}
		// Anti-affinity applies across the whole topology domain, not just the
		// single node, so a zone-scoped rule must consider sibling nodes too.
		if term.TopologyKey != model.LabelHostname {
			if conflict := p.conflictInDomain(pod, ns.node, term); conflict != "" {
				return false, conflict
			}
		}
	}
	return true, ""
}

func (p *Placement) conflictInDomain(pod model.Pod, node model.Node, term model.PodAffinityTerm) string {
	domain, ok := node.Labels[term.TopologyKey]
	if !ok {
		return ""
	}
	for _, name := range p.nodeOrder {
		if name == node.Name {
			continue
		}
		other := p.nodes[name]
		if other.node.Labels[term.TopologyKey] != domain {
			continue
		}
		for _, occupant := range other.occupants {
			if sameKey(pod, occupant) {
				continue
			}
			if termMatchesPod(term, occupant, pod.Namespace) {
				return fmt.Sprintf(
					"Required anti-affinity on %s=%s: %s already runs in that domain.",
					term.TopologyKey, domain, occupant.Key())
			}
		}
	}
	return ""
}

func (p *Placement) fitsPodAffinity(pod model.Pod, ns *nodeState) (bool, string) {
	if pod.PodAffinity == nil {
		return true, ""
	}
	for _, term := range pod.PodAffinity.RequiredAffinity {
		found := false
		domain, hasDomain := ns.node.Labels[term.TopologyKey]
		for _, name := range p.nodeOrder {
			other := p.nodes[name]
			if term.TopologyKey == model.LabelHostname {
				if name != ns.node.Name {
					continue
				}
			} else if !hasDomain || other.node.Labels[term.TopologyKey] != domain {
				continue
			}
			for _, occupant := range other.occupants {
				if sameKey(pod, occupant) {
					continue
				}
				if termMatchesPod(term, occupant, pod.Namespace) {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			return false, fmt.Sprintf(
				"Required pod affinity on %s is not satisfied at node %s: no matching pod in that domain.",
				term.TopologyKey, ns.node.Name)
		}
	}
	return true, ""
}

// fitsTopologySpread checks hard spread constraints by simulating the
// placement: counting the domain as it would be *after* the pod lands, which
// is what the scheduler does.
func (p *Placement) fitsTopologySpread(pod model.Pod, nodeName string) (bool, string) {
	for _, tsc := range pod.TopologySpread {
		if !tsc.IsHard() {
			continue
		}
		target, ok := p.nodes[nodeName]
		if !ok {
			continue
		}
		targetDomain, ok := target.node.Labels[tsc.TopologyKey]
		if !ok {
			// A node with no value for the topology key cannot satisfy a hard
			// spread constraint.
			return false, fmt.Sprintf("Node %s has no %s label, required by a topology spread constraint.",
				nodeName, tsc.TopologyKey)
		}

		counts := p.domainCounts(pod, tsc)
		counts[targetDomain]++ // the candidate placement

		minCount := -1
		for _, c := range counts {
			if minCount == -1 || c < minCount {
				minCount = c
			}
		}
		if minCount < 0 {
			minCount = 0
		}
		if skew := counts[targetDomain] - minCount; int32(skew) > tsc.MaxSkew {
			return false, fmt.Sprintf(
				"Topology spread on %s would reach skew %d in domain %q, exceeding maxSkew %d.",
				tsc.TopologyKey, skew, targetDomain, tsc.MaxSkew)
		}
	}
	return true, ""
}

// domainCounts tallies matching pods per topology domain, excluding the pod
// being placed. Every domain present in the cluster is seeded at zero so an
// empty domain still counts toward the skew calculation.
//
// Served from the memoised index rather than by walking the cluster. The pod
// under consideration is subtracted here rather than skipped during counting:
// the index is shared across candidate nodes, and which pod is excluded is the
// only part that varies per call.
func (p *Placement) domainCounts(pod model.Pod, tsc model.TopologySpreadConstraint) map[string]int {
	cached := p.spreadCounts(pod.Namespace, tsc)

	// Copied because the caller increments the candidate domain, and the index
	// must not absorb a speculative placement.
	counts := make(map[string]int, len(cached))
	for d, c := range cached {
		counts[d] = c
	}

	// If the pod is currently placed and matches its own selector, the index
	// counted it. It must not count toward the spread it is being moved out of.
	if ns, ok := p.nodes[pod.NodeName]; ok {
		if domain, ok := ns.node.Labels[tsc.TopologyKey]; ok {
			for _, occupant := range ns.occupants {
				if sameKey(pod, occupant) && tsc.LabelSelector.Matches(occupant.Labels) &&
					occupant.Namespace == pod.Namespace {
					counts[domain]--
					break
				}
			}
		}
	}
	return counts
}

func (p *Placement) inSameTopologyDomain(a, b, topologyKey string) bool {
	if topologyKey == model.LabelHostname {
		return a == b
	}
	na, okA := p.nodes[a]
	nb, okB := p.nodes[b]
	if !okA || !okB {
		return false
	}
	va, okA := na.node.Labels[topologyKey]
	vb, okB := nb.node.Labels[topologyKey]
	return okA && okB && va == vb
}

func termMatchesPod(term model.PodAffinityTerm, candidate model.Pod, defaultNamespace string) bool {
	namespaces := term.Namespaces
	if len(namespaces) == 0 {
		namespaces = []string{defaultNamespace}
	}
	inScope := false
	for _, ns := range namespaces {
		if ns == candidate.Namespace {
			inScope = true
			break
		}
	}
	if !inScope {
		return false
	}
	return term.LabelSelector.Matches(candidate.Labels)
}

func sameKey(a, b model.Pod) bool {
	return a.Namespace == b.Namespace && a.Name == b.Name
}

func valueSuffix(v string) string {
	if v == "" {
		return ""
	}
	return "=" + v
}
