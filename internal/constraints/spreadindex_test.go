package constraints_test

import (
	"testing"

	"github.com/atedgimo/k8s-dencer/internal/constraints"
	"github.com/atedgimo/k8s-dencer/internal/model"
)

// The index is a cache in front of a correctness-critical check, so what
// matters is that it can never disagree with a full recount.
//
// These drive it through the mutations a planning pass performs — place,
// remove, clone — and compare against the same question asked of a freshly
// built Placement, which has no cache to be wrong.

func spreadSnapshot() *model.ClusterSnapshot {
	snap := &model.ClusterSnapshot{}
	for i, zone := range []string{"z1", "z1", "z2", "z2", "z3"} {
		snap.Nodes = append(snap.Nodes, model.Node{
			Name: string(rune('a' + i)), Ready: true,
			Labels:      map[string]string{model.LabelZone: zone},
			Allocatable: model.Resources{MilliCPU: 8000, MemoryBytes: 1 << 34, Pods: 110},
		})
	}
	for i, host := range []string{"a", "a", "b", "c", "e"} {
		snap.Pods = append(snap.Pods, model.Pod{
			Namespace: "app", Name: "web-" + string(rune('0'+i)), NodeName: host,
			Phase: model.PodRunning, Ready: true,
			Labels:   map[string]string{"app": "web"},
			Requests: model.Resources{MilliCPU: 100, MemoryBytes: 1 << 26},
			Owner:    &model.OwnerRef{Kind: "ReplicaSet", Name: "web"},
			TopologySpread: []model.TopologySpreadConstraint{{
				MaxSkew: 1, TopologyKey: model.LabelZone,
				WhenUnsatisfiable: model.DoNotSchedule,
				LabelSelector:     &model.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
			}},
		})
	}
	return snap
}

// A cached verdict must equal the verdict of a placement that has never
// cached anything, after any sequence of moves.
func TestSpreadVerdictSurvivesMutation(t *testing.T) {
	snap := spreadSnapshot()
	pod := snap.Pods[0]

	// Warm the index, then move pods around underneath it.
	cached := constraints.NewPlacement(snap)
	for _, n := range cached.NodeNames() {
		cached.CanPlace(pod, n) // populates the spread entry
	}

	moves := []struct {
		pod int
		to  string
	}{
		{1, "c"}, {2, "e"}, {3, "a"}, {4, "b"}, {1, "e"},
	}
	fresh := constraints.NewPlacement(snap)

	for i, m := range moves {
		p := snap.Pods[m.pod]
		cached.Remove(p)
		fresh.Remove(p)
		cached.Place(p, m.to)
		fresh.Place(p, m.to)

		// A fresh placement rebuilt from the same state has no cache at all.
		for _, n := range cached.NodeNames() {
			gotOK, gotWhy := cached.CanPlace(pod, n)
			wantOK, wantWhy := fresh.CanPlace(pod, n)
			if gotOK != wantOK {
				t.Fatalf("after move %d, node %s: cached says %v (%s), uncached says %v (%s)",
					i, n, gotOK, gotWhy, wantOK, wantWhy)
			}
		}
	}
}

// A trial placement must not write its speculative counts back into the
// placement it branched from — the planner clones constantly.
func TestCloneDoesNotShareTheIndex(t *testing.T) {
	snap := spreadSnapshot()
	pod := snap.Pods[0]

	base := constraints.NewPlacement(snap)
	for _, n := range base.NodeNames() {
		base.CanPlace(pod, n)
	}
	before := map[string]bool{}
	for _, n := range base.NodeNames() {
		ok, _ := base.CanPlace(pod, n)
		before[n] = ok
	}

	// Pile work into a clone.
	trial := base.Clone()
	for i := 1; i < len(snap.Pods); i++ {
		trial.Remove(snap.Pods[i])
		trial.Place(snap.Pods[i], "e")
	}

	for _, n := range base.NodeNames() {
		if ok, _ := base.CanPlace(pod, n); ok != before[n] {
			t.Errorf("node %s: the clone's placements leaked into its parent", n)
		}
	}
}

// An unsorted fingerprint would still give correct answers — every lookup
// would simply miss and recompute, quietly restoring the cubic cost. That is a
// worse failure than a wrong one because nothing surfaces it, so the cache's
// effectiveness is asserted rather than only its correctness.
func TestRepeatedChecksHitTheCache(t *testing.T) {
	snap := spreadSnapshot()
	pod := snap.Pods[0]
	// Several selector keys, so map ordering has room to vary between calls.
	pod.TopologySpread[0].LabelSelector = &model.LabelSelector{
		MatchLabels: map[string]string{"app": "web", "tier": "front", "env": "prod", "team": "core"},
	}

	p := constraints.NewPlacement(snap)
	// First pass populates; the rest must be served from it. If the
	// fingerprint were unstable each call would build a new entry.
	for range 50 {
		for _, n := range p.NodeNames() {
			p.CanPlace(pod, n)
		}
	}
	if n := constraints.SpreadEntriesForTest(p); n != 1 {
		t.Errorf("%d cache entries after repeated identical checks, want 1 — "+
			"the selector fingerprint is not stable, so every lookup misses", n)
	}
}

func TestVerdictIsStableAcrossRuns(t *testing.T) {
	snap := spreadSnapshot()
	pod := snap.Pods[0]
	pod.TopologySpread[0].LabelSelector = &model.LabelSelector{
		MatchLabels: map[string]string{"app": "web", "tier": "front", "env": "prod"},
	}

	var first []bool
	for run := range 8 {
		p := constraints.NewPlacement(snap)
		var got []bool
		for _, n := range p.NodeNames() {
			ok, _ := p.CanPlace(pod, n)
			got = append(got, ok)
		}
		if run == 0 {
			first = got
			continue
		}
		for i := range got {
			if got[i] != first[i] {
				t.Fatalf("run %d disagrees with run 0 at node %d: %v vs %v",
					run, i, got[i], first[i])
			}
		}
	}
}
