/**
 * The step list IS the screen (assets/design/README.md, 1a) — not a sidebar
 * showing 4 of 14 steps. Rows grouped under the three verdicts, each header
 * explaining what its verdict means; the row grid is the handoff's:
 * checkbox | order | node+pool | pods | verdict | reason | chevron.
 *
 * Row click focuses the step in the detail pane; the checkbox toggles
 * selection independently — reviewing and approving are different gestures.
 * Held-back rows mute the node name: they are facts to read, not work to
 * pick. The list scrolls under a bottom fade with a live "+N more" count,
 * because a clipped row without one reads as the end of the plan.
 */

import { useCallback, useEffect, useRef, useState } from "react";
import { GraphPayload, Impact, PlanStep } from "../../api";
import { VERDICT, VERDICT_NOTE } from "../Impact";

interface Props {
  steps: PlanStep[];
  graph: GraphPayload;
  checked: Set<number>;
  onToggle: (seq: number, shiftKey: boolean) => void;
  focused: number | null;
  onFocus: (seq: number | null) => void;
  filter: Impact | null;
  onFilter: (r: Impact | null) => void;
}

const ORDER: Impact[] = ["Green", "Yellow", "Red"];

export default function StepList({
  steps,
  graph,
  checked,
  onToggle,
  focused,
  onFocus,
  filter,
  onFilter,
}: Props) {
  const pools = poolIndex(graph);
  const groups = ORDER.map((r) => ({
    rating: r,
    rows: steps.filter((s) => s.impact === r),
  })).filter((g) => g.rows.length > 0 && (filter === null || filter === g.rating));

  const scrollRef = useRef<HTMLDivElement>(null);
  const [below, setBelow] = useState(0);

  // How many rows sit under the fold. Recomputed on scroll and when the list
  // changes shape; the fade without the number would read as "the plan ends
  // here" to anyone who does not think to scroll.
  const recount = useCallback(() => {
    const el = scrollRef.current;
    if (!el) return;
    const fold = el.scrollTop + el.clientHeight;
    let n = 0;
    for (const row of el.querySelectorAll<HTMLElement>("[data-step-row]")) {
      if (row.offsetTop + row.offsetHeight / 2 > fold) n++;
    }
    setBelow(n);
  }, []);

  useEffect(() => {
    recount();
    const el = scrollRef.current;
    if (!el) return;
    const ro = new ResizeObserver(recount);
    ro.observe(el);
    return () => ro.disconnect();
  }, [recount, steps, filter]);

  const total = steps.length;
  const counts = (r: Impact) => steps.filter((s) => s.impact === r).length;

  return (
    <section className="steplist" aria-label="Plan steps">
      <div className="steplist-toolbar">
        <span className="steplist-selected">
          <span className="steplist-selected-box" aria-hidden="true">
            {checked.size > 0 && (
              <svg viewBox="0 0 10 10">
                <path d="M1.5 5.2 4 7.5 8.5 2.5" strokeWidth="1.8" fill="none" strokeLinecap="round" />
              </svg>
            )}
          </span>
          {checked.size} of {total} selected
        </span>
        <span className="steplist-toolbar-sep" aria-hidden="true" />
        <div className="steplist-filters" role="group" aria-label="Filter by verdict">
          <button
            type="button"
            className={"steplist-filter" + (filter === null ? " is-on" : "")}
            onClick={() => onFilter(null)}
          >
            All {total}
          </button>
          {ORDER.map((r) => (
            <button
              key={r}
              type="button"
              className={"steplist-filter" + (filter === r ? " is-on" : "")}
              onClick={() => onFilter(filter === r ? null : r)}
            >
              {VERDICT[r]} {counts(r)}
            </button>
          ))}
        </div>
        <span className="steplist-order eyebrow mono">execution order</span>
      </div>

      <div className="steplist-scrollwrap">
        <div className="steplist-scroll" ref={scrollRef} onScroll={recount}>
          {groups.map((g) => (
            <div key={g.rating} className="stepgroup">
              <div className={"stepgroup-head stepgroup-" + g.rating.toLowerCase()}>
                <span className="stepgroup-swatch" aria-hidden="true" />
                <span className="stepgroup-label eyebrow mono">
                  {VERDICT[g.rating]} · {g.rows.length}
                </span>
                <span className="stepgroup-note mono">{VERDICT_NOTE[g.rating]}</span>
              </div>
              {g.rows.map((s) => {
                const isChecked = checked.has(s.sequenceNumber);
                const isFocused = focused === s.sequenceNumber;
                return (
                  <div
                    key={s.sequenceNumber}
                    data-step-row
                    className={
                      "steprow" +
                      (isFocused ? " is-focused" : "") +
                      (s.impact === "Red" ? " is-held" : "")
                    }
                    onClick={() => onFocus(s.sequenceNumber)}
                  >
                    <button
                      type="button"
                      role="checkbox"
                      aria-checked={isChecked}
                      aria-label={`Select step ${s.sequenceNumber}, ${s.targetNode ?? ""}`}
                      className={"steprow-box" + (isChecked ? " is-checked" : "")}
                      disabled={s.impact === "Red"}
                      onClick={(e) => {
                        e.stopPropagation();
                        onToggle(s.sequenceNumber, e.shiftKey);
                      }}
                    >
                      {isChecked && (
                        <svg viewBox="0 0 10 10" aria-hidden="true">
                          <path
                            d="M1.5 5.2 4 7.5 8.5 2.5"
                            strokeWidth="1.8"
                            fill="none"
                            strokeLinecap="round"
                          />
                        </svg>
                      )}
                    </button>
                    <span className="steprow-order mono">
                      {String(s.sequenceNumber).padStart(2, "0")}
                    </span>
                    <span className="steprow-node">
                      <span className="steprow-name mono">{s.targetNode}</span>
                      {s.targetNode && pools.get(s.targetNode) && (
                        <span className="steprow-pool">{pools.get(s.targetNode)}</span>
                      )}
                    </span>
                    <span className="steprow-pods mono">
                      {s.moves.length} pod{s.moves.length === 1 ? "" : "s"}
                    </span>
                    <span className={"steprow-verdict steprow-verdict-" + s.impact.toLowerCase()}>
                      {VERDICT[s.impact]}
                    </span>
                    <span className="steprow-why" title={s.rationale}>
                      {shortWhy(s)}
                    </span>
                    <span className="steprow-chevron" aria-hidden="true">
                      ›
                    </span>
                  </div>
                );
              })}
            </div>
          ))}
        </div>
        {below > 0 && (
          <div className="steplist-fade" aria-hidden="true">
            <span>
              {below} more step{below === 1 ? "" : "s"} below
            </span>
          </div>
        )}
      </div>
    </section>
  );
}

/** node name → pool chip text, joined from the graph's node metadata. */
function poolIndex(graph: GraphPayload): Map<string, string> {
  const m = new Map<string, string>();
  for (const e of graph.elements) {
    if (e.data.kind !== "node") continue;
    const pool = e.data.instanceType || e.data.capacityType;
    if (pool) m.set(e.data.label, pool);
  }
  return m;
}

/**
 * The row's one-line reason. Structured reasons name the rule; otherwise the
 * rationale minus its opening "Draining X moves N pod(s)" clause, which the
 * row already says in its own columns. Rows are for scanning — the full text
 * lives in the detail pane.
 */
function shortWhy(s: PlanStep): string {
  const r = s.reasons?.[0];
  if (r) return r.subject ? `${r.kind}: ${r.subject}` : r.kind;
  const stop = s.rationale.indexOf(". ");
  return stop > 0 ? s.rationale.slice(stop + 2) : s.rationale;
}
