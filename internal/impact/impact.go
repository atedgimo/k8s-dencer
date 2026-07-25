// Package impact rates consolidation steps Green, Yellow or Red and explains
// the rating.
//
// The rating is policy, not advice: architecture doc §5 confines Red steps to
// an approved maintenance window, and Phase 2's safety guard enforces that in
// code rather than trusting UI input validation. So a rating has to be
// defensible — which is why every one carries a rationale naming the specific
// object and number that drove it.
//
// Thresholds are configurable rather than hardcoded. Doc §10 is explicit that
// where PDB headroom stops being "Green" differs per cluster, and a product
// that decides that for you will be wrong for most people.
package impact

import (
	"fmt"
	"sort"
	"strings"

	"github.com/atedgimo/k8s-dencer/internal/constraints"
	"github.com/atedgimo/k8s-dencer/internal/model"
)

// Reason kinds. Stable identifiers: the UI filters on them and the agent
// reasons over them.
const (
	ReasonPDBZeroHeadroom  = "PDBZeroHeadroom"
	ReasonPDBTightHeadroom = "PDBTightHeadroom"
	ReasonUnmanagedPod     = "UnmanagedPod"
	ReasonStatefulWorkload = "StatefulWorkload"
	ReasonPersistentVolume = "PersistentVolume"
	ReasonAntiAffinity     = "RequiredAntiAffinity"
	ReasonTopologySpread   = "HardTopologySpread"
	ReasonBlastRadius      = "BlastRadius"
	ReasonBlastRadiusHigh  = "BlastRadiusHigh"
)

// Thresholds tune where one rating becomes the next.
type Thresholds struct {
	// YellowPodsMoved is the pod count above which a step stops being Green
	// purely on the size of its blast radius.
	YellowPodsMoved int
	// RedPodsMoved is the pod count above which a step is Red on size alone.
	RedPodsMoved int
	// TightPDBHeadroom is the disruption headroom at or below which a covering
	// PDB makes a step Yellow. Zero headroom is always Red regardless.
	TightPDBHeadroom int32
}

// DefaultThresholds are deliberately cautious. An operator who finds them
// noisy can loosen them; an operator surprised by an outage they were told was
// Green will not trust the tool again.
func DefaultThresholds() Thresholds {
	return Thresholds{
		YellowPodsMoved:  5,
		RedPodsMoved:     15,
		TightPDBHeadroom: 1,
	}
}

// Classifier rates steps against a set of thresholds.
type Classifier struct {
	Thresholds Thresholds
}

// New returns a classifier with the given thresholds, falling back to the
// defaults for any zero value.
func New(t Thresholds) Classifier {
	d := DefaultThresholds()
	if t.YellowPodsMoved <= 0 {
		t.YellowPodsMoved = d.YellowPodsMoved
	}
	if t.RedPodsMoved <= 0 {
		t.RedPodsMoved = d.RedPodsMoved
	}
	if t.TightPDBHeadroom <= 0 {
		t.TightPDBHeadroom = d.TightPDBHeadroom
	}
	return Classifier{Thresholds: t}
}

// ClassifyPlan rates every step in place.
func (c Classifier) ClassifyPlan(plan *model.Plan, snap *model.ClusterSnapshot, analysis *constraints.Analysis) {
	for i := range plan.Steps {
		rating, rationale, reasons := c.ClassifyStep(plan.Steps[i], snap, analysis)
		plan.Steps[i].Impact = rating
		plan.Steps[i].Rationale = rationale
		plan.Steps[i].Reasons = reasons
	}
}

// ClassifyStep rates a single step and explains the rating.
func (c Classifier) ClassifyStep(
	step model.PlanStep,
	snap *model.ClusterSnapshot,
	analysis *constraints.Analysis,
) (model.ImpactRating, string, []model.ImpactReason) {
	reasons := c.reasonsFor(step, snap, analysis)
	rating := ratingFor(reasons)
	return rating, c.rationale(step, rating, reasons), reasons
}

