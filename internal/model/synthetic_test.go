package model_test

import (
	"testing"

	"github.com/atedgimo/k8s-dencer/internal/model"
)

// The generator's output has to be worth benchmarking. A uniform cluster with
// no PDBs and no affinity would make the constraint work look far cheaper than
// it is, since most of the cost lives in checks that uniform data never fires.
func TestSynthesizeProducesAClusterWorthMeasuring(t *testing.T) {
	for _, nodes := range []int{4, 34, 334, 1667} {
		snap := model.Synthesize(model.DefaultSynthetic(nodes))

		anti, spread, sizes := 0, 0, map[int64]bool{}
		for _, p := range snap.Pods {
			if p.PodAffinity != nil {
				anti++
			}
			if len(p.TopologySpread) > 0 {
				spread++
			}
			sizes[p.Requests.MilliCPU] = true
		}
		t.Logf("%5d nodes -> %6d pods, %4d pdbs, %5d anti-affinity, %5d spread, %d distinct sizes",
			len(snap.Nodes), len(snap.Pods), len(snap.PDBs), anti, spread, len(sizes))

		if len(snap.Nodes) != nodes {
			t.Errorf("got %d nodes, want %d", len(snap.Nodes), nodes)
		}
		if len(snap.Pods) < nodes {
			t.Errorf("only %d pods for %d nodes", len(snap.Pods), nodes)
		}
		if len(snap.PDBs) == 0 || anti == 0 || spread == 0 {
			t.Errorf("cluster is too uniform: %d pdbs, %d anti-affinity, %d spread",
				len(snap.PDBs), anti, spread)
		}
		// First-fit-decreasing only does interesting work with a range of sizes.
		if len(sizes) < 4 {
			t.Errorf("only %d distinct pod sizes; the packer needs a spread", len(sizes))
		}
	}
}

// A benchmark must measure a change in the code, never a change in the data.
func TestSynthesizeIsDeterministic(t *testing.T) {
	a := model.Synthesize(model.DefaultSynthetic(50))
	b := model.Synthesize(model.DefaultSynthetic(50))

	if len(a.Pods) != len(b.Pods) {
		t.Fatalf("pod counts differ across runs: %d vs %d", len(a.Pods), len(b.Pods))
	}
	for i := range a.Pods {
		if a.Pods[i].Name != b.Pods[i].Name || a.Pods[i].Requests != b.Pods[i].Requests {
			t.Fatalf("pod %d differs across runs: %+v vs %+v", i, a.Pods[i], b.Pods[i])
		}
	}
}
