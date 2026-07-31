import { useEffect, useState } from "react";
import { api, VersionResponse } from "./api";

/**
 * The server's version payload, polled — not fetched once — because this
 * response carries the only liveness signal the client gets.
 *
 * A plan re-publishes when its *content* changes. On a steady cluster it
 * never does — which is the healthiest possible state — so a client that
 * fetched this once would sit on the storedAt it was handed at load and watch
 * it age past the staleness threshold while the planner went on confirming
 * the plan every resync. The warning fired precisely when nothing was wrong.
 * Re-reading planConfirmedAt is what makes "confirmed just now" stay true,
 * and what makes the warning mean the thing it says.
 *
 * Identity and cluster label ride along unchanged; they are cheap and cannot
 * drift within a session.
 *
 * Guarded by test/ui: the poll and the version fetch must stay in one
 * effect, or planConfirmedAt freezes at page load.
 */
export function useVersion(): VersionResponse | null {
  const [server, setServer] = useState<VersionResponse | null>(null);

  useEffect(() => {
    let cancelled = false;
    const load = () => {
      api
        .version()
        .then((v) => {
          if (!cancelled) setServer(v);
        })
        .catch(() => {
          // Leave the last good response in place. A dropped poll is not
          // evidence the plan went stale, and blanking the header over one
          // failed request would be its own kind of lie.
        });
    };
    load();
    const t = setInterval(load, 30_000);
    return () => {
      cancelled = true;
      clearInterval(t);
    };
  }, []);

  return server;
}
