// Package recommend turns the analyzer's observations into fixes.
//
// audit answers "what cannot survive a node loss"; preflight answers "will a
// rotation wedge". Both report what IS. This package reports what is MISSING
// — the PDB nobody wrote, the second replica nobody added, the requests
// nobody set — because the analyzer sees all of it on every cycle and has
// never once said what to do about it.
//
// Every recommendation carries a why in one sentence and, where a fix is a
// piece of YAML, the YAML — paste-ready, minimal, and honest about being a
// starting point rather than a policy.
package recommend

import (
	"fmt"
	"sort"
	"strings"

	"github.com/atedgimo/k8s-dencer/internal/model"
)

// Queue is what the Recommendations destination shows: the plan's own
// blocking rules first — each one derived from the step reasons the impact
// assessor already wrote, grouped by rule and responsible workload, carrying
// exactly the steps it appears on — then Build's advice underneath. The
// rank is nodes unlocked (one step drains one node), which is what turns 29
// findings into a work queue.
//
// A blocking rule's step list is never speculative: it is the impact
// assessor's attribution read back, not this package guessing. Advice kinds
// (MissingPDB, MissingRequests, HandsOff, and any finding whose subject
// appears on no step) rank below every blocker.
func Queue(plan *model.Plan, snap *model.ClusterSnapshot) []Recommendation {
	blockers := fromPlanReasons(plan, snap)
	advice := Build(snap)

	// A blocker and an advice finding can name the same workload for
	// different reasons (a StatefulWorkload rule and a SingleReplica advice
	// on the same pod). Both stay: they propose different actions.
	out := append(blockers, advice...)

	sort.SliceStable(out, func(i, j int) bool {
		if len(out[i].UnblocksSteps) != len(out[j].UnblocksSteps) {
			return len(out[i].UnblocksSteps) > len(out[j].UnblocksSteps)
		}
		rank := map[Severity]int{SeverityHigh: 0, SeverityMedium: 1, SeverityInfo: 2}
		if rank[out[i].Severity] != rank[out[j].Severity] {
			return rank[out[i].Severity] < rank[out[j].Severity]
		}
		return out[i].Workload < out[j].Workload
	})
	return out
}

// fromPlanReasons turns step reasons into one finding per (rule, workload):
// "HardTopologySpread on shop/Deployment/web holds back steps 3 and 4". The
// subject workload comes from resolving the reason's subject pod against the
// snapshot; a reason whose subject is a node (BlastRadius) groups by node.
func fromPlanReasons(plan *model.Plan, snap *model.ClusterSnapshot) []Recommendation {
	if plan == nil || snap == nil {
		return nil
	}

	ownerOf := make(map[string]string, len(snap.Pods)) // pod key → workload key
	for i := range snap.Pods {
		p := &snap.Pods[i]
		if p.Owner != nil {
			ownerOf[p.Key()] = p.Namespace + "/" + p.Owner.Kind + "/" + p.Owner.Name
		}
	}

	type group struct {
		rec   Recommendation
		steps map[int]bool
		nodes map[string]bool
		worst model.ImpactRating
	}
	groups := map[string]*group{}
	order := []string{}

	for _, step := range plan.Steps {
		if step.Impact == model.ImpactGreen {
			continue
		}
		for _, r := range step.Reasons {
			subject := r.Subject
			if w, ok := ownerOf[subject]; ok {
				subject = w
			}
			key := r.Kind + "|" + subject
			g := groups[key]
			if g == nil {
				g = &group{
					rec: Recommendation{
						Kind:     r.Kind,
						Workload: subject,
						Why:      r.Detail,
					},
					steps: map[int]bool{},
					nodes: map[string]bool{},
					worst: step.Impact,
				}
				groups[key] = g
				order = append(order, key)
			}
			g.steps[step.SequenceNumber] = true
			if step.TargetNode != "" {
				g.nodes[step.TargetNode] = true
			}
			if g.rec.Why == "" {
				g.rec.Why = r.Detail
			}
			if step.Impact == model.ImpactRed {
				g.worst = model.ImpactRed
			}
		}
	}

	out := make([]Recommendation, 0, len(groups))
	for _, key := range order {
		g := groups[key]
		// Red-rated attribution blocks outright; Yellow asks for a call.
		if g.worst == model.ImpactRed {
			g.rec.Severity = SeverityHigh
		} else {
			g.rec.Severity = SeverityMedium
		}
		for seq := range g.steps {
			g.rec.UnblocksSteps = append(g.rec.UnblocksSteps, seq)
		}
		sort.Ints(g.rec.UnblocksSteps)
		g.rec.Pools = poolsOf(g.nodes, snap)
		out = append(out, g.rec)
	}
	return out
}

