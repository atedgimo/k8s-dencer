package reclaim

import (
	"strings"

	"github.com/atedgimo/k8s-dencer/internal/model"
)

// DetectReclaimer reports whether a known node-removing component is visible
// in the snapshot, and which one.
//
// The limits are the point, so they are stated here rather than discovered:
// this can only see reclaimers that run AS PODS in the cluster. GKE's
// cluster autoscaler runs on the managed control plane and is invisible to
// this scan — M23 watched it remove a node while nothing here could have
// detected it. Absence is therefore evidence of nothing. The trustworthy
// signal is the reclamation tracker's own history: a recorded removal proves
// a reclaimer works here, whatever this function says. Detection exists for
// the opposite case — warning BEFORE the first drain, when there is no
// history to consult yet.
func DetectReclaimer(snap *model.ClusterSnapshot) (string, bool) {
	for _, p := range snap.Pods {
		name := p.Labels["app.kubernetes.io/name"]
		switch {
		case name == "karpenter" || strings.HasPrefix(p.Name, "karpenter-"):
			return "Karpenter", true
		case name == "cluster-autoscaler" ||
			p.Labels["app"] == "cluster-autoscaler" ||
			strings.HasPrefix(p.Name, "cluster-autoscaler-"):
			return "cluster-autoscaler", true
		}
	}
	return "", false
}
