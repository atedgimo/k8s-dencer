import { useEffect, useState } from "react";
import { onTokenChange } from "./auth";
import { api, Recommendation } from "./api";

/**
 * The fix queue, fetched once for the whole app.
 *
 * Lifted out of the Recommendations component when the redesign gave the
 * queue two readers at once: the rail badge (high-severity count) and the
 * Recommendations destination. Two components polling the same endpoint on
 * their own timers would disagree with each other for up to a minute — the
 * badge saying 29 while the page says 31 — which is exactly the kind of
 * self-contradiction an operator reads as "this tool is guessing".
 */
export function useRecommendations(): Recommendation[] | null {
  const [recs, setRecs] = useState<Recommendation[] | null>(null);

  useEffect(() => {
    let cancelled = false;
    const load = () => {
      api
        .recommendations()
        .then((d) => {
          if (!cancelled) setRecs(d.recommendations);
        })
        .catch(() => {
          // Advice is supplementary; a failure must never blank the rail.
        });
    };
    load();
    const t = setInterval(load, 60_000);
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

  return recs;
}
