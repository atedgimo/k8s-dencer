// Package constraints derives the effective scheduling constraint set for
// every pod in a ClusterSnapshot.
//
// The explanation text for each constraint is produced here and nowhere else.
// The UI's constraint inspector and the Kagent agent both surface these exact
// strings rather than deriving their own, so the two can never disagree about
// why a pod cannot move — which would be a bad look for a product whose whole
// value proposition is transparency.
package constraints

import "time"

// Kind identifies what sort of constraint this is. Values are stable
// identifiers: the UI filters on them and the agent reasons over them.
type Kind string

const (
	KindResources        Kind = "Resources"
	KindNodeSelector     Kind = "NodeSelector"
	KindNodeAffinity     Kind = "NodeAffinity"
	KindPodAffinity      Kind = "PodAffinity"
	KindPodAntiAffinity  Kind = "PodAntiAffinity"
	KindTopologySpread   Kind = "TopologySpread"
	KindPDB              Kind = "PodDisruptionBudget"
	KindTaint            Kind = "Taint"
	KindControllerPinned Kind = "ControllerPinned"
	// KindDoNotDisrupt is the ecosystem's explicit hands-off annotation,
	// honoured here exactly as Karpenter and the cluster autoscaler honour it.
	KindDoNotDisrupt     Kind = "DoNotDisrupt"
	KindPersistentVolume Kind = "PersistentVolume"
)

// Constraint is one effective restriction on where a pod may live.
type Constraint struct {
	Kind Kind `json:"kind"`
	// Subject is the object responsible, e.g. "dencer-demo/payments" for a
	// PDB or "topology.kubernetes.io/zone" for a spread constraint.
	Subject string `json:"subject,omitempty"`

	// Hard distinguishes a scheduling requirement from a preference. A
	// preference must never be reported as the reason a pod cannot move.
	Hard bool `json:"hard"`

	// Blocking means this constraint prevents the pod from moving *right
	// now*, as opposed to merely restricting where it could go. A PDB with
	// headroom is a constraint; a PDB at zero headroom is blocking.
	Blocking bool `json:"blocking"`

	// Explanation is the single canonical human-readable description.
	Explanation string `json:"explanation"`
}

// PodConstraints is the complete constraint picture for one pod.
type PodConstraints struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	NodeName  string `json:"nodeName,omitempty"`

	// Movable is false when the pod cannot be relocated at all, either
	// because it is pinned to its node or because something currently blocks
	// its eviction.
	Movable     bool         `json:"movable"`
	Constraints []Constraint `json:"constraints"`

	// CandidateNodes are the other nodes this pod could legally occupy given
	// current cluster state. An empty list on a movable pod means the pod is
	// effectively stuck: there is nowhere for it to go.
	CandidateNodes []string `json:"candidateNodes"`
}

// Key is the namespace/name identifier.
func (p PodConstraints) Key() string { return p.Namespace + "/" + p.Name }

// Blockers returns only the constraints currently preventing movement.
func (p PodConstraints) Blockers() []Constraint {
	var out []Constraint
	for _, c := range p.Constraints {
		if c.Blocking {
			out = append(out, c)
		}
	}
	return out
}

// Of returns the constraints of a given kind.
func (p PodConstraints) Of(kind Kind) []Constraint {
	var out []Constraint
	for _, c := range p.Constraints {
		if c.Kind == kind {
			out = append(out, c)
		}
	}
	return out
}

// Analysis is the constraint picture for a whole snapshot.
//
// Pods is a slice sorted by key rather than a map, deliberately. Go map
// iteration order is randomised, and that randomness leaks straight into
// serialized output — which would make the API response, the UI list order and
// any golden test unstable for no reason.
type Analysis struct {
	TakenAt time.Time        `json:"takenAt"`
	Pods    []PodConstraints `json:"pods"`

	// index is built by Analyze for O(1) lookup. It is nil on an Analysis
	// that was deserialized rather than computed, in which case ForPod falls
	// back to a scan. Never mutated after construction, so Analysis stays
	// safe for concurrent reads.
	index map[string]int
}

// ForPod looks up one pod's constraints by "namespace/name".
func (a *Analysis) ForPod(key string) (PodConstraints, bool) {
	if a.index != nil {
		i, ok := a.index[key]
		if !ok {
			return PodConstraints{}, false
		}
		return a.Pods[i], true
	}
	for _, pc := range a.Pods {
		if pc.Key() == key {
			return pc, true
		}
	}
	return PodConstraints{}, false
}

// ForNode returns the constraints of every pod currently on a node, in the
// same sorted order as Pods.
func (a *Analysis) ForNode(nodeName string) []PodConstraints {
	var out []PodConstraints
	for _, pc := range a.Pods {
		if pc.NodeName == nodeName {
			out = append(out, pc)
		}
	}
	return out
}

// NodeDrainable reports whether every pod on a node could be moved elsewhere,
// and returns the constraints standing in the way if not.
//
// This is the question the planner actually asks of a node, so answering it
// here keeps the reasoning in one place rather than spread across the packer.
func (a *Analysis) NodeDrainable(nodeName string) (bool, []Constraint) {
	var blockers []Constraint
	drainable := true
	for _, pc := range a.ForNode(nodeName) {
		if !pc.Movable {
			drainable = false
			blockers = append(blockers, pc.Blockers()...)
			continue
		}
		if len(pc.CandidateNodes) == 0 {
			drainable = false
			blockers = append(blockers, Constraint{
				Kind:        KindResources,
				Subject:     pc.Key(),
				Hard:        true,
				Blocking:    true,
				Explanation: "No other node can currently accept this pod.",
			})
		}
	}
	return drainable, blockers
}

// Summary is an aggregate view for logging and the UI header.
type Summary struct {
	Pods          int `json:"pods"`
	Movable       int `json:"movable"`
	Blocked       int `json:"blocked"`
	Stuck         int `json:"stuck"`
	PDBBlocked    int `json:"pdbBlocked"`
	AntiAffinity  int `json:"antiAffinity"`
	SpreadBound   int `json:"spreadBound"`
	ControllerPin int `json:"controllerPinned"`
}

// Summarize tallies the analysis.
func (a *Analysis) Summarize() Summary {
	var s Summary
	for _, pc := range a.Pods {
		s.Pods++
		if pc.Movable {
			s.Movable++
			if len(pc.CandidateNodes) == 0 {
				s.Stuck++
			}
		} else {
			s.Blocked++
		}
		for _, c := range pc.Constraints {
			switch c.Kind {
			case KindPDB:
				if c.Blocking {
					s.PDBBlocked++
				}
			case KindPodAntiAffinity:
				s.AntiAffinity++
			case KindTopologySpread:
				if c.Hard {
					s.SpreadBound++
				}
			case KindControllerPinned:
				s.ControllerPin++
			}
		}
	}
	return s
}
