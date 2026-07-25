package model

// Scheduling constraint types, mirrored from the Kubernetes API rather than
// imported from it.
//
// This duplication is deliberate and is the single most important property of
// this package: with no k8s.io imports, a ClusterSnapshot round-trips through
// YAML and the planner can be tested against a fixture with no API server, no
// envtest and no cluster. See the package doc in cluster.go.

// TaintEffect is the scheduling consequence of a taint.
type TaintEffect string

const (
	TaintEffectNoSchedule       TaintEffect = "NoSchedule"
	TaintEffectPreferNoSchedule TaintEffect = "PreferNoSchedule"
	TaintEffectNoExecute        TaintEffect = "NoExecute"
)

// Taint repels pods that do not tolerate it.
type Taint struct {
	Key    string      `json:"key"`
	Value  string      `json:"value,omitempty"`
	Effect TaintEffect `json:"effect"`
}

// TolerationOperator is how a toleration matches a taint.
type TolerationOperator string

const (
	TolerationOpExists TolerationOperator = "Exists"
	TolerationOpEqual  TolerationOperator = "Equal"
)

// Toleration allows a pod onto a tainted node.
type Toleration struct {
	Key      string             `json:"key,omitempty"`
	Operator TolerationOperator `json:"operator,omitempty"`
	Value    string             `json:"value,omitempty"`
	Effect   TaintEffect        `json:"effect,omitempty"`
}

// ToleratesTaint reports whether this toleration matches the given taint,
// following the same rules as the scheduler: an empty key with Exists
// tolerates everything, and an empty effect matches every effect.
func (t Toleration) ToleratesTaint(taint Taint) bool {
	if t.Effect != "" && t.Effect != taint.Effect {
		return false
	}
	if t.Key == "" {
		// An empty key is only valid with Exists, where it matches all taints.
		return t.Operator == TolerationOpExists
	}
	if t.Key != taint.Key {
		return false
	}
	switch t.Operator {
	case TolerationOpExists:
		return true
	case TolerationOpEqual, "":
		// Equal is the default when the operator is unset.
		return t.Value == taint.Value
	default:
		return false
	}
}

// SelectorOperator is a set-based label match operator.
type SelectorOperator string

const (
	SelectorOpIn           SelectorOperator = "In"
	SelectorOpNotIn        SelectorOperator = "NotIn"
	SelectorOpExists       SelectorOperator = "Exists"
	SelectorOpDoesNotExist SelectorOperator = "DoesNotExist"
	SelectorOpGt           SelectorOperator = "Gt"
	SelectorOpLt           SelectorOperator = "Lt"
)

// SelectorRequirement is one set-based label requirement.
type SelectorRequirement struct {
	Key      string           `json:"key"`
	Operator SelectorOperator `json:"operator"`
	Values   []string         `json:"values,omitempty"`
}

// LabelSelector selects objects by label. An empty selector matches
// everything, matching Kubernetes semantics.
type LabelSelector struct {
	MatchLabels      map[string]string     `json:"matchLabels,omitempty"`
	MatchExpressions []SelectorRequirement `json:"matchExpressions,omitempty"`
}

// IsEmpty reports whether the selector constrains nothing.
func (s *LabelSelector) IsEmpty() bool {
	return s == nil || (len(s.MatchLabels) == 0 && len(s.MatchExpressions) == 0)
}

// Matches reports whether labels satisfy every requirement.
func (s *LabelSelector) Matches(labels map[string]string) bool {
	if s == nil {
		return true
	}
	for k, v := range s.MatchLabels {
		if labels[k] != v {
			return false
		}
	}
	for _, req := range s.MatchExpressions {
		if !req.matches(labels) {
			return false
		}
	}
	return true
}

func (r SelectorRequirement) matches(labels map[string]string) bool {
	v, present := labels[r.Key]
	switch r.Operator {
	case SelectorOpExists:
		return present
	case SelectorOpDoesNotExist:
		return !present
	case SelectorOpIn:
		return present && contains(r.Values, v)
	case SelectorOpNotIn:
		return !present || !contains(r.Values, v)
	case SelectorOpGt, SelectorOpLt:
		// Numeric comparisons apply to node selectors only and are not used by
		// any predicate the planner evaluates. Treated as unsatisfied rather
		// than silently satisfied: a constraint we cannot evaluate must never
		// make a move look feasible.
		return false
	default:
		return false
	}
}

// NodeSelectorTerm is a conjunction of requirements against node labels.
// Terms are OR-ed with each other; requirements within a term are AND-ed.
type NodeSelectorTerm struct {
	MatchExpressions []SelectorRequirement `json:"matchExpressions,omitempty"`
	MatchFields      []SelectorRequirement `json:"matchFields,omitempty"`
}

// NodeAffinity is the hard node-affinity requirement of a pod. Only the
// required form is modelled: preferred affinity affects scoring, never
// feasibility, and the planner must not treat a preference as a blocker.
type NodeAffinity struct {
	RequiredTerms []NodeSelectorTerm `json:"requiredTerms,omitempty"`
}

// PodAffinityTerm constrains placement relative to other pods.
type PodAffinityTerm struct {
	LabelSelector *LabelSelector `json:"labelSelector,omitempty"`
	TopologyKey   string         `json:"topologyKey"`
	Namespaces    []string       `json:"namespaces,omitempty"`
}

// PodAffinity holds the required inter-pod affinity and anti-affinity rules.
// As with NodeAffinity, only the required form is modelled.
type PodAffinity struct {
	RequiredAffinity     []PodAffinityTerm `json:"requiredAffinity,omitempty"`
	RequiredAntiAffinity []PodAffinityTerm `json:"requiredAntiAffinity,omitempty"`
}

// UnsatisfiableAction is what the scheduler does when a spread constraint
// cannot be met.
type UnsatisfiableAction string

const (
	// DoNotSchedule makes the constraint hard; the planner must respect it.
	DoNotSchedule UnsatisfiableAction = "DoNotSchedule"
	// ScheduleAnyway makes it a preference.
	ScheduleAnyway UnsatisfiableAction = "ScheduleAnyway"
)

// TopologySpreadConstraint limits how unevenly a pod set may be distributed.
type TopologySpreadConstraint struct {
	MaxSkew           int32               `json:"maxSkew"`
	TopologyKey       string              `json:"topologyKey"`
	WhenUnsatisfiable UnsatisfiableAction `json:"whenUnsatisfiable"`
	LabelSelector     *LabelSelector      `json:"labelSelector,omitempty"`
	MinDomains        *int32              `json:"minDomains,omitempty"`
}

// IsHard reports whether violating this constraint blocks scheduling.
func (t TopologySpreadConstraint) IsHard() bool {
	return t.WhenUnsatisfiable == DoNotSchedule
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
