package model

import "fmt"

// Resources is a resource vector in canonical integer units.
//
// Integers rather than the Kubernetes resource.Quantity type: the bin-packer
// sums and compares these millions of times per plan, and exact integer
// arithmetic keeps packing decisions reproducible. Reproducibility is what
// makes the golden fixture tests meaningful.
type Resources struct {
	MilliCPU    int64 `json:"milliCPU"`
	MemoryBytes int64 `json:"memoryBytes"`
	Pods        int64 `json:"pods"`
}

// Add returns the component-wise sum.
func (r Resources) Add(o Resources) Resources {
	return Resources{
		MilliCPU:    r.MilliCPU + o.MilliCPU,
		MemoryBytes: r.MemoryBytes + o.MemoryBytes,
		Pods:        r.Pods + o.Pods,
	}
}

// Sub returns the component-wise difference. Components are clamped at zero:
// negative capacity is meaningless and silently propagating it would produce
// packings that look feasible but are not.
func (r Resources) Sub(o Resources) Resources {
	return Resources{
		MilliCPU:    max64(r.MilliCPU-o.MilliCPU, 0),
		MemoryBytes: max64(r.MemoryBytes-o.MemoryBytes, 0),
		Pods:        max64(r.Pods-o.Pods, 0),
	}
}

// Fits reports whether r fits entirely within capacity.
func (r Resources) Fits(capacity Resources) bool {
	return r.MilliCPU <= capacity.MilliCPU &&
		r.MemoryBytes <= capacity.MemoryBytes &&
		r.Pods <= capacity.Pods
}

// IsZero reports whether every component is zero.
func (r Resources) IsZero() bool {
	return r.MilliCPU == 0 && r.MemoryBytes == 0 && r.Pods == 0
}

// Ratio returns r as a fraction of capacity per dimension, in [0,1]. A zero
// capacity dimension yields 0 rather than a division by zero.
func (r Resources) Ratio(capacity Resources) (cpu, memory, pods float64) {
	return ratio(r.MilliCPU, capacity.MilliCPU),
		ratio(r.MemoryBytes, capacity.MemoryBytes),
		ratio(r.Pods, capacity.Pods)
}

// DominantRatio is the largest per-dimension utilisation fraction. This is the
// dimension that actually limits packing, so it is what the planner sorts on.
func (r Resources) DominantRatio(capacity Resources) float64 {
	cpu, mem, pods := r.Ratio(capacity)
	return math3(cpu, mem, pods)
}

func (r Resources) String() string {
	return fmt.Sprintf("%dm cpu / %s / %d pods", r.MilliCPU, humanBytes(r.MemoryBytes), r.Pods)
}

func ratio(v, capacity int64) float64 {
	if capacity <= 0 {
		return 0
	}
	return float64(v) / float64(capacity)
}

func math3(a, b, c float64) float64 {
	m := a
	if b > m {
		m = b
	}
	if c > m {
		m = c
	}
	return m
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