// severity maps a reason kind to the rating it forces. The step takes the
// highest severity of any reason found.
func severity(kind string) model.ImpactRating {
	switch kind {
	case ReasonPDBZeroHeadroom, ReasonUnmanagedPod, ReasonStatefulWorkload, ReasonBlastRadiusHigh:
		return model.ImpactRed
	case ReasonPDBTightHeadroom, ReasonPersistentVolume, ReasonAntiAffinity,
		ReasonTopologySpread, ReasonBlastRadius:
		return model.ImpactYellow
	default:
		return model.ImpactGreen
	}
}

// ratingFor takes the highest severity of any reason present.
func ratingFor(reasons []model.ImpactReason) model.ImpactRating {
	rating := model.ImpactGreen
	for _, r := range reasons {
		switch severity(r.Kind) {
		case model.ImpactRed:
			return model.ImpactRed
		case model.ImpactYellow:
			rating = model.ImpactYellow
		}
	}
	return rating
}

func (c Classifier) reasonsFor(
	step model.PlanStep,
	snap *model.ClusterSnapshot,
	analysis *constraints.Analysis,
) []model.ImpactReason {
	var reasons []model.ImpactReason
	seen := map[string]bool{}

	add := func(r model.ImpactReason) {
		key := r.Kind + "|" + r.Subject
		if seen[key] {
			return
		}
		seen[key] = true
		reasons = append(reasons, r)
	}

	pdbs := indexPDBs(snap)

	for _, move := range step.Moves {
		key := move.Namespace + "/" + move.Pod
		pod, found := findPod(snap, move.Namespace, move.Pod)
		if !found {
			continue
		}

		// A pod with no controller is not recreated after eviction — it is
		// simply gone. That is the most consequential thing a consolidation
		// step can do, so it outranks everything else.
		if pod.Owner == nil {
			add(model.ImpactReason{
				Kind:    ReasonUnmanagedPod,
				Subject: key,
				Detail: fmt.Sprintf(
					"%s has no controller. Evicting it deletes it permanently; nothing will recreate it.", key),
			})
		} else if pod.Owner.Kind == "StatefulSet" {
			add(model.ImpactReason{
				Kind:    ReasonStatefulWorkload,
				Subject: key,
				Detail: fmt.Sprintf(
					"%s belongs to StatefulSet %s. Its replacement keeps the same identity and cannot start until this one has fully terminated, so the workload loses that ordinal for the duration.",
					key, pod.Owner.Name),
			})
		}

		if pod.HasPersistentVol {
			add(model.ImpactReason{
				Kind:    ReasonPersistentVolume,
				Subject: key,
				Detail: fmt.Sprintf(
					"%s mounts a PersistentVolumeClaim, which may be bound to its current node's topology and unable to follow it to %s.",
					key, move.ToNode),
			})
		}

		// PDB headroom is the difference between a step that succeeds and one
		// the API server refuses outright, so it is read per covered pod.
		for _, pdb := range pdbs[pod.Namespace] {
			if pdb.Selector.IsEmpty() || !pdb.Selector.Matches(pod.Labels) {
				continue
			}
			switch {
			case pdb.Blocks():
				add(model.ImpactReason{
					Kind:    ReasonPDBZeroHeadroom,
					Subject: pdb.Key(),
					Detail: fmt.Sprintf(
						"PodDisruptionBudget %s allows 0 disruptions (%d healthy, %d required); the API server will refuse to evict %s.",
						pdb.Key(), pdb.CurrentHealthy, pdb.DesiredHealthy, key),
				})
			case pdb.DisruptionsAllowed <= c.Thresholds.TightPDBHeadroom:
				add(model.ImpactReason{
					Kind:    ReasonPDBTightHeadroom,
					Subject: pdb.Key(),
					Detail: fmt.Sprintf(
						"PodDisruptionBudget %s has only %d disruption(s) of headroom (%d healthy, %d required), so any concurrent disruption elsewhere will block this step.",
						pdb.Key(), pdb.DisruptionsAllowed, pdb.CurrentHealthy, pdb.DesiredHealthy),
				})
			}
		}

		// Reuse the analyzer's constraint set rather than re-deriving these:
		// its explanations are the canonical text, and re-deriving invites the
		// two to drift.
		if pc, ok := analysis.ForPod(key); ok {
			for _, con := range pc.Of(constraints.KindPodAntiAffinity) {
				add(model.ImpactReason{
					Kind:    ReasonAntiAffinity,
					Subject: key,
					Detail:  con.Explanation,
				})
			}
			for _, con := range pc.Of(constraints.KindTopologySpread) {
				if !con.Hard {
					continue
				}
				add(model.ImpactReason{
					Kind:    ReasonTopologySpread,
					Subject: key,
					Detail:  con.Explanation,
				})
			}
		}
	}

	if n := len(step.Moves); n >= c.Thresholds.RedPodsMoved {
		add(model.ImpactReason{
			Kind:    ReasonBlastRadiusHigh,
			Subject: step.TargetNode,
			Detail: fmt.Sprintf("Moves %d pods at once, at or above the %d-pod red threshold.",
				n, c.Thresholds.RedPodsMoved),
		})
	} else if n >= c.Thresholds.YellowPodsMoved {
		add(model.ImpactReason{
			Kind:    ReasonBlastRadius,
			Subject: step.TargetNode,
			Detail: fmt.Sprintf("Moves %d pods at once, at or above the %d-pod yellow threshold.",
				n, c.Thresholds.YellowPodsMoved),
		})
	}

	// Stable order so the rationale and the UI list do not reshuffle between
	// identical runs.
	sort.SliceStable(reasons, func(i, j int) bool {
		si, sj := severityRank(reasons[i].Kind), severityRank(reasons[j].Kind)
		if si != sj {
			return si > sj
		}
		if reasons[i].Kind != reasons[j].Kind {
			return reasons[i].Kind < reasons[j].Kind
		}
		return reasons[i].Subject < reasons[j].Subject
	})
	return reasons
}

