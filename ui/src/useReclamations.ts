import { useEffect, useState } from "react";
import { onTokenChange } from "./auth";
import { api, Reclamation } from "./api";

export interface ReclamationState {
  awaiting: Reclamation[];
  /** No removal ever recorded AND no autoscaler visible — drained nodes may
   *  sit as pure cost. Absence of evidence, clearly labelled as only that. */
  noReclaimerEvidence: boolean;
  recent: Reclamation[];
  /** The server's own tallies, for the tray. The recent list is windowed;
   *  recounting it client-side would drift from what the server reports. */
  stats: {
    awaiting: number;
    reclaimed: number;
    /** The ledger: measured capacity actually returned. */
    reclaimedCpuMilli: number;
    reclaimedMemBytes: number;
    /** What that capacity was worth. Undefined when the operator has not
     *  configured prices — no claim, rather than a claim of zero. */
    pricing?: {
      currency: string;
      perMonth: number;
      pricedNodes: number;
      unpricedNodes: number;
      externalPerMonth: number;
      externalPriced: number;
    };
  };
}

const EMPTY: ReclamationState = {
  awaiting: [],
  noReclaimerEvidence: false,
  recent: [],
  stats: { awaiting: 0, reclaimed: 0, reclaimedCpuMilli: 0, reclaimedMemBytes: 0 },
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
            noReclaimerEvidence:
              r.reclaimer != null && !r.reclaimer.observedWorking && !r.reclaimer.detected,
            recent: r.recent ?? [],
            stats: {
              awaiting: r.stats.awaiting,
              reclaimed: r.stats.reclaimed,
              reclaimedCpuMilli: r.stats.reclaimedCpuMilli ?? 0,
              reclaimedMemBytes: r.stats.reclaimedMemBytes ?? 0,
              pricing: r.stats.pricing,
            },
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
    // Re-read the moment a token arrives.
    //
    // The app mounts before anyone signs in, so the first load above is
    // unauthenticated, 401s, and is swallowed by the catch below. Without
    // this line the screen then shows a confident zero until the interval
    // fires — measured at 0 findings on a cluster with 34, for a full
    // minute after sign-in, which is exactly when someone is looking.
    const stopAuth = onTokenChange(load);
    return () => {
      cancelled = true;
      clearInterval(t);
      stopAuth();
    };
  }, []);

  return state;
}
