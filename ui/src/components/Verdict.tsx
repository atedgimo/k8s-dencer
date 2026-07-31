import { useEffect, useState } from "react";
import { formatBytes, formatCPU, GraphStats, Impact, PlanStep } from "../api";
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
  /**
   * When the plan was last confirmed against the cluster — the store's
   * storedAt, not the plan's generatedAt.
   *
   * An unchanged plan keeps its original generatedAt however many times the
   * planner re-verifies it, so that field reported a perfectly current plan as
   * hours stale. Confirmation is the question a reader is actually asking.
   *
   * It must be *re-read*, not taken once. The store touches it every resync,
   * but a plan only re-publishes when its content changes, so a client that
   * fetched this at load and never asked again ages a plan the planner is
   * actively confirming. App polls the version endpoint for it.
   */
  confirmedAt: string;
  onFocusRating: (impact: Impact | null) => void;
  focusedRating: Impact | null;
  onRun: (dryRun: boolean) => void;
  /** Opens the closed-loop consent sheet. */
  onConverge: () => void;
  busy: boolean;
  /** Steps ticked in the ledger. Empty means "the Green ones". */
  picked: PlanStep[];
  onClearPicked: () => void;
}

export default function Verdict({
  stats,
  steps,
  confirmedAt,
  onFocusRating,
  focusedRating,
  onRun,
  onConverge,
  busy,
  picked,
  onClearPicked,
}: Props) {
  const green = stats.ratings.Green ?? 0;
  const yellow = stats.ratings.Yellow ?? 0;
  const red = stats.ratings.Red ?? 0;

  const custom = picked.length > 0;
  // What the button would actually run: the ticked steps, or the Green ones.
  const willRun = custom ? picked : steps.filter((s) => s.impact === "Green");
  const runCount = willRun.length;
  const hasYellow = willRun.some((s) => s.impact === "Yellow");
  const podsMoving = willRun.reduce((n, s) => n + s.moves.length, 0);
  const nodeNames = willRun.map((s) => s.targetNode).filter(Boolean) as string[];
  const laterCount = steps.length - runCount;

  return (
    <header className="verdict">
      <div className="verdict-main">
        <p className="verdict-eyebrow mono">
          consolidation plan <PlanAge confirmedAt={confirmedAt} />
        </p>
        <h1 className="verdict-line">
          Reclaim <span className="verdict-figure num">{stats.reclaimable}</span> of{" "}
          <span className="num">{stats.nodesBefore}</span> nodes
          {/* What the count is worth. Nodes are not fungible: "15 nodes" may
              be a rack of 96-core machines or a drawer of 2-core ones, and
              the count alone cannot say whether this plan matters. Zero is
              omitted rather than rendered — "0 cores" under a real headline
              would read as a bug, and the one honest case for it (a plan
              with no steps) already says so in words. */}
          {stats.cpuReclaimableMilli > 0 && (
            <span className="verdict-capacity num">
              {" "}
              · {formatCPU(stats.cpuReclaimableMilli)} cores ·{" "}
              {formatBytes(stats.memReclaimableBytes)}
            </span>
          )}
        </h1>
        {/* The nodes, named. "Run the 5 safe steps" is abstract; a list of
            machines is something an operator can picture and sanity-check
            against what they know about their own cluster. */}
        {nodeNames.length > 0 ? (
          <p className="verdict-sub">
            Next: drain{" "}
            <span className="verdict-nodes mono">{nodeNames.slice(0, 4).join(", ")}</span>
            {nodeNames.length > 4 && (
              <span className="verdict-nodes-more"> +{nodeNames.length - 4} more</span>
            )}
            . <span className="verdict-cost">{podsMoving} pods move</span>
            {laterCount > 0 && `, ${laterCount} step${laterCount === 1 ? "" : "s"} left after this`}.
          </p>
        ) : (
          <p className="verdict-sub">{describe(green, yellow, red)}</p>
        )}
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
        {/* The closed loop's door. Quieter than the primary action on
            purpose: approving a policy deserves a deliberate step through
            the consent sheet, not a one-click reflex beside it. */}
        <button
          className="btn btn-quiet"
          onClick={onConverge}
          disabled={busy}
          title="Approve a bounded policy: re-plan after every drain until nothing worthwhile remains."
        >
          Run to optimum…
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

/**
 * How old the plan is.
 *
 * The honest uncertainty signal. Measured plan stability is 95–100% after a
 * step runs, so dimming later steps would misrepresent them — what actually
 * ages a plan is the cluster changing underneath it, and age is the only
 * proxy for that the UI has. Ticks on its own so the number stays true
 * without waiting for a re-fetch.
 */
function PlanAge({ confirmedAt }: { confirmedAt: string }) {
  const [, tick] = useState(0);
  useEffect(() => {
    const t = setInterval(() => tick((n) => n + 1), 15_000);
    return () => clearInterval(t);
  }, []);

  const seconds = Math.max(0, (Date.now() - new Date(confirmedAt).getTime()) / 1000);
  const stale = seconds > STALE_AFTER_SECONDS;
  return (
    <span className={stale ? "verdict-age verdict-age-stale" : "verdict-age"}>
      · confirmed {describeAge(seconds)}
      {stale && " — the planner has stopped confirming it"}
    </span>
  );
}

/**
 * How long without a confirmation before the screen stops vouching for itself.
 *
 * Ten missed resyncs at the 30s default. The old wording — "the cluster may
 * have moved on" — described the opposite of what this measures. A confirmation
 * is refreshed whether or not the plan changes, so silence here does not mean
 * the cluster drifted; it means **nothing is watching it any more**: the
 * planner wedged, snapshots failing, the poll dead. That is a different
 * problem, it is worse, and it deserves to be named.
 */
const STALE_AFTER_SECONDS = 300;

function describeAge(seconds: number): string {
  if (seconds < 45) return "just now";
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes} minute${minutes === 1 ? "" : "s"} ago`;
  const hours = Math.round(minutes / 60);
  return `${hours} hour${hours === 1 ? "" : "s"} ago`;
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