func severityRank(kind string) int {
	switch severity(kind) {
	case model.ImpactRed:
		return 2
	case model.ImpactYellow:
		return 1
	default:
		return 0
	}
}

// rationale composes the single canonical explanation for a rating.
//
// It leads with the reason that drove the rating, because that is the question
// an operator is actually asking, then adds the supporting factors. The UI and
// the Kagent agent both surface this exact string.
func (c Classifier) rationale(step model.PlanStep, rating model.ImpactRating, reasons []model.ImpactReason) string {
	pods := len(step.Moves)
	head := fmt.Sprintf("Draining %s moves %d pod(s).", step.TargetNode, pods)

	if len(reasons) == 0 {
		return head + " No disruption-budget, stateful, affinity or spread constraints apply, so this step is safe to run at any time."
	}

	driving := reasons[0]
	var b strings.Builder
	b.WriteString(head)
	b.WriteString(" Rated ")
	b.WriteString(string(rating))
	b.WriteString(" because: ")
	b.WriteString(driving.Detail)

	if len(reasons) > 1 {
		b.WriteString(" Also: ")
		rest := make([]string, 0, len(reasons)-1)
		for _, r := range reasons[1:] {
			rest = append(rest, strings.TrimSuffix(r.Detail, "."))
		}
		b.WriteString(strings.Join(rest, "; "))
		b.WriteString(".")
	}

	if rating == model.ImpactRed {
		b.WriteString(" Red steps may only execute inside an approved maintenance window.")
	}
	return b.String()
}

func indexPDBs(snap *model.ClusterSnapshot) map[string][]model.PodDisruptionBudget {
	out := make(map[string][]model.PodDisruptionBudget)
	for _, pdb := range snap.PDBs {
		out[pdb.Namespace] = append(out[pdb.Namespace], pdb)
	}
	return out
}

func findPod(snap *model.ClusterSnapshot, namespace, name string) (model.Pod, bool) {
	for _, p := range snap.Pods {
		if p.Namespace == namespace && p.Name == name {
			return p, true
		}
	}
	return model.Pod{}, false
}
