import { useEffect, useState } from "react";
import { api, Reclamation } from "./api";

export interface ReclamationState {
  awaiting: Reclamation[];
  recent: Reclamation[];
  /** The server's own tallies, for the tray. The recent list is windowed;
   *  recounting it client-side would drift from what the server reports. */
  stats: { awaiting: number; reclaimed: number };
}

const EMPTY: ReclamationState = {
  awaiting: [],
  recent: [],
  stats: { awaiting: 0, reclaimed: 0 },
};

/**
 * Observed reclamation, as distinct from what the plan predicts. Polled
 * rather than pushed: it changes on the planner's resync, which is tens of
 * seconds, and it is never the reason someone is watching the screen.
 *
 * The full lists, not just the tallies. The names in them are the difference
 * between "2 awaiting" in a tray and the actual node on the field being
 * marked — the tray version shipped first and a user immediately asked
 * *which* node.
 */
export function useReclamations(): ReclamationState {
  const [state, setState] = useState<ReclamationState>(EMPTY);

  useEffect(() => {
    let cancelled = false;
    const load = async () => {
      try {
        const r = await api.reclamations();
        if (!cancelled && r.tracking) {
          setState({
            awaiting: r.awaiting ?? [],
            recent: r.recent ?? [],
            stats: { awaiting: r.stats.awaiting, reclaimed: r.stats.reclaimed },
          });
        }
      } catch {
        // Reclamation tracking is supplementary. A backend that does not have
        // it, or a transient failure, must never blank the page — the field
        // and the ledger are what the operator came for.
      }
    };
    void load();
    const t = setInterval(load, 30_000);
    return () => {
      cancelled = true;
      clearInterval(t);
    };
  }, []);

  return state;
}
