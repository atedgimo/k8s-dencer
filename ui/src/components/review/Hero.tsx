/**
 * The hero band — what approval buys (assets/design/README.md, 1a).
 *
 * "Reclaim 3 of 24 nodes" states what running everything safe achieves
 * today; the held-back ceiling is the secondary clause, never the headline,
 * and never a call to action — it is a ceiling, not a plan you can run. The
 * three stats price the safe selection in cores, memory and pods, summed
 * from the drained nodes' allocatable, which is what actually returns to
 * the pool.
 *
 * The triage bar and cards are the same three numbers the old verdict panel
 * carried, now wearing the verdict vocabulary: Safe now / Needs a call /
 * Held back. Cards filter the list below on click.
 */

import { GraphPayload, Impact, PlanStep, formatBytes, formatCPU } from "../../api";
import { GLYPH, VERDICT_LABEL } from "../Impact";

interface Props {
  graph: GraphPayload;
  steps: PlanStep[];
  focusedRating: Impact | null;
  onFocusRating: (r: Impact | null) => void;
}

export default function Hero({ graph, steps, focusedRating, onFocusRating }: Props) {
  const byRating = (r: Impact) => steps.filter((s) => s.impact === r);
  // Nodes needing no step because nothing on them can or must move.
  const free = graph.stats.alreadyReclaimable ?? 0;
  // What running this plan would stop costing. Deliberately on the screen
  // where someone decides whether to run it: the measured figure lives on
  // History, which is the honest place for it and four clicks from here.
  const forecast = graph.stats.forecast;
  const safe = byRating("Green");
  const caution = byRating("Yellow");
  const held = byRating("Red");

  // What the safe steps return to the pool: the allocatable of the nodes
  // they drain, joined through the graph. Nodes are not fungible — the count
  // alone cannot say whether the plan is worth running.
  const nodesByName = new Map(
    graph.elements.filter((e) => e.data.kind === "node").map((e) => [e.data.label, e.data]),
  );
  let cpu = 0;
  let mem = 0;
  let pods = 0;
  for (const s of safe) {
    const n = s.targetNode ? nodesByName.get(s.targetNode) : undefined;
    cpu += n?.cpuAllocatable ?? 0;
    mem += n?.memAllocatable ?? 0;
    pods += s.moves.length;
  }

  return (
    <div className="hero">
      <div className="hero-lead">
        <span className="eyebrow mono">
          {safe.length > 0 ? "If you approve everything safe" : free > 0 ? "Free capacity" : "This cluster"}
        </span>
        {safe.length > 0 ? (
          <div className="hero-line">
            <h2 className="hero-headline">
              Reclaim {safe.length} of {graph.stats.nodesBefore} nodes
            </h2>
            <span className="hero-sub">
              now
              {steps.length > safe.length && (
                <>
                  {" · "}
                  <span className="hero-sub-strong">{steps.length} reclaimable</span> if the
                  held-back rules are resolved
                </>
              )}
            </span>
          </div>
        ) : (
          <div className="hero-line">
            <h2 className="hero-headline">
              {free > 0
                ? `${free} node${free === 1 ? "" : "s"} ${free === 1 ? "is" : "are"} already free to take`
                : steps.length > 0
                  ? "Nothing is safely reclaimable right now"
                  : // The state a well-run cluster spends most of its life in.
                    // It used to read as an absence — "nothing is safely
                    // reclaimable" — which describes a failure and a success
                    // with the same sentence. This is the success.
                    "This cluster is packed as tightly as its rules allow"}
            </h2>
            {free > 0 && (
              // No step, because there is nothing to relocate — but saying
              // nothing at all is how three idle GKE nodes went unmentioned
              // while the autoscaler quietly removed them.
              <span className="hero-sub">
                {free === 1 ? "It holds" : "They hold"} only DaemonSets and static pods.{" "}
                <span className="hero-sub-strong">Draining {free === 1 ? "it" : "them"} moves nothing</span>
                {" "}— most autoscalers remove {free === 1 ? "it" : "them"} unprompted.
              </span>
            )}
            {steps.length === 0 && free === 0 && (
              <span className="hero-sub">
                Every node is either needed or held by a rule. Nothing to
                approve — which is the point.
              </span>
            )}
            {steps.length > 0 && (
              <span className="hero-sub">
                <span className="hero-sub-strong">{steps.length} reclaimable</span> if the rules
                below are resolved — start with Recommendations
              </span>
            )}
          </div>
        )}
        {forecast && forecast.pricedNodes > 0 && (
          // A rate, not a running total, and named as a forecast: the ledger
          // on History reports what was measured after the fact, and the two
          // numbers must never be mistaken for each other.
          <p className="hero-worth">
            Worth <strong>{fmtMoney(forecast.currency, forecast.perMonth)}/month</strong> if run in
            full
            {forecast.unpricedNodes > 0 && (
              <span className="hero-worth-gap">
                {" "}
                · {forecast.unpricedNodes} node{forecast.unpricedNodes === 1 ? "" : "s"} unpriced,
                so the real figure is higher
              </span>
            )}
          </p>
        )}
        {safe.length > 0 && (
          <div className="hero-stats">
            <div className="hero-stat">
              <span className="hero-stat-figure mono">{formatCPU(cpu)} cores</span>
              <span className="hero-stat-label">returned to the pool</span>
            </div>
            <span className="hero-stat-sep" aria-hidden="true" />
            <div className="hero-stat">
              <span className="hero-stat-figure mono">{formatBytes(mem)}</span>
              <span className="hero-stat-label">memory freed</span>
            </div>
            <span className="hero-stat-sep" aria-hidden="true" />
            <div className="hero-stat">
              <span className="hero-stat-figure mono">
                {pods} pod{pods === 1 ? "" : "s"}
              </span>
              <span className="hero-stat-label">rescheduled, 0 downtime</span>
            </div>
          </div>
        )}
      </div>

      <div className="hero-triage">
        <div className="triage-bar" aria-hidden="true">
          {safe.length > 0 && <div className="triage-seg triage-safe" style={{ flex: safe.length }} />}
          {caution.length > 0 && (
            <div className="triage-seg triage-caution" style={{ flex: caution.length }} />
          )}
          {held.length > 0 && <div className="triage-seg triage-held" style={{ flex: held.length }} />}
        </div>
        <div className="triage-cards">
          {(
            [
              ["Green", safe.length],
              ["Yellow", caution.length],
              ["Red", held.length],
            ] as Array<[Impact, number]>
          ).map(([rating, count]) => (
            <button
              key={rating}
              type="button"
              className={
                "triage-card triage-card-" +
                rating.toLowerCase() +
                (focusedRating === rating ? " is-on" : "")
              }
              aria-pressed={focusedRating === rating}
              onClick={() => onFocusRating(focusedRating === rating ? null : rating)}
            >
              <span className="triage-count">
                <span aria-hidden="true" className="triage-glyph">
                  {GLYPH[rating]}
                </span>
                <span className="num">{count}</span>
              </span>
              <span className="triage-label">{VERDICT_LABEL[rating]}</span>
            </button>
          ))}
        </div>
      </div>
    </div>
  );
}

/** Money, at the precision a monthly rate deserves and no more. */
function fmtMoney(currency: string, amount: number): string {
  try {
    return new Intl.NumberFormat(undefined, {
      style: "currency",
      currency,
      maximumFractionDigits: amount < 100 ? 2 : 0,
    }).format(amount);
  } catch {
    // An unknown currency code must not blank the figure it is attached to.
    return `${amount.toFixed(2)} ${currency}`;
  }
}