// Severity is impact-on-consolidation, not risk: these are chores, not
// alarms, and the UI must not colour them like ratings.
type Severity string

const (
	// SeverityHigh: this blocks consolidation (or availability) today.
	SeverityHigh Severity = "high"
	// SeverityMedium: this degrades packing or resilience.
	SeverityMedium Severity = "medium"
	// SeverityInfo: not a problem — an explanation of a deliberate state.
	SeverityInfo Severity = "info"
)

type Recommendation struct {
	Kind     string   `json:"kind"`
	Severity Severity `json:"severity"`
	// Workload is namespace/Kind/name — the thing an operator recognises.
	Workload string `json:"workload"`
	Why      string `json:"why"`
	// Fix is paste-ready YAML when the fix is YAML, empty when it is a
	// decision. Suggestions, not policy: the numbers are starting points.
	Fix string `json:"fix,omitempty"`

	// UnblocksSteps names the plan steps this finding holds back, by
	// sequence number — the linkage that turns a findings list into an
	// ordered work queue ("fixing this unblocks 3 steps"). Only Queue sets
	// it, because only the plan knows; Build stays snapshot-pure. Empty
	// means the finding is advice, not a blocker.
	UnblocksSteps []int `json:"unblocksSteps,omitempty"`

	// Pools names where those steps would reclaim capacity.
	//
	// "This PDB blocks three steps" is a quantity; "this PDB blocks three
	// steps on your spot pool" is a decision, because spot is the capacity
	// an operator is most willing to move work off and least willing to hold
	// open for a budget written without thinking about it.
	//
	// Grouped by whatever the nodes actually say. A cluster with node-pool
	// labels groups by pool and names its capacity type; a cluster with no
	// pool labels but known capacity — the KWOK fabric, and any self-managed
	// cluster — still groups by spot versus on-demand, because that is the
	// half the operator acts on. Nothing known about a node means no entry:
	// unlabelled is not a pool called "unknown", the same way an unpriced
	// node is not a free one.
	Pools []BlockedPool `json:"pools,omitempty"`
}

// BlockedPool is one group of nodes a finding holds capacity in.
type BlockedPool struct {
	// Name is the node-pool label, empty on a cluster that has none. An
	// entry with no name is still worth showing when its capacity type is
	// known — "2 spot" answers the question a pool name only decorates.
	Name string `json:"name,omitempty"`
	// CapacityType is "spot" or "on-demand", empty when the node says
	// neither. Never guessed: spot is never reported as on-demand, and a
	// node no cloud has labelled is reported as neither rather than as the
	// cheaper-sounding one.
	CapacityType string `json:"capacityType,omitempty"`
	// Nodes in this group that the finding holds back.
	Nodes int `json:"nodes"`
}

