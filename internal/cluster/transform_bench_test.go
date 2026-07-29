package cluster

import (
	"runtime"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// The informer cache holds decoded Go objects, so the number that matters is
// resident heap per pod, not serialised bytes. This measures it by holding a
// large population live and reading the heap, which is crude but honest — and
// it is the same measurement an operator would make with a memory limit.
func TestTransformHeapSaving(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates ~100k pods")
	}
	if raceEnabled {
		t.Skip("heap measurement is meaningless under the race detector")
	}
	const n = 50000

	measure := func(transform bool) uint64 {
		fn := transformFor(t, &corev1.Pod{})
		pods := make([]*corev1.Pod, 0, n)
		for i := 0; i < n; i++ {
			p := fullyPopulatedPod()
			if transform {
				out, err := fn(p)
				if err != nil {
					t.Fatal(err)
				}
				p = out.(*corev1.Pod)
			}
			pods = append(pods, p)
		}
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		runtime.KeepAlive(pods)
		return m.HeapAlloc
	}

	var base runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&base)

	raw := measure(false) - base.HeapAlloc
	runtime.GC()
	kept := measure(true) - base.HeapAlloc

	t.Logf("untransformed: %d bytes/pod (%.1f MB for %d pods)", raw/n, float64(raw)/1e6, n)
	t.Logf("transformed:   %d bytes/pod (%.1f MB for %d pods)", kept/n, float64(kept)/1e6, n)
	t.Logf("saving:        %.1f%%", 100*(1-float64(kept)/float64(raw)))

	if kept >= raw {
		t.Errorf("transform did not reduce heap: %d -> %d", raw, kept)
	}
}
