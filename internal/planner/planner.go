// Package planner turns a cluster snapshot into an ordered sequence of
// consolidation steps.
//
// The planner is deliberately deterministic and algorithmic, not
// LLM-driven: given the same snapshot it must produce the same plan, every
// time, so that plans are auditable and regressions are catchable by a golden
// test. The agent layer explains plans; it never makes them.
//
// It also plans the *ideal* end state without regard to whether a step is safe
// to run right now. Risk is scored separately by the impact classifier, and
// enforced separately by the safety guard. Mixing the two would mean the plan
// silently changes shape depending on the time of day.
package planner

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/atedgimo/k8s-dencer/internal/constraints"
	"github.com/atedgimo/k8s-dencer/internal/model"
)

// Strategy produces a consolidation plan from observed state.
//
// The interface exists so the greedy heuristic can be compared against a
// constraint solver later (architecture doc §10) without disturbing anything
// that consumes plans.
type Strategy interface {
	// Name identifies the strategy in plan metadata and logs.
	Name() string
	// Plan computes a consolidation plan. It must not mutate its inputs and
	// must be deterministic for a given snapshot and options.
	Plan(snap *model.ClusterSnapshot, analysis *constraints.Analysis, opts Options) (*model.Plan, error)
}

// Options constrains what the planner is allowed to propose.
type Options struct {
	// MaxSteps caps plan length. Zero means unlimited.
	MaxSteps int

	// ExcludeNodeLabels excludes a node from being drained if it carries any
	// of these label keys, whatever the value. Control-plane nodes are
	// excluded by default: draining the API server out from under the cluster
	// is not consolidation.
	ExcludeNodeLabels []string

	// ExcludeNamespaces marks pods in these namespaces immovable, which in
	// practice makes their nodes undrainable.
	ExcludeNamespaces []string

	// MinNodeAge skips nodes younger than this. A node that was created
	// seconds ago is probably being scaled up on purpose, and draining it
	// immediately would fight the autoscaler.
	MinNodeAge time.Duration

	// Now is the reference time for MinNodeAge. Zero uses the snapshot time,
	// which keeps planning reproducible from a fixture.
	Now time.Time
}

// DefaultExcludeNodeLabels are the label keys that mark a node as
// infrastructure rather than a consolidation candidate.
var DefaultExcludeNodeLabels = []string{
	"node-role.kubernetes.io/control-plane",
	"node-role.kubernetes.io/master",
}

// DefaultOptions returns options safe for any cluster.
func DefaultOptions() Options {
	return Options{
		ExcludeNodeLabels: DefaultExcludeNodeLabels,
		MinNodeAge:        10 * time.Minute,
	}
}

// nodeExcluded reports whether policy forbids draining this node, and why.
func (o Options) nodeExcluded(n model.Node, now time.Time) (bool, string) {
	for _, key := range o.ExcludeNodeLabels {
		if _, ok := n.Labels[key]; ok {
			return true, fmt.Sprintf("carries excluded label %s", key)
		}
	}
	if o.MinNodeAge > 0 && !n.CreatedAt.IsZero() {
		if age := now.Sub(n.CreatedAt); age < o.MinNodeAge {
			return true, fmt.Sprintf("younger than the %s minimum node age", o.MinNodeAge)
		}
	}
	return false, ""
}

func (o Options) namespaceExcluded(ns string) bool {
	for _, e := range o.ExcludeNamespaces {
		if e == ns {
			return true
		}
	}
	return false
}

// planID is a stable content hash of the plan's steps.
//
// Content-addressed rather than random or time-based so that re-planning an
// unchanged cluster yields the same ID. That makes "has the plan actually
// changed?" a string comparison, and keeps golden tests meaningful.
func planID(strategy string, steps []model.PlanStep) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\n", strategy)
	for _, s := range steps {
		fmt.Fprintf(h, "%d|%s|", s.SequenceNumber, s.TargetNode)
		for _, m := range s.Moves {
			fmt.Fprintf(h, "%s/%s:%s>%s,", m.Namespace, m.Pod, m.FromNode, m.ToNode)
		}
		fmt.Fprint(h, "\n")
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// occupiedNodes counts nodes currently holding at least one movable pod.
// Nodes holding only DaemonSet pods are already reclaimable and are not
// counted as occupied.
func occupiedNodes(p *constraints.Placement) int {
	n := 0
	for _, name := range p.NodeNames() {
		if !p.IsEmpty(name) {
			n++
		}
	}
	return n
}

// sortNodesForDraining orders drain candidates emptiest-first.
//
// Evacuating the least-loaded node is both the cheapest move and the one most
// likely to succeed, so taking them in this order frees the most nodes for a
// given amount of disruption.
func sortNodesForDraining(nodes []model.Node, p *constraints.Placement) {
	sort.SliceStable(nodes, func(i, j int) bool {
		ri := requestedRatio(nodes[i], p)
		rj := requestedRatio(nodes[j], p)
		if ri != rj {
			return ri < rj
		}
		// Name tie-break keeps the plan stable across runs.
		return nodes[i].Name < nodes[j].Name
	})
}

func requestedRatio(n model.Node, p *constraints.Placement) float64 {
	free := p.Free(n.Name)
	used := n.Allocatable.Sub(free)
	return used.DominantRatio(n.Allocatable)
}

// sortPodsBySizeDesc implements the "decreasing" half of first-fit-decreasing.
// Large pods are hardest to place, so placing them first avoids stranding them
// after the easy pods have fragmented the remaining space.
func sortPodsBySizeDesc(pods []model.Pod) {
	sort.SliceStable(pods, func(i, j int) bool {
		a, b := pods[i].Requests, pods[j].Requests
		if a.MilliCPU != b.MilliCPU {
			return a.MilliCPU > b.MilliCPU
		}
		if a.MemoryBytes != b.MemoryBytes {
			return a.MemoryBytes > b.MemoryBytes
		}
		return pods[i].Key() < pods[j].Key()
	})
}

// describeMoves renders a step's moves for logs and debugging.
func describeMoves(moves []model.Move) string {
	parts := make([]string, 0, len(moves))
	for _, m := range moves {
		parts = append(parts, fmt.Sprintf("%s/%s->%s", m.Namespace, m.Pod, m.ToNode))
	}
	return strings.Join(parts, " ")
}
