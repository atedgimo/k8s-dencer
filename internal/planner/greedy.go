package planner

import (
	"sort"
	"time"

	"github.com/atedgimo/k8s-dencer/internal/constraints"
	"github.com/atedgimo/k8s-dencer/internal/model"
)

// Greedy is a first-fit-decreasing bin-packer.
//
// It walks drain candidates emptiest-first and, for each, tries to relocate
// every movable pod onto the nodes that remain. A node is accepted only if
// *all* of its pods find a home — a partial evacuation frees nothing, so a
// step that cannot complete is not proposed at all.
//
// Chosen over a constraint solver for the MVP because it is fast enough to
// re-plan continuously, trivially deterministic, and has no CGO dependency.
// Architecture doc §10 keeps the OR-Tools comparison open; the Strategy
// interface is what makes swapping it a contained change.
type Greedy struct{}

// Name implements Strategy.
func (Greedy) Name() string { return "greedy-first-fit-decreasing" }

// Plan implements Strategy.
func (Greedy) Plan(snap *model.ClusterSnapshot, analysis *constraints.Analysis, opts Options) (*model.Plan, error) {
	now := opts.Now
	if now.IsZero() {
		now = snap.TakenAt
	}

	// The working placement is mutated as steps are accepted, so later steps
	// see the cluster as earlier steps will have left it. Planning each step
	// against the original state would produce a set of moves that conflict
	// with each other. The ceiling applies here and only here: the analyzer
	// keeps full allocatable because its question is feasibility, while the
	// planner's question is what to aim for.
	work := constraints.NewPlacementCeiling(snap, opts.PackCeiling)
	nodesBefore := occupiedNodes(work)

	candidates := drainCandidates(snap, opts, now)
	sortNodesForDraining(candidates, work)

	drained := make(map[string]bool, len(candidates))
	steps := make([]model.PlanStep, 0, len(candidates))

	for _, node := range candidates {
		if opts.MaxSteps > 0 && len(steps) >= opts.MaxSteps {
			break
		}
		if work.IsEmpty(node.Name) {
			// Already reclaimable; nothing to do and no step to propose.
			drained[node.Name] = true
			continue
		}

		moves, ok := tryDrain(work, node, drained, analysis, opts)
		if !ok {
			continue
		}

		// Commit only once the whole node is known to be evacuable.
		for _, m := range moves {
			pod, found := findPod(work, m.FromNode, m.Namespace, m.Pod)
			if !found {
				continue
			}
			work.Remove(pod)
			work.Place(pod, m.ToNode)
		}
		drained[node.Name] = true

		steps = append(steps, model.PlanStep{
			SequenceNumber: len(steps) + 1,
			TargetNode:     node.Name,
			Moves:          moves,
		})
	}

	for i := range steps {
		steps[i].ID = stepID(steps[i])
	}

	return &model.Plan{
		ID:              planID(Greedy{}.Name(), steps),
		GeneratedAt:     time.Now().UTC(),
		SnapshotTakenAt: snap.TakenAt,
		Status:          model.PlanValid,
		Steps:           steps,
		NodesBefore:     nodesBefore,
		NodesAfter:      nodesBefore - len(steps),
		PackCeiling:     opts.PackCeiling,
	}, nil
}

// tryDrain attempts to relocate every movable pod off node, against a
// throwaway copy of the working state. Returns the moves only if all of them
// succeed.
func tryDrain(
	work *constraints.Placement,
	node model.Node,
	drained map[string]bool,
	analysis *constraints.Analysis,
	opts Options,
) ([]model.Move, bool) {
	pods := make([]model.Pod, 0)
	for _, p := range work.Occupants(node.Name) {
		if !p.IsMovable() {
			// DaemonSet and terminating pods stay put and do not block the
			// drain; they simply do not need relocating.
			continue
		}
		if opts.namespaceExcluded(p.Namespace) {
			return nil, false
		}
		// A pod the analyzer says cannot be evicted (a zero-headroom PDB, for
		// instance) makes the whole node undrainable.
		if pc, ok := analysis.ForPod(p.Key()); ok && !pc.Movable {
			return nil, false
		}
		pods = append(pods, p)
	}
	if len(pods) == 0 {
		return nil, false
	}

	sortPodsBySizeDesc(pods)

	trial := work.Clone()
	moves := make([]model.Move, 0, len(pods))

	for _, p := range pods {
		target := firstFit(trial, p, node.Name, drained)
		if target == "" {
			// One unplaceable pod invalidates the whole step.
			return nil, false
		}
		trial.Remove(p)
		trial.Place(p, target)
		moves = append(moves, model.Move{
			Namespace:   p.Namespace,
			Pod:         p.Name,
			FromNode:    node.Name,
			ToNode:      target,
			CPUMilli:    p.Requests.MilliCPU,
			MemoryBytes: p.Requests.MemoryBytes,
		})
	}
	return moves, true
}

// firstFit picks a destination for pod, preferring the fullest node that can
// still take it.
//
// Preferring fuller nodes concentrates load instead of spreading it, which is
// the entire point of consolidation: a plain first-fit over an arbitrary node
// order tends to leave every node partly used and frees nothing.
func firstFit(trial *constraints.Placement, pod model.Pod, source string, drained map[string]bool) string {
	type candidate struct {
		name string
		used float64
	}
	var options []candidate

	for _, name := range trial.NodeNames() {
		if name == source || drained[name] {
			continue
		}
		if ok, _ := trial.CanPlace(pod, name); !ok {
			continue
		}
		free := trial.Free(name)
		options = append(options, candidate{name: name, used: 1 - free.DominantRatio(freeBasis(trial, name))})
	}
	if len(options) == 0 {
		return ""
	}
	sort.SliceStable(options, func(i, j int) bool {
		if options[i].used != options[j].used {
			return options[i].used > options[j].used
		}
		return options[i].name < options[j].name
	})
	return options[0].name
}

// freeBasis returns the node's total allocatable, used as the denominator when
// scoring how full a node is.
func freeBasis(trial *constraints.Placement, name string) model.Resources {
	free := trial.Free(name)
	used := model.Resources{}
	for _, p := range trial.Occupants(name) {
		used = used.Add(p.Requests).Add(model.Resources{Pods: 1})
	}
	return free.Add(used)
}

// drainCandidates returns nodes policy permits draining, in snapshot order.
func drainCandidates(snap *model.ClusterSnapshot, opts Options, now time.Time) []model.Node {
	out := make([]model.Node, 0, len(snap.Nodes))
	for _, n := range snap.Nodes {
		if !n.Ready {
			// An unready node's pods are already in trouble; moving them is
			// the scheduler's problem, not a consolidation decision.
			continue
		}
		if excluded, _ := opts.nodeExcluded(n, now); excluded {
			continue
		}
		if n.DoNotDisrupt() {
			// The node-level hands-off marker. Karpenter will not consolidate
			// this node; neither will we.
			continue
		}
		out = append(out, n)
	}
	return out
}

func findPod(p *constraints.Placement, nodeName, namespace, name string) (model.Pod, bool) {
	for _, occupant := range p.Occupants(nodeName) {
		if occupant.Namespace == namespace && occupant.Name == name {
			return occupant, true
		}
	}
	return model.Pod{}, false
}

func stepID(s model.PlanStep) string {
	return planID("step", []model.PlanStep{s})
}
