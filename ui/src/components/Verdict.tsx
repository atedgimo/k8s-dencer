import { GraphStats, Impact, PlanStep } from "../api";
import { GLYPH } from "./Impact";

/**
 * What the page opens with.
 *
 * Not a row of stat tiles. An operator arrives with one question — "what should
 * I do, and is it safe?" — and a tile reading "27" answers neither. This states
 * the recommendation in a sentence, breaks it down by how much confidence each
 * part needs, and puts the run control immediately beneath.
 *
 * The run controls do what they say. They queue the Green steps only —
 * Yellow needs a deliberate per-step choice, which is why the tally for it
 * filters the ledger rather than offering a button.
 */

interface Props {
  stats: GraphStats;
  steps: PlanStep[];
  onFocusRating: (impact: Impact | null) => void;
  focusedRating: Impact | null;
  onRun: (dryRun: boolean) => void;
  busy: boolean;
}

export default function Verdict({
  stats,
  steps,
  onFocusRating,
  focusedRating,
  onRun,
  busy,
}: Props) {
  const green = stats.ratings.Green ?? 0;
  const yellow = stats.ratings.Yellow ?? 0;
  const red = stats.ratings.Red ?? 0;

  return (
    <header className="verdict">
      <div className="verdict-main">
        <p className="verdict-eyebrow mono">consolidation plan</p>
        <h1 className="verdict-line">
          Reclaim <span className="verdict-figure num">{stats.reclaimed}</span> of{" "}
          <span className="num">{stats.nodesBefore}</span> nodes
        </h1>
        <p className="verdict-sub">
          {describe(green, yellow, red)}
        </p>
      </div>

      <div className="verdict-breakdown">
        <Tally
          impact="Green"
          count={green}
          caption="safe to run now"
          active={focusedRating === "Green"}
          onClick={() => onFocusRating(focusedRating === "Green" ? null : "Green")}
        />
        <Tally
          impact="Yellow"
          count={yellow}
          caption="needs confirmation"
          active={focusedRating === "Yellow"}
          onClick={() => onFocusRating(focusedRating === "Yellow" ? null : "Yellow")}
        />
        <Tally
          impact="Red"
          count={red}
          caption="held back"
          active={focusedRating === "Red"}
          onClick={() => onFocusRating(focusedRating === "Red" ? null : "Red")}
        />
      </div>

      <div className="verdict-actions">
        <button
          className="btn btn-primary"
          onClick={() => onRun(false)}
          disabled={green === 0 || busy}
          title={
            green === 0
              ? "No step in this plan is safe to run unattended."
              : "Cordon and drain the Green steps, in order."
          }
        >
          Run the {green} safe {green === 1 ? "step" : "steps"}
        </button>
        <button
          className="btn"
          onClick={() => onRun(true)}
          disabled={green === 0 || busy}
          title="Run the full Safety Guard and show the same trail, without touching anything."
        >
          Dry run
        </button>
        <p className="verdict-note">
          {steps.length} step{steps.length === 1 ? "" : "s"} · {stats.podsMoved} pods move
        </p>
      </div>
    </header>
  );
}

function Tally({
  impact,
  count,
  caption,
  active,
  onClick,
}: {
  impact: Impact;
  count: number;
  caption: string;
  active: boolean;
  onClick: () => void;
}) {
  return (
    <button
      className={`tally tally-${impact.toLowerCase()}${active ? " tally-active" : ""}`}
      onClick={onClick}
      disabled={count === 0}
      aria-pressed={active}
      aria-label={`${count} ${impact} steps, ${caption}`}
    >
      {/* Glyph, word and count together. A rating is never carried by colour
          alone — Red and Green are ΔE 4.1 apart under deuteranopia. */}
      <span className="tally-glyph" aria-hidden="true">
        {GLYPH[impact]}
      </span>
      <span className="tally-count num">{count}</span>
      <span className="tally-word">{impact}</span>
      <span className="tally-caption">{caption}</span>
    </button>
  );
}

/** One sentence of judgement, matched to what the plan actually contains. */
function describe(green: number, yellow: number, red: number): string {
  if (green === 0 && yellow === 0 && red === 0) {
    return "Nothing to consolidate. Every node is carrying work that cannot move.";
  }
  if (green === 0 && red > 0) {
    return "No step is safe to run unattended. Every candidate touches something that cannot be disrupted.";
  }
  if (green === 0) {
    return "Nothing runs unattended, but some steps can go ahead once you have reviewed them.";
  }
  const tail = red > 0 ? ` ${red} held back until a maintenance window exists.` : "";
  return `${green} step${green === 1 ? "" : "s"} can run now.${
    yellow > 0 ? ` ${yellow} more after you confirm.` : ""
  }${tail}`;
}
