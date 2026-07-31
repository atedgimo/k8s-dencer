package rest

import (
	"net/http"
	"sort"
)

// Right-sizing: requests versus observed usage, per workload.
//
// Consolidation packs *requests* — that is the correct input, because it is
// the scheduler's input. But it means oversized requests hold capacity that
// no amount of consolidation can free: a workload asking for 4 cores and
// using 200m keeps its slice of every bin it lands in. This report names
// those workloads, with measurements on both sides of the comparison.
//
// It refuses to guess. Without usage data the answer is "not available" and
// the reason, never an estimate — an invented utilisation figure would make
// confident, wrong recommendations, which is worse than none.

type rightsizingRow struct {
	// Workload is "namespace/kind/name" — the thing an operator recognises.
	Workload string `json:"workload"`
	Pods     int    `json:"pods"`
	// Requested and Used are summed over the workload's pods, in the same
	// units the rest of the API uses.
	RequestedMilli int64 `json:"requestedMilli"`
	UsedMilli      int64 `json:"usedMilli"`
	RequestedBytes int64 `json:"requestedBytes"`
	UsedBytes      int64 `json:"usedBytes"`
}

func (s *Server) handleRightsizing(w http.ResponseWriter, r *http.Request) {
	rec, err := s.record(r.Context(), "latest")
	if err != nil {
		s.fail(w, err)
		return
	}
	snap := rec.Snapshot

	if !snap.HasUsageData {
		// Not an error: a cluster without metrics-server is a normal cluster.
		// But it is also not a page of zeros pretending to be measurements.
		writeJSON(w, http.StatusOK, map[string]any{
			"available": false,
			"reason": "no usage data in the snapshot. Enable it with " +
				"planner.usageSource=metrics-server (requires metrics-server in the cluster); " +
				"without measurements this report refuses to estimate.",
		})
		return
	}

	type agg struct {
		row      rightsizingRow
		hasUsage bool
	}
	byWorkload := map[string]*agg{}
	for i := range snap.Pods {
		p := &snap.Pods[i]
		key := p.Namespace + "/"
		if p.Owner != nil {
			key += p.Owner.Kind + "/" + p.Owner.Name
		} else {
			key += "Pod/" + p.Name
		}
		a := byWorkload[key]
		if a == nil {
			a = &agg{row: rightsizingRow{Workload: key}}
			byWorkload[key] = a
		}
		a.row.Pods++
		a.row.RequestedMilli += p.Requests.MilliCPU
		a.row.RequestedBytes += p.Requests.MemoryBytes
		if p.Usage != nil {
			a.hasUsage = true
			a.row.UsedMilli += p.Usage.MilliCPU
			a.row.UsedBytes += p.Usage.MemoryBytes
		}
	}

	rows := []rightsizingRow{}
	var totReqMilli, totUsedMilli int64
	for _, a := range byWorkload {
		// A workload with no usage sample this cycle is skipped, not shown as
		// using zero: zero is the most damning number a report can print, and
		// here it would be an artifact of sampling, not a measurement.
		if !a.hasUsage {
			continue
		}
		rows = append(rows, a.row)
		totReqMilli += a.row.RequestedMilli
		totUsedMilli += a.row.UsedMilli
	}

	// Most over-requested first, by absolute CPU excess: the top of this list
	// is where fixing requests frees the most packable capacity.
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].RequestedMilli-rows[i].UsedMilli > rows[j].RequestedMilli-rows[j].UsedMilli
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"available":           true,
		"takenAt":             snap.TakenAt,
		"workloads":           rows,
		"totalRequestedMilli": totReqMilli,
		"totalUsedMilli":      totUsedMilli,
	})
}