// poolsOf resolves blocked node names to the groups they belong to.
//
// Ordered most-nodes-first, then by name, so the group a finding costs most
// reads first and the order is stable across cycles.
func poolsOf(nodes map[string]bool, snap *model.ClusterSnapshot) []BlockedPool {
	if len(nodes) == 0 || snap == nil {
		return nil
	}
	byNode := make(map[string]*model.Node, len(snap.Nodes))
	for i := range snap.Nodes {
		byNode[snap.Nodes[i].Name] = &snap.Nodes[i]
	}

	seen := map[BlockedPool]int{}
	for name := range nodes {
		n := byNode[name]
		if n == nil {
			continue
		}
		key := BlockedPool{Name: n.Pool(), CapacityType: n.CapacityType()}
		if key.Name == "" && key.CapacityType == "" {
			// The node says nothing about where it belongs or how it is
			// bought. An entry here would be a row that answers no question.
			continue
		}
		seen[key]++
	}

	out := make([]BlockedPool, 0, len(seen))
	for key, count := range seen {
		key.Nodes = count
		out = append(out, key)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Nodes != out[j].Nodes {
			return out[i].Nodes > out[j].Nodes
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].CapacityType < out[j].CapacityType
	})
	return out
}

// Build derives recommendations from a snapshot and nothing else — same
// purity contract as the analyzer, so it can run against any stored moment.
// platformNamespaces are reconciled by the cluster's provider, not its
// operator. Scaling kube-dns-autoscaler or adding a PDB to l7-default-backend
// is not a change anyone can make: the platform controller reverts it.
//
// A real GKE cluster produced fifteen recommendations, eleven of them HIGH, of
// which exactly one named a workload the operator owned. Advice nobody can act
// on is not advice.
var platformNamespaces = map[string]bool{
	"kube-system":     true,
	"kube-public":     true,
	"kube-node-lease": true,
	"gmp-system":      true,
	"gmp-public":      true,
}

// notYours reports whether a workload belongs to the platform or to this
// product, rather than to the operator reading the report.
//
// The prefix match covers the managed namespaces every provider invents —
// gke-managed-cim, gke-managed-system and their equivalents — without
// pretending to enumerate them.
//
// k8s-dencer's own components are excluded for a different reason: the chart
// owns their replica counts deliberately. ui-backend is pinned to one replica
// because SQLite has one writer, and the chart's contract test rejects a
// second. Recommending one would be advising a change the product itself
// refuses to install.
func notYours(namespace string, sample *model.Pod) bool {
	if platformNamespaces[namespace] {
		return true
	}
	for _, prefix := range []string{"gke-managed-", "gke-system", "aks-", "eks-", "openshift-"} {
		if strings.HasPrefix(namespace, prefix) {
			return true
		}
	}
	return sample != nil && sample.Labels["app.kubernetes.io/part-of"] == "k8s-dencer"
}

// selectorLabel picks a label that plausibly identifies a workload's pods, in
// the order the ecosystem actually uses them.
//
// pod-template-hash is deliberately absent: it changes on every rollout, so a
// PDB written against it silently stops matching the moment anyone deploys.
func selectorLabel(p *model.Pod) (struct{ key, value string }, bool) {
	for _, k := range []string{"app", "app.kubernetes.io/name", "k8s-app", "component", "name"} {
		if v := p.Labels[k]; v != "" {
			return struct{ key, value string }{k, v}, true
		}
	}
	return struct{ key, value string }{}, false
}

