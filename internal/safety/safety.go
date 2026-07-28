// Package safety implements the non-negotiable rails on execution.
//
// Architecture doc §9: these are enforced in code, not in prompts and not in
// API input validation, so no crafted request and no confused LLM can talk
// them into allowing something. The UI may only ever ask; this package decides.
//
// Two properties are deliberate and load-bearing:
//
//  1. Every rule is checked immediately before the action it guards, against
//     freshly-read cluster state — never once at admission for a whole run. A
//     request for steps 1–10 that becomes unsafe at step 4 stops at step 4.
//  2. Every refusal names the rule that produced it. The name lands in the
//     audit trail and in the operator's face, so "why did it stop?" is
//     answerable without reading logs.
package safety

import (
	"fmt"

	"github.com/atedgimo/k8s-dencer/internal/constraints"
	"github.com/atedgimo/k8s-dencer/internal/model"
)

// Rule identifies a rail. Stable strings: they appear in audit events, and
// changing one silently rewrites history.
type Rule string

const (
	// RuleRedRequiresWindow blocks Red-rated steps. Until the MaintenanceWindow
	// CRD exists in Phase 3 there is no window to be inside, so Red is simply
	// refused — which is the safe reading of "Red may only run inside an
	// approved window".
	RuleRedRequiresWindow Rule = "RedRequiresWindow"

	// RuleMaxNodesPerRun caps how much one request may disturb.
	RuleMaxNodesPerRun Rule = "MaxNodesPerRun"

	// RuleMinReadyNodes keeps a floor of schedulable capacity, so a
	// consolidation cannot squeeze a cluster to the point where the next
	// failure has nowhere to go.
	RuleMinReadyNodes Rule = "MinReadyNodes"

	// RuleStepFreshness rejects a step whose preconditions have drifted since
	// the plan was computed.
	RuleStepFreshness Rule = "StepFreshness"

	// RulePDBHeadroom re-reads live disruption headroom immediately before
	// each individual eviction.
	RulePDBHeadroom Rule = "PDBHeadroom"

	// RuleNodeNotFound covers a target that has vanished.
	RuleNodeNotFound Rule = "NodeNotFound"
)

// Limits are the operator-tunable rails, supplied by the chart.
type Limits struct {
	// MaxNodesPerRun caps nodes drained by a single execution request.
	MaxNodesPerRun int

	// MinReadyNodes is the floor of Ready, schedulable nodes to leave behind.
	MinReadyNodes int

	// AllowRed is a blanket override, and remains false in every shipped
	// configuration. Red steps are unlocked by an open MaintenanceWindow that
	// sets allowRed — a decision scoped to a time and a set of nodes — not by
	// a global switch. This exists for tests and for a deliberate operator
	// escape hatch, never as a values key.
	AllowRed bool
}

// Windows reports whether an open maintenance window permits a Red step on a
// node, and explains the answer.
//
// An interface so the guard does not depend on how windows are discovered, and
// so "no windows at all" is representable as nil — which is what Phase 2 was,
// and what an install that never creates one stays.
type Windows interface {
	AllowsRedOn(node model.Node) (bool, string)
}

// DefaultLimits are deliberately conservative. An operator who wants to drain
// more can raise them explicitly; nobody should discover the ceiling by
// accidentally draining a cluster.
func DefaultLimits() Limits {
	return Limits{MaxNodesPerRun: 5, MinReadyNodes: 3, AllowRed: false}
}

// Blocked is a refusal. It is an error so it cannot be ignored by a caller
// that forgets to check a boolean.
type Blocked struct {
	Rule   Rule
	Reason string
}

func (b *Blocked) Error() string { return fmt.Sprintf("%s: %s", b.Rule, b.Reason) }

// block is a small constructor to keep the rules readable.
func block(rule Rule, format string, args ...any) *Blocked {
	return &Blocked{Rule: rule, Reason: fmt.Sprintf(format, args...)}
}

// Guard evaluates the rails.
type Guard struct {
	limits  Limits
	windows Windows
}

// New builds a Guard with no maintenance windows, so Red is always refused.
func New(limits Limits) *Guard { return &Guard{limits: limits} }

// WithWindows attaches the maintenance windows a Red step may be permitted by.
// A nil Windows keeps the Phase 2 behaviour: Red is refused outright.
func (g *Guard) WithWindows(w Windows) *Guard {
	g.windows = w
	return g
}

// Limits exposes the configured rails, for reporting.
func (g *Guard) Limits() Limits { return g.limits }

// RunState is what the guard needs to know about the request in progress.
type RunState struct {
	// NodesDrainedSoFar counts nodes already drained by this run, so the cap
	// applies across the whole request rather than per step.
	NodesDrainedSoFar int
}

