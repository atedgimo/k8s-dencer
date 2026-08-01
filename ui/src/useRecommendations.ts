import { useEffect, useState } from "react";
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
    return () => {
      cancelled = true;
      clearInterval(t);
    };
  }, []);

  return recs;
}
