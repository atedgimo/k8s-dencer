package rest

import (
	"net/http"
	"time"
)

// Maintenance windows: whether Red steps can run, and if not, when they could.
//
// The UI has told operators "Maintenance window only" on every Red step since
// the redesign and given them no way to see whether a window is open, when the
// next one starts, or which windows exist at all. Worse, the window package
// resolves every failure to CLOSED and writes a reason for each — an
// unparseable cron, an unknown timezone, a suspended window — and nobody could
// read any of them. A typo meant Red steps silently never became runnable.
//
// Evaluated per request rather than cached. The package's own doc is explicit
// that a cached copy of a live authorisation is not one, and "is a window open
// right now" is a question about now.
func (s *Server) handleWindows(w http.ResponseWriter, r *http.Request) {
	if s.windows == nil {
		// No reader configured: say so rather than implying no windows exist.
		// "None defined" and "not looking" are different answers and only one
		// of them means Red can never run.
		writeJSON(w, http.StatusOK, map[string]any{
			"available": false,
			"reason":    "this deployment cannot read maintenance windows",
		})
		return
	}

	set, err := s.windows.Current(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}

	states := set.States()
	out := make([]map[string]any, 0, len(states))
	for _, st := range states {
		row := map[string]any{
			"name":      st.Name,
			"open":      st.Open,
			"allowsRed": st.AllowsRed,
			"reason":    st.Reason,
		}
		if !st.ClosesAt.IsZero() {
			row["closesAt"] = st.ClosesAt.UTC()
		}
		if !st.NextOpen.IsZero() {
			row["nextOpen"] = st.NextOpen.UTC()
		}
		if len(st.Selector) > 0 {
			row["selector"] = st.Selector
		}
		if st.MaxNodes > 0 {
			row["maxNodes"] = st.MaxNodes
		}
		out = append(out, row)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"available":   true,
		"evaluatedAt": time.Now().UTC(),
		// anyOpen is the question the top bar asks: can anything Red run at
		// all right now. Per-node coverage is a different question and the
		// step detail is where it belongs.
		"anyOpen": set.Any(),
		"windows": out,
	})
}
