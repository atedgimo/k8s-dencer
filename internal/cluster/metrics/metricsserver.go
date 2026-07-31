package metrics

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"

	"github.com/atedgimo/k8s-dencer/internal/model"
)

// MetricsServer reads actual usage from the metrics.k8s.io API — the
// aggregated API metrics-server (or a compatible adapter) serves.
//
// This is the Source that turns HasUsageData true, which has been false in
// every snapshot since M2. The interface's contract is honoured strictly:
// Available answers by asking the API rather than assuming, so a cluster
// whose metrics-server is installed-but-broken degrades back to "no data"
// instead of erroring every cycle — the Noop behaviour, chosen at runtime
// instead of compile time.
type MetricsServer struct {
	client metricsclient.Interface
}

// NewMetricsServer builds the source from the same rest config the informers
// use. It needs get/list on nodes.metrics.k8s.io and pods.metrics.k8s.io —
// read-only, and the chart grants exactly that when usage collection is on.
func NewMetricsServer(cfg *rest.Config) (*MetricsServer, error) {
	c, err := metricsclient.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("metrics client: %w", err)
	}
	return &MetricsServer{client: c}, nil
}

// Available asks the API rather than assuming: one cheap list, and any
// failure means "not available this cycle", never an error the collector has
// to route around.
func (m *MetricsServer) Available() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := m.client.MetricsV1beta1().NodeMetricses().List(ctx, metav1.ListOptions{Limit: 1})
	return err == nil
}

// NodeUsage returns observed usage keyed by node name.
func (m *MetricsServer) NodeUsage(ctx context.Context) (map[string]model.Resources, error) {
	list, err := m.client.MetricsV1beta1().NodeMetricses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list node metrics: %w", err)
	}
	out := make(map[string]model.Resources, len(list.Items))
	for _, item := range list.Items {
		out[item.Name] = model.Resources{
			MilliCPU:    item.Usage.Cpu().MilliValue(),
			MemoryBytes: item.Usage.Memory().Value(),
		}
	}
	return out, nil
}

// PodUsage returns observed usage keyed by "namespace/name", summed over
// containers the way requests are.
func (m *MetricsServer) PodUsage(ctx context.Context) (map[string]model.Resources, error) {
	list, err := m.client.MetricsV1beta1().PodMetricses("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list pod metrics: %w", err)
	}
	out := make(map[string]model.Resources, len(list.Items))
	for _, item := range list.Items {
		var r model.Resources
		for _, c := range item.Containers {
			r.MilliCPU += c.Usage.Cpu().MilliValue()
			r.MemoryBytes += c.Usage.Memory().Value()
		}
		out[item.Namespace+"/"+item.Name] = r
	}
	return out, nil
}