// CheckStep evaluates every rail that applies before a step begins.
//
// live must be a freshly-collected snapshot, not the one the plan was computed
// from. Checking a step against the state that produced it would confirm only
// that the planner was self-consistent.
func (g *Guard) CheckStep(step model.PlanStep, live *model.ClusterSnapshot, run RunState) error {
	if g.limits.MaxNodesPerRun > 0 && run.NodesDrainedSoFar >= g.limits.MaxNodesPerRun {
		return block(RuleMaxNodesPerRun,
			"this run has already drained %d node(s), the configured maximum. "+
				"Raise safety.maxNodesPerRun or start another run",
			run.NodesDrainedSoFar)
	}

	if step.TargetNode == "" {
		return block(RuleStepFreshness, "step %d names no target node", step.SequenceNumber)
	}

	node, ok := live.NodeByName(step.TargetNode)
	if !ok {
		return block(RuleNodeNotFound,
			"node %s no longer exists; the plan is stale", step.TargetNode)
	}

	// Red is checked against the windows covering THIS node, which is why it
	// comes after the node is resolved rather than first. A window scoped to a
	// batch pool must not authorise a step elsewhere.
	if err := g.checkRed(step, node); err != nil {
		return err
	}

	// Draining below the floor is refused before anything is touched, rather
	// than discovered when the last node fills up.
	if err := g.checkNodeFloor(node, live); err != nil {
		return err
	}

	return g.checkStepFeasible(step, live)
}

// checkRed enforces the confinement of Red steps to an approved window.
//
// Doc §9: "Red-rated steps can only execute inside an active, approved
// maintenance window — enforced by the Safety Guard itself, not left to UI/API
// input validation, so it cannot be bypassed by a crafted request."
func (g *Guard) checkRed(step model.PlanStep, node model.Node) error {
	if !step.Impact.RequiresMaintenanceWindow() {
		return nil
	}
	if g.limits.AllowRed {
		return nil
	}
	if g.windows == nil {
		return block(RuleRedRequiresWindow,
			"step %d is rated Red and may only run inside an approved maintenance window; "+
				"none is configured. Rated Red because: %s",
			step.SequenceNumber, step.Rationale)
	}
	allowed, why := g.windows.AllowsRedOn(node)
	if !allowed {
		return block(RuleRedRequiresWindow,
			"step %d is rated Red and may only run inside an approved maintenance window: %s. "+
				"Rated Red because: %s",
			step.SequenceNumber, why, step.Rationale)
	}
	return nil
}

// checkNodeFloor keeps enough Ready, schedulable capacity in the cluster.
func (g *Guard) checkNodeFloor(target model.Node, live *model.ClusterSnapshot) error {
	if g.limits.MinReadyNodes <= 0 {
		return nil
	}
	ready := 0
	for _, n := range live.Nodes {
		if n.Ready && !n.Unschedulable {
			ready++
		}
	}
	// The target is about to leave the schedulable pool, so count it out.
	if target.Ready && !target.Unschedulable {
		ready--
	}
	if ready < g.limits.MinReadyNodes {
		return block(RuleMinReadyNodes,
			"draining %s would leave %d schedulable node(s), below the floor of %d",
			target.Name, ready, g.limits.MinReadyNodes)
	}
	return nil
}

// checkStepFeasible re-proves that every pod on the target can go somewhere
// else, against live state.
//
// This is the scheduler-simulation half of the design. Rather than importing
// kube-scheduler's framework, it reuses the Placement evaluator the planner
// itself was built on — and the executor then verifies reality after each
// eviction, so a prediction that turns out wrong aborts the run instead of
// corrupting it.
func (g *Guard) checkStepFeasible(step model.PlanStep, live *model.ClusterSnapshot) error {
	placement := constraints.NewPlacement(live).Clone()

	// The target is leaving the pool; nothing may be placed back onto it.
	// Copied because Occupants aliases the placement's own slice, which Remove
	// mutates — iterating the live one would skip pods.
	occupants := append([]model.Pod(nil), placement.Occupants(step.TargetNode)...)
	for _, pod := range occupants {
		placement.Remove(pod)
	}

	for _, pod := range occupants {
		// DaemonSet, terminating and completed pods leave with the node and
		// need no home; counting them would make every node undrainable.
		if !pod.IsMovable() {
			continue
		}
		if !g.hasHome(placement, pod, step.TargetNode) {
			return block(RuleStepFreshness,
				"%s/%s on %s has nowhere left to go; the cluster has changed since the plan was computed",
				pod.Namespace, pod.Name, step.TargetNode)
		}
	}
	return nil
}

// hasHome reports whether pod fits on some node other than the one draining.
// The pod is provisionally placed so the search reflects the real end state.
func (g *Guard) hasHome(placement *constraints.Placement, pod model.Pod, draining string) bool {
	for _, candidate := range placement.NodeNames() {
		if candidate == draining {
			continue
		}
		if ok, _ := placement.CanPlace(pod, candidate); ok {
			placement.Place(pod, candidate)
			return true
		}
	}
	return false
}

// CheckEviction is the last gate before a single pod is evicted.
//
// Called immediately before each eviction with freshly-read state, never once
// per step: evicting the first pod of a set changes the headroom for the rest,
// and a batch check would authorise disruptions the PDB no longer permits.
func (g *Guard) CheckEviction(pod model.Pod, live *model.ClusterSnapshot) error {
	for _, pdb := range live.PDBs {
		if pdb.Namespace != pod.Namespace {
			continue
		}
		if pdb.Selector.IsEmpty() || !pdb.Selector.Matches(pod.Labels) {
			continue
		}
		if pdb.Blocks() {
			return block(RulePDBHeadroom,
				"PodDisruptionBudget %s allows %d more disruption(s) (%d/%d healthy); "+
					"evicting %s/%s would violate it",
				pdb.Key(), pdb.DisruptionsAllowed, pdb.CurrentHealthy, pdb.DesiredHealthy,
				pod.Namespace, pod.Name)
		}
	}
	return nil
}
