/**
 * The 392px detail pane (assets/design/README.md, 1a): the focused step's
 * verdict, its risk in plain language, the Safety Guard check list, and
 * where each pod goes.
 *
 * The layout rule that bit the designers twice, kept on purpose: "Where the
 * pods go" is `flex: 0 0 auto` — a payload list must never lose a row,
 * because a missing pod reads as "placed elsewhere", and the dropped row is
 * exactly the one carrying the `only replica` flag. The Safety Guard list is
 * the scroll region (`flex: 1 1 0; min-height: 0; overflow: hidden`) with a
 * fade and an explicit "+N more": a clipped row in a uniform list reads as
 * "scroll", which is true there and only there.
 */

import { useEffect, useState } from "react";
import { Impact, PlanStep, StepDetail as StepDetailData, api } from "../../api";
import { GLYPH, VERDICT_LABEL } from "../Impact";

interface Props {
  planId: string;
  step: PlanStep | null;
  pool?: string;
  checked: boolean;
  stale: boolean;
  onAdd: (seq: number) => void;
  onSkip: (seq: number) => void;
}

interface Check {
  label: string;
  value: string;
  rating: Impact;
  detail?: string;
}

export default function StepDetail({ planId, step, pool, checked, stale, onAdd, onSkip }: Props) {
  const [detail, setDetail] = useState<StepDetailData | null>(null);

  useEffect(() => {
    setDetail(null);
    if (!step) return;
    const ctl = new AbortController();
    api
      .step(planId, step.sequenceNumber, ctl.signal)
      .then(setDetail)
      .catch(() => {
        // The pane degrades to what the step row already carried; a fetch
        // failure must not blank the review.
      });
    return () => ctl.abort();
  }, [planId, step]);

  if (!step) {
    return (
      <aside className="stepdetail stepdetail-empty">
        <p>Select a step to see its Safety Guard checks and where its pods go.</p>
      </aside>
    );
  }

  const singletons = new Set(detail?.singletons ?? []);
  const checks = detail ? buildChecks(detail) : [];
  const shown = checks;

  return (
    <aside className="stepdetail">
      <div className="stepdetail-head">
        <div className={"stepdetail-verdict stepdetail-verdict-" + step.impact.toLowerCase()}>
          <span aria-hidden="true">{GLYPH[step.impact]}</span>
          <span className="eyebrow mono">
            {VERDICT_LABEL[step.impact]} · step {String(step.sequenceNumber).padStart(2, "0")}
          </span>
        </div>
        <div className="stepdetail-node">
          <span className="stepdetail-name mono">{step.targetNode}</span>
          {pool && <span className="stepdetail-pool">{pool}</span>}
        </div>
        <p className="stepdetail-why">{step.rationale}</p>
      </div>

      <div className="stepdetail-checks">
        <span className="eyebrow mono">Safety Guard</span>
        <div className="stepdetail-checks-list">
          {shown.map((c) => (
            <div key={c.label} className="check" title={c.detail}>
              <span
                className={"check-glyph check-" + c.rating.toLowerCase()}
                aria-hidden="true"
              >
                {GLYPH[c.rating]}
              </span>
              <span className="check-label">{c.label}</span>
              <span className={"check-value mono check-" + c.rating.toLowerCase()}>{c.value}</span>
            </div>
          ))}
          {detail === null && <div className="check check-loading">Reading the guard…</div>}
        </div>
        <div className="stepdetail-checks-fade" aria-hidden="true" />
      </div>

      <div className="stepdetail-moves">
        <span className="eyebrow mono">Where the pods go</span>
        {step.moves.map((m) => {
          const key = `${m.namespace}/${m.pod}`;
          return (
            <div key={key} className="move">
              <span className="move-pod mono" title={key}>
                {m.pod}
              </span>
              <span className="move-arrow" aria-hidden="true">
                →
              </span>
              <span className="move-to mono">{m.toNode}</span>
              {singletons.has(key) && <span className="move-flag">only replica</span>}
            </div>
          );
        })}
      </div>

      <div className="stepdetail-actions">
        <div className="stepdetail-buttons">
          <button
            type="button"
            className="btn stepdetail-add"
            disabled={step.impact === "Red" || stale}
            onClick={() => onAdd(step.sequenceNumber)}
          >
            {checked ? "Remove from selection" : "Add to selection"}
          </button>
          <button
            type="button"
            className="btn"
            disabled={!checked}
            onClick={() => onSkip(step.sequenceNumber)}
          >
            Skip
          </button>
        </div>
        <span className="stepdetail-note">
          {step.impact === "Red"
            ? "Held back — there is no override in this UI. A refusal is the product working."
            : "Adding this step also adds a rollback point before it."}
        </span>
      </div>
    </aside>
  );
}

/**
 * The six guard categories, synthesised from the step's pod constraints and
 * the singleton flags. Values are facts, not colours: each row is a category
 * the guard actually evaluates, and its worst finding across the moved pods.
 */
function buildChecks(detail: StepDetailData): Check[] {
  const cons = detail.constraints.flatMap((pc) => pc.constraints ?? []);
  const of = (kinds: string[]) => cons.filter((c) => kinds.includes(c.kind));

  const category = (label: string, kinds: string[], okValue: string): Check => {
    const hits = of(kinds);
    const blocking = hits.filter((c) => c.blocking);
    if (blocking.length > 0) {
      return {
        label,
        value: blocking[0].subject || "blocks the drain",
        rating: "Red",
        detail: blocking[0].explanation,
      };
    }
    if (hits.length > 0) {
      return {
        label,
        value: hits[0].subject ? `${hits[0].subject}` : `${hits.length} to weigh`,
        rating: "Yellow",
        detail: hits[0].explanation,
      };
    }
    return { label, value: okValue, rating: "Green" };
  };

  const singles = detail.singletons?.length ?? 0;
  const placed = detail.step.moves.filter((m) => m.toNode).length;

  const checks: Check[] = [
    category("PodDisruptionBudgets", ["PodDisruptionBudget"], "none applies"),
    category("Topology spread", ["TopologySpread"], "unaffected"),
    category(
      "Affinity / anti-affinity",
      ["NodeAffinity", "PodAffinity", "PodAntiAffinity", "NodeSelector"],
      "satisfied",
    ),
    category("Taints & tolerations", ["Taint"], "satisfied"),
    {
      label: "Single-replica workloads",
      value: singles > 0 ? `${singles} found` : "none",
      rating: singles > 0 ? "Yellow" : "Green",
      detail:
        singles > 0
          ? "Evicting an only replica takes its workload to zero while rescheduling runs."
          : undefined,
    },
    {
      label: "Destination capacity",
      value: `${placed} of ${detail.step.moves.length} placed`,
      rating: placed === detail.step.moves.length ? "Green" : "Red",
    },
  ];

  // Kinds outside the six categories still deserve a row when present —
  // hands-off annotations, local storage, controller pinning.
  const extras: Array<[string, string]> = [
    ["DoNotDisrupt", "Hands-off annotation"],
    ["PersistentVolume", "Local storage"],
    ["ControllerPinned", "Controller-pinned"],
  ];
  for (const [kind, label] of extras) {
    const hits = of([kind]);
    if (hits.length === 0) continue;
    const blocking = hits.some((c) => c.blocking);
    checks.push({
      label,
      value: hits[0].subject || `${hits.length} pod${hits.length === 1 ? "" : "s"}`,
      rating: blocking ? "Red" : "Yellow",
      detail: hits[0].explanation,
    });
  }

  return checks;
}
