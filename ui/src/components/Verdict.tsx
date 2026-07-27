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
 * The run button defaults to the Green steps — the safe, unattended answer —
 * but follows the ledger's selection the moment an operator ticks anything.
 * That is how Yellow gets run: it is executable on request and flagged, so it
 * requires a deliberate per-step choice rather than a bulk button. Red is not
 * offered at all, since the Safety Guard refuses it regardless.
 */

interface Props {
  stats: GraphStats;
  steps: PlanStep[];
  onFocusRating: (impact: Impact | null) => void;
  focusedRating: Impact | null;
  onRun: (dryRun: boolean) => void;
  busy: boolean;
  /** Steps ticked in the ledger. Empty means "the Green ones". */
  picked: PlanStep[];
  onClearPicked: () => void;
}

export default function Verdict({
  stats,
  steps,
  onFocusRating,
  focusedRating,
  onRun,
  busy,
  picked,
  onClearPicked,
}: Props) {
  const green = stats.ratings.Green ?? 0;
  const yellow = stats.ratings.Yellow ?? 0;
  const red = stats.ratings.Red ?? 0;

  const custom = picked.length > 0;
  const runCount = custom ? picked.length : green;
  const hasYellow = picked.some((s) => s.impact === "Yellow");

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
          disabled={runCount === 0 || busy}
          title={
            runCount === 0
              ? "No step is safe to run unattended. Tick the steps you want in the ledger."
              : custom
                ? "Cordon and drain the steps you have ticked, in order."
                : "Cordon and drain the Green steps, in order."
          }
        >
          {custom
            ? `Run ${runCount} selected step${runCount === 1 ? "" : "s"}`
            : `Run the ${green} safe step${green === 1 ? "" : "s"}`}
        </button>
        <button
          className="btn"
          onClick={() => onRun(true)}
          disabled={runCount === 0 || busy}
          title="Run the full Safety Guard and show the same trail, without touching anything."
        >
          Dry run
        </button>

        {custom ? (
          <p className="verdict-note">
            {hasYellow && <span className="verdict-flag">▲ includes flagged steps</span>}
            <button className="verdict-clear" onClick={onClearPicked}>
              clear selection
            </button>
          </p>
        ) : (
          <p className="verdict-note">
            {green === 0 && yellow > 0
              ? "Tick steps in the ledger to run them"
              : `${steps.length} step${steps.length === 1 ? "" : "s"} · ${stats.podsMoved} pods move`}
          </p>
        )}
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
