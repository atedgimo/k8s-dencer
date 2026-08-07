/**
 * Why the plan is empty.
 *
 * An empty plan used to render as a headline and nothing else — and it is the
 * state a healthy cluster spends most of its life in. On a real GKE cluster an
 * operator scaled workloads down, watched utilisation fall, saw no plan appear
 * and had no way to find out why. The planner knew the whole time: every node
 * undrainable, with a named pod and a named reason for each.
 *
 * That answer is not an absence of information. It is a full constraint
 * analysis with a definite conclusion about every node, and a product whose
 * pitch is that it explains itself owes the reader that conclusion.
 *
 * The data comes from preflight, which already answers exactly this question
 * per node and now answers it usefully — before DaemonSets stopped counting as
 * blockers it reported "0 of N nodes will drain cleanly" on every managed
 * cluster in existence.
 */

import { useEffect, useMemo, useState } from "react";
import { GraphPayload, Preflight, api, formatCPU } from "../../api";

/** This product targets a thousand nodes; a thousand rows is not an
 *  explanation, it is the same explanation a thousand times. */
const MAX_ROWS = 8;

/** formatCPU drops the unit above a core, so "cores" is only right above it. */
const cpu = (milli: number) => (milli >= 1000 ? `${formatCPU(milli)} cores` : formatCPU(milli));

/**
 * The constructive half: what would have to change for this node to drain.
 *
 * Only for capacity blockers. A PodDisruptionBudget at zero headroom is not
 * unlocked by freeing CPU somewhere, and saying so would send someone to do
 * work that changes nothing — worse than saying nothing at all.
 */
function unlock(
  blocker: { pod: string; kind: string } | undefined,
  requestOf: Map<string, number>,
  room: { best: number; bestNode: string },
) {
  if (!blocker || blocker.kind !== "Resources") return null;
  const need = requestOf.get(blocker.pod);
  if (!need || need <= 0) return null;
  const short = need - room.best;
  if (short <= 0) return null; // it would fit; something else is in the way
  return (
    <span className="whynothing-unlock">
      {" "}
      Free {cpu(short)} more anywhere and this pod has somewhere to go
      {room.bestNode ? ` — ${room.bestNode} has the most room today, ${cpu(room.best)}` : ""}.
    </span>
  );
}

export default function WhyNothing({ graph }: { graph: GraphPayload }) {
  const [data, setData] = useState<Preflight | null>(null);
  const [failed, setFailed] = useState(false);

  // What the cluster could absorb right now: the largest single gap between a
  // node's ceiling and what it already holds. A pod moves whole, so the
  // biggest gap is the only one that decides whether it can move at all.
  const room = useMemo(() => {
    const ceiling = graph.stats.packCeiling && graph.stats.packCeiling > 0 ? graph.stats.packCeiling : 1;
    let best = 0;
    let bestNode = "";
    for (const e of graph.elements) {
      if (e.data.kind !== "node" || e.data.cordoned) continue;
      const gap = (e.data.cpuAllocatable ?? 0) * ceiling - (e.data.cpuRequested ?? 0);
      if (gap > best) {
        best = gap;
        bestNode = e.data.label;
      }
    }
    return { best, bestNode };
  }, [graph]);

  const requestOf = useMemo(() => {
    const m = new Map<string, number>();
    for (const e of graph.elements) {
      if (e.data.kind === "pod") m.set(e.data.id.replace(/^pod:/, ""), e.data.cpuRequest ?? 0);
    }
    return m;
  }, [graph]);

  useEffect(() => {
    let cancelled = false;
    api
      .preflight()
      .then((d) => !cancelled && setData(d))
      .catch(() => !cancelled && setFailed(true));
    return () => {
      cancelled = true;
    };
  }, []);

  if (failed || !data) return null;

  // A node with no pods and no blockers is already empty — worth saying,
  // because it is the one case where the answer is "nothing is wrong".
  const held = data.nodes.filter((n) => !n.drainable);
  const free = data.nodes.filter((n) => n.drainable);

  return (
    <section className="whynothing">
      <span className="eyebrow mono">Why there is nothing to do</span>
      <p className="whynothing-lead">
        {held.length === 0
          ? `All ${data.total} nodes could drain, but moving their pods would not free a node — the workloads need the room they are using.`
          : `${held.length} of ${data.total} node${data.total === 1 ? "" : "s"} cannot be freed right now. Each one is held by something specific:`}
      </p>

      {held.length > 0 && (
        <ul className="whynothing-list">
          {held.slice(0, MAX_ROWS).map((n) => {
            // One line per node: the first blocker is the story, the rest are
            // the same story repeated.
            const first = n.blockers[0];
            const more = n.blockers.length - 1;
            return (
              <li key={n.node} className="whynothing-row">
                <span className="whynothing-node mono">{n.node}</span>
                <span className="whynothing-why">
                  {first ? (
                    <>
                      <span className="whynothing-pod mono">{first.pod}</span> — {first.explanation}
                      {more > 0 && (
                        <span className="whynothing-more">
                          {" "}
                          and {more} other{more === 1 ? "" : "s"} on this node
                        </span>
                      )}
                      {unlock(first, requestOf, room)}
                    </>
                  ) : n.cordoned ? (
                    "Cordoned, so nothing new can land here and it is already out of the pool."
                  ) : (
                    "Its workloads have nowhere else to go with room to spare."
                  )}
                </span>
              </li>
            );
          })}
        </ul>
      )}

      {held.length > MAX_ROWS && (
        <p className="whynothing-foot">
          and {held.length - MAX_ROWS} more held the same way — run{" "}
          <span className="mono">dencer preflight</span> for the full list.
        </p>
      )}

      {free.length > 0 && held.length > 0 && (
        <p className="whynothing-foot">
          {free.length} node{free.length === 1 ? "" : "s"} could drain, but emptying{" "}
          {free.length === 1 ? "it" : "them"} would not free a node worth reclaiming.
        </p>
      )}
    </section>
  );
}
