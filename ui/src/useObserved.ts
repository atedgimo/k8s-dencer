import { useMemo } from "react";
import { ObservedNode } from "./components/FieldViews";
import { ReclamationState } from "./useReclamations";
import { RunState } from "./useRun";

/**
 * Everything known about nodes from *outside* the plan's snapshot, keyed by
 * node name. Two sources, layered oldest-first:
 *
 *   - the reclamation tracker: nodes drained for real and what became of them
 *   - the current run's event trail: the executor saying what it just did,
 *     which matters most at exactly the moment the plan is pinned and its
 *     snapshot is at its most wrong
 *
 * A dry run is excluded outright. Its events say "would", and letting a
 * rehearsal mark nodes as actually drained would be the predicted/observed
 * confusion this overlay exists to end, rebuilt one layer up. Guarded by
 * test/ui, mutation-tested.
 */
export function useObserved(
  reclamations: ReclamationState,
  runState: RunState,
): Map<string, ObservedNode> {
  return useMemo(() => {
    const m = new Map<string, ObservedNode>();
    for (const r of reclamations.awaiting) {
      m.set(r.node, { reclaim: "awaiting" });
    }
    for (const r of reclamations.recent) {
      if (r.outcome === "reclaimed") m.set(r.node, { reclaim: "reclaimed" });
      // "returned" is deliberately not drawn: the node is back in service and
      // is, once again, just a node. The tray still counts it.
    }
    if (runState.status === "active" || runState.status === "done") {
      if (!runState.run.dryRun) {
        for (const e of runState.events) {
          if (!e.node) continue;
          if (e.action === "Cordon") {
            m.set(e.node, { ...m.get(e.node), cordoned: true });
          } else if (e.action === "Drained") {
            const cur = m.get(e.node);
            m.set(e.node, { ...cur, cordoned: true, reclaim: cur?.reclaim ?? "awaiting" });
          }
        }
      }
    }
    return m;
  }, [reclamations, runState]);
}