func Build(snap *model.ClusterSnapshot) []Recommendation {
	out := []Recommendation{}

	type workload struct {
		namespace, kind, name string
		pods                  []*model.Pod
	}
	byOwner := map[string]*workload{}
	for i := range snap.Pods {
		p := &snap.Pods[i]
		if p.Terminating || p.Owner == nil {
			continue
		}
		key := p.Namespace + "/" + p.Owner.Kind + "/" + p.Owner.Name
		w := byOwner[key]
		if w == nil {
			w = &workload{namespace: p.Namespace, kind: p.Owner.Kind, name: p.Owner.Name}
			byOwner[key] = w
		}
		w.pods = append(w.pods, p)
	}

	covered := func(p *model.Pod) bool {
		for _, pdb := range snap.PDBs {
			if pdb.Namespace == p.Namespace && pdb.Selector.Matches(p.Labels) {
				return true
			}
		}
		return false
	}

	for key, w := range byOwner {
		if w.kind == "DaemonSet" {
			continue // pinned by design; a PDB or replicas advice is noise
		}
		if notYours(w.namespace, w.pods[0]) {
			continue
		}
		replicas := len(w.pods)
		sample := w.pods[0]

		// Multi-replica workload with no PDB: every eviction is currently
		// ungoverned, which makes every step touching it look riskier than
		// its owner probably intends.
		if replicas >= 2 && !covered(sample) {
			rec := Recommendation{
				Kind: "MissingPDB", Severity: SeverityMedium, Workload: key,
				Why: fmt.Sprintf(
					"%d replicas and no PodDisruptionBudget: evictions are ungoverned, so the API server cannot pace a drain for this workload.",
					replicas),
			}
			// Only emit YAML when the selector is real. Guessing `app:` and
			// finding nothing produced manifests reading `app: ` with no
			// value — a PDB that matches no pods, which is worse than none
			// because it looks like coverage.
			if sel, ok := selectorLabel(sample); ok {
				rec.Fix = fmt.Sprintf(`apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: %s
  namespace: %s
spec:
  maxUnavailable: 1
  selector:
    matchLabels:
      # check this matches every pod of this workload, and only those
      %s: %s`, w.name, w.namespace, sel.key, sel.value)
			} else {
				rec.Why += " No single label identifies its pods, so write the selector by hand rather than trusting a generated one."
			}
			out = append(out, rec)
		}

		// Single replica: any eviction is downtime, and the impact
		// classifier will keep rating it accordingly forever.
		if replicas == 1 && (w.kind == "Deployment" || w.kind == "ReplicaSet" || w.kind == "StatefulSet") {
			out = append(out, Recommendation{
				Kind: "SingleReplica", Severity: SeverityHigh, Workload: key,
				Why: "One replica: every eviction — voluntary or a node dying — is downtime. " +
					"A second replica turns drains from outages into non-events.",
			})
		}

		// Missing requests: the packing math is blind to this workload, and
		// so is every scheduler decision about it.
		if sample.Requests.MilliCPU == 0 && sample.Requests.MemoryBytes == 0 {
			fix := ""
			if sample.Usage != nil && (sample.Usage.MilliCPU > 0 || sample.Usage.MemoryBytes > 0) {
				fix = fmt.Sprintf(`resources:
  requests:
    # measured just now — set from your own percentiles, not one sample
    cpu: %dm
    memory: %dMi`, sample.Usage.MilliCPU, sample.Usage.MemoryBytes>>20)
			}
			out = append(out, Recommendation{
				Kind: "MissingRequests", Severity: SeverityHigh, Workload: key,
				Why: "No resource requests: the scheduler and every capacity number in this product " +
					"are blind to what this workload needs.",
				Fix: fix,
			})
		}

		// Hands-off annotation: deliberate, so informational — but worth a
		// line, because six months later nobody remembers why nothing moves.
		if sample.DoNotDisrupt {
			out = append(out, Recommendation{
				Kind: "HandsOff", Severity: SeverityInfo, Workload: key,
				Why: "Annotated do-not-disrupt (or safe-to-evict: \"false\"): this workload will " +
					"never be moved by k8s-dencer or the autoscalers. Deliberate — and worth " +
					"remembering when this node shows as undrainable.",
			})
		}
	}

	// Zero-headroom PDBs, workload or not: the budget that blocks every
	// drain and is violated by any node loss.
	for _, pdb := range snap.PDBs {
		if pdb.Blocks() {
			out = append(out, Recommendation{
				Kind: "ZeroHeadroomPDB", Severity: SeverityHigh,
				Workload: pdb.Namespace + "/PodDisruptionBudget/" + pdb.Name,
				Why: fmt.Sprintf(
					"Allows 0 disruptions (%d healthy, %d required): every drain is refused and any node loss violates it. Add a replica or lower the requirement.",
					pdb.CurrentHealthy, pdb.DesiredHealthy),
			})
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		rank := map[Severity]int{SeverityHigh: 0, SeverityMedium: 1, SeverityInfo: 2}
		if rank[out[i].Severity] != rank[out[j].Severity] {
			return rank[out[i].Severity] < rank[out[j].Severity]
		}
		return out[i].Workload < out[j].Workload
	})
	return out
}
