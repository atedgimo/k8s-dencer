package graph

import (
	"testing"

	"github.com/atedgimo/k8s-dencer/internal/model"
)

// The capacity figures must be the allocatable of the drained nodes — not the
// requested, not the whole cluster's, and absent nodes must not panic.
func TestStatsPriceTheReclaimableNodes(t *testing.T) {
	snap := &model.ClusterSnapshot{
		Nodes: []model.Node{
			{Name: "a", Allocatable: model.Resources{MilliCPU: 8000, MemoryBytes: 32 << 30}},
			{Name: "b", Allocatable: model.Resources{MilliCPU: 4000, MemoryBytes: 16 << 30}},
			{Name: "c", Allocatable: model.Resources{MilliCPU: 96000, MemoryBytes: 768 << 30}},
		},
	}
	plan := &model.Plan{
		NodesBefore: 3,
		Steps: []model.PlanStep{
			{SequenceNumber: 1, TargetNode: "a"},
			{SequenceNumber: 2, TargetNode: "b"},
			// A node that vanished between planning and serving: skipped, not fatal.
			{SequenceNumber: 3, TargetNode: "gone"},
		},
	}

	p := Build(plan, snap, nil)

	if got, want := p.Stats.CPUReclaimableMilli, int64(12000); got != want {
		t.Errorf("cpu reclaimable = %d, want %d (a+b only; c stays, gone is gone)", got, want)
	}
	if got, want := p.Stats.MemReclaimableBytes, int64(48<<30); got != want {
		t.Errorf("mem reclaimable = %d, want %d", got, want)
	}
}
