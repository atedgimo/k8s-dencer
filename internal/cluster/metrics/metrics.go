// Package metrics abstracts actual resource usage away from the collector.
//
// The MVP plans from resource *requests* only. That is not merely a
// simplification: scheduling itself is request-based, so requests are the
// correct input for deciding whether a packing is feasible. Actual usage is
// useful for spotting over-provisioned workloads, which is a later concern.
//
// The interface exists so that adding a real source later is a wiring change
// rather than a change to the collector or the model.
package metrics

import (
	"context"

	"github.com/atedgimo/k8s-dencer/internal/model"
)

// Source supplies observed resource usage.
type Source interface {
	// Available reports whether this source can currently serve data. A
	// source that is configured but whose backend is missing must report
	// false rather than returning errors on every collection.
	Available() bool

	// NodeUsage returns usage keyed by node name.
	NodeUsage(ctx context.Context) (map[string]model.Resources, error)

	// PodUsage returns usage keyed by "namespace/name".
	PodUsage(ctx context.Context) (map[string]model.Resources, error)
}

// Noop is the default source: no usage data at all.
//
// Chosen over a source that guesses or extrapolates. A planner that silently
// invents utilisation figures would produce confident, wrong plans; reporting
// "no data" keeps the snapshot honest and leaves HasUsageData false.
type Noop struct{}

// Available always reports false.
func (Noop) Available() bool { return false }

// NodeUsage returns no data.
func (Noop) NodeUsage(context.Context) (map[string]model.Resources, error) { return nil, nil }

// PodUsage returns no data.
func (Noop) PodUsage(context.Context) (map[string]model.Resources, error) { return nil, nil }

// compile-time check
var _ Source = Noop{}
