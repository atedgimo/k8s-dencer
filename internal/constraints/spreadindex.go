package constraints

import (
	"sort"
	"strings"

	"github.com/atedgimo/k8s-dencer/internal/model"
)

// spreadIndex memoises "how many matching pods sit in each topology domain".
//
// This is the fix for the cubic term M16 measured. domainCounts used to walk
// every node and every occupant, running a label-selector match, on every
// call — and it is called once per spread-constrained pod per candidate node,
// so the cost was pods × nodes × pods. At 2,500 pods that put 69% of
// constraints.Analyze inside this one function.
//
// The counts do not depend on which candidate node is being considered. They
// depend only on the topology key, the selector, the namespace, and the
// current placement — so they are computed once and then maintained
// incrementally as pods move.
//
// Incremental maintenance rather than invalidation is deliberate. A planning
// pass interleaves many CanPlace calls with occasional Place calls; throwing
// the whole index away on every Place would rebuild it O(pods) times and leave
// the cost quadratic instead of cubic. Adjusting the affected counters keeps it
// near linear.
type spreadIndex struct {
	entries map[spreadKey]*spreadEntry
}

// spreadKey identifies one question: "for this selector, in this namespace,
// bucketed by this topology key, how many pods are in each domain?"
type spreadKey struct {
	topologyKey string
	namespace   string
	selector    string
}

type spreadEntry struct {
	counts map[string]int
	// Kept so a later Place or Remove can decide whether a pod affects this
	// entry without re-deriving the selector from a fingerprint.
	selector *model.LabelSelector
}

func newSpreadIndex() *spreadIndex {
	return &spreadIndex{entries: make(map[spreadKey]*spreadEntry)}
}

func (s *spreadIndex) clone() *spreadIndex {
	out := &spreadIndex{entries: make(map[spreadKey]*spreadEntry, len(s.entries))}
	for k, e := range s.entries {
		counts := make(map[string]int, len(e.counts))
		for d, c := range e.counts {
			counts[d] = c
		}
		out.entries[k] = &spreadEntry{counts: counts, selector: e.selector}
	}
	return out
}

// counts returns the per-domain tally for a constraint, building it on first
// use. The returned map is the index's own — callers must not mutate it.
func (p *Placement) spreadCounts(namespace string, tsc model.TopologySpreadConstraint) map[string]int {
	key := spreadKey{
		topologyKey: tsc.TopologyKey,
		namespace:   namespace,
		selector:    selectorFingerprint(tsc.LabelSelector),
	}
	if e, ok := p.spread.entries[key]; ok {
		return e.counts
	}

	// First use: one pass over the cluster. Every domain present is seeded at
	// zero, because an empty domain still counts toward the skew.
	counts := make(map[string]int)
	for _, name := range p.nodeOrder {
		ns := p.nodes[name]
		domain, ok := ns.node.Labels[tsc.TopologyKey]
		if !ok {
			continue
		}
		if _, seen := counts[domain]; !seen {
			counts[domain] = 0
		}
		for _, occupant := range ns.occupants {
			if occupant.Namespace == namespace && tsc.LabelSelector.Matches(occupant.Labels) {
				counts[domain]++
			}
		}
	}

	p.spread.entries[key] = &spreadEntry{counts: counts, selector: tsc.LabelSelector}
	return counts
}

// adjust moves a pod's contribution in or out of every affected entry.
//
// Called from Place and Remove. Only entries whose namespace and selector match
// the pod are touched, which is a handful even on a large cluster.
func (p *Placement) adjustSpread(pod model.Pod, nodeName string, delta int) {
	if len(p.spread.entries) == 0 {
		return
	}
	ns, ok := p.nodes[nodeName]
	if !ok {
		return
	}
	for key, entry := range p.spread.entries {
		if key.namespace != pod.Namespace {
			continue
		}
		domain, ok := ns.node.Labels[key.topologyKey]
		if !ok {
			continue
		}
		if !entry.selector.Matches(pod.Labels) {
			continue
		}
		entry.counts[domain] += delta
	}
}

// selectorFingerprint renders a selector as a stable string.
//
// Map iteration in Go is randomised, so the keys are sorted. Without that, the
// same selector would fingerprint differently from one call to the next and
// every lookup would miss — quietly restoring the cubic behaviour this index
// exists to remove, while still returning correct answers. A silent
// performance regression is harder to notice than a wrong one, which is why
// stability is asserted directly.
func selectorFingerprint(s *model.LabelSelector) string {
	if s == nil {
		return ""
	}
	var b strings.Builder

	keys := make([]string, 0, len(s.MatchLabels))
	for k := range s.MatchLabels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(s.MatchLabels[k])
		b.WriteByte(',')
	}

	if len(s.MatchExpressions) == 0 {
		return b.String()
	}
	exprs := make([]string, 0, len(s.MatchExpressions))
	for _, e := range s.MatchExpressions {
		vals := append([]string(nil), e.Values...)
		sort.Strings(vals)
		exprs = append(exprs, e.Key+" "+string(e.Operator)+" ["+strings.Join(vals, "|")+"]")
	}
	sort.Strings(exprs)
	b.WriteByte(';')
	b.WriteString(strings.Join(exprs, ";"))
	return b.String()
}

// SpreadEntriesForTest reports how many distinct spread questions have been
// memoised. Exported for tests: cache effectiveness is a property worth
// asserting, and it is invisible from outside otherwise.
func SpreadEntriesForTest(p *Placement) int { return len(p.spread.entries) }
