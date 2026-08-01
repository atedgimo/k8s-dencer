package model

import "testing"

// The capacity figures must be the allocatable of the drained nodes — not the
// requested, not the whole cluster's, and absent nodes must not panic. This
// guard lived in the graph package while graph stats carried the figures; the
// figures now ship only in the plan envelope, but the arithmetic is the same
// and the vanished-node case is the part that once went wrong.
func TestReclaimableCapacityPricesTheDrainedNodes(t *testing.T) {
	snap := &ClusterSnapshot{
		Nodes: []Node{
			{Name: "a", Allocatable: Resources{MilliCPU: 8000, MemoryBytes: 32 << 30}},
			{Name: "b", Allocatable: Resources{MilliCPU: 4000, MemoryBytes: 16 << 30}},
			{Name: "c", Allocatable: Resources{MilliCPU: 96000, MemoryBytes: 768 << 30}},
		},
	}
	plan := &Plan{
		NodesBefore: 3,
		Steps: []PlanStep{
			{SequenceNumber: 1, TargetNode: "a"},
			{SequenceNumber: 2, TargetNode: "b"},
			// A node that vanished between planning and serving: skipped, not fatal.
			{SequenceNumber: 3, TargetNode: "gone"},
		},
	}

	cpu, mem := ReclaimableCapacity(plan, snap)
	if want := int64(12000); cpu != want {
		t.Errorf("cpu reclaimable = %d, want %d (a+b only; c stays, gone is gone)", cpu, want)
	}
	if want := int64(48 << 30); mem != want {
		t.Errorf("mem reclaimable = %d, want %d", mem, want)
	}
}
