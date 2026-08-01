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
					worst: step.Impact,
				}
				groups[key] = g
				order = append(order, key)
			}
			g.steps[step.SequenceNumber] = true
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
}

// Build derives recommendations from a snapshot and nothing else — same
// purity contract as the analyzer, so it can run against any stored moment.
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
		replicas := len(w.pods)
		sample := w.pods[0]

		// Multi-replica workload with no PDB: every eviction is currently
		// ungoverned, which makes every step touching it look riskier than
		// its owner probably intends.
		if replicas >= 2 && !covered(sample) {
			out = append(out, Recommendation{
				Kind: "MissingPDB", Severity: SeverityMedium, Workload: key,
				Why: fmt.Sprintf(
					"%d replicas and no PodDisruptionBudget: evictions are ungoverned, so the API server cannot pace a drain for this workload.",
					replicas),
				Fix: fmt.Sprintf(`apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: %s
  namespace: %s
spec:
  maxUnavailable: 1
  selector:
    matchLabels:
      # match your workload's pod labels
      app: %s`, w.name, w.namespace, sample.Labels["app"]),
			})
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
