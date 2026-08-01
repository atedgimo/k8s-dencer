/**
 * The Recommendations destination (assets/design/README.md, 2d): the
 * blocking-rule cards as a queue ranked by nodes unlocked, with a detail
 * pane carrying the fix.
 *
 * The rank is the product's argument: 29 findings are a list nobody works
 * through; "this one unblocks 3 steps" is a work queue. Findings that block
 * nothing are still here — under the All filter — but they never outrank
 * work. Muting removes a finding from the queue, never from the plan.
 */

import { useMemo, useState } from "react";
import { GraphPayload, PlanStep, Recommendation, formatCPU } from "../../api";
import { findingKey } from "../../useMuted";

interface Props {
  recs: Recommendation[] | null;
  steps: PlanStep[];
  graph: GraphPayload;
  muted: Set<string>;
  onMute: (key: string) => void;
  onUnmute: (key: string) => void;
  /** Cross-navigation: focus these steps on the Review screen. */
  onOpenSteps: (steps: number[]) => void;
}

const SEV_LABEL: Record<Recommendation["severity"], string> = {
  high: "HIGH",
  medium: "MED",
  info: "INFO",
};

export default function RecsPage({ recs, steps, graph, muted, onMute, onUnmute, onOpenSteps }: Props) {
  const [showAll, setShowAll] = useState(false);
  const [selected, setSelected] = useState<string | null>(null);

  const all = recs ?? [];
  const visible = useMemo(() => {
    const unmuted = all.filter((r) => !muted.has(findingKey(r.kind, r.workload)));
    const pool = showAll ? all : unmuted.filter((r) => (r.unblocksSteps?.length ?? 0) > 0);
    return pool;
  }, [all, muted, showAll]);

  const highCount = all.filter((r) => r.severity === "high").length;
  const blockingRules = all.filter(
    (r) => (r.unblocksSteps?.length ?? 0) > 0 && !muted.has(findingKey(r.kind, r.workload)),
  );
  const blockedSeqs = new Set(blockingRules.flatMap((r) => r.unblocksSteps ?? []));
  const safeNow = steps.filter((s) => s.impact === "Green").length;
  const topThree = blockingRules
    .slice(0, 3)
    .reduce((acc, r) => {
      for (const s of r.unblocksSteps ?? []) acc.add(s);
      return acc;
    }, new Set<number>()).size;

  // What the held-back steps are worth: the allocatable of their nodes.
  const nodesByName = new Map(
    graph.elements.filter((e) => e.data.kind === "node").map((e) => [e.data.label, e.data]),
  );
  let strandedCpu = 0;
  for (const s of steps) {
    if (s.impact === "Green" || !s.targetNode) continue;
    strandedCpu += nodesByName.get(s.targetNode)?.cpuAllocatable ?? 0;
  }

  const current =
    visible.find((r) => findingKey(r.kind, r.workload) === selected) ?? visible[0] ?? null;

  return (
    <div className="recspage">
      <div className="recspage-head">
        <span className="recspage-title">Recommendations</span>
        <span className="recspage-counts mono">
          {all.length} finding{all.length === 1 ? "" : "s"} · {highCount} high
        </span>
        <div className="recspage-filters">
          <button
            type="button"
            className={"steplist-filter" + (!showAll ? " is-on" : "")}
            onClick={() => setShowAll(false)}
          >
            Blocking a step
          </button>
          <button
            type="button"
            className={"steplist-filter" + (showAll ? " is-on" : "")}
            onClick={() => setShowAll(true)}
          >
            All findings
          </button>
        </div>
      </div>

      <div className="recspage-hero">
        <div className="recspage-hero-lead">
          <span className="eyebrow mono">Ranked by nodes unlocked</span>
          <h2 className="recspage-headline">
            {blockingRules.length === 0
              ? "No rule is holding a step back"
              : `${blockingRules.length} rule${blockingRules.length === 1 ? "" : "s"} hold${blockingRules.length === 1 ? "s" : ""} back ${blockedSeqs.size} of your ${steps.length} nodes`}
          </h2>
          {topThree > 0 && (
            <p className="recspage-sub">
              Fixing the top {Math.min(3, blockingRules.length) === 1 ? "one" : "three"} would
              take today's safe plan from {safeNow} node{safeNow === 1 ? "" : "s"} to{" "}
              {safeNow + topThree}.
            </p>
          )}
        </div>
        <div className="recspage-hero-stats">
          <div className="hero-stat">
            <span className="hero-stat-figure mono">{blockedSeqs.size} steps</span>
            <span className="hero-stat-label">would unblock</span>
          </div>
          <span className="hero-stat-sep" aria-hidden="true" />
          <div className="hero-stat">
            <span className="hero-stat-figure mono">{formatCPU(strandedCpu)} cores</span>
            <span className="hero-stat-label">still stranded</span>
          </div>
        </div>
      </div>

      <div className="recspage-body">
        <div className="recsqueue">
          <div className="recsqueue-cols">
            <span className="eyebrow mono">Rule</span>
            <span className="eyebrow mono recsqueue-cols-right">Unblocks</span>
          </div>
          <div className="recsqueue-list">
            {visible.length === 0 && (
              <p className="recsqueue-empty">
                {showAll
                  ? "No findings against the current cluster."
                  : "Nothing blocking — every finding is advice. Switch to All findings to read it."}
              </p>
            )}
            {visible.map((r) => {
              const key = findingKey(r.kind, r.workload);
              const n = r.unblocksSteps?.length ?? 0;
              const worth = (r.unblocksSteps ?? []).reduce((acc, seq) => {
                const st = steps.find((s) => s.sequenceNumber === seq);
                return acc + (st?.targetNode ? (nodesByName.get(st.targetNode)?.cpuAllocatable ?? 0) : 0);
              }, 0);
              const isMuted = muted.has(key);
              return (
                <button
                  type="button"
                  key={key}
                  className={
                    "recsrow" +
                    (current && findingKey(current.kind, current.workload) === key
                      ? " is-on"
                      : "") +
                    (isMuted ? " is-muted" : "")
                  }
                  onClick={() => setSelected(key)}
                >
                  <span className={"recsrow-sev recsrow-sev-" + r.severity}>
                    {SEV_LABEL[r.severity]}
                  </span>
                  <span className="recsrow-body">
                    <span className="recsrow-rule mono">
                      {r.kind}/{shortName(r.workload)}
                    </span>
                    <span className="recsrow-why">{r.why}</span>
                  </span>
                  <span className="recsrow-right">
                    <span className="recsrow-unblocks mono">
                      {n > 0 ? `${n} step${n === 1 ? "" : "s"}` : isMuted ? "muted" : "advice"}
                    </span>
                    {worth > 0 && (
                      <span className="recsrow-worth mono">{formatCPU(worth)} cores</span>
                    )}
                  </span>
                </button>
              );
            })}
          </div>
        </div>

        {current && (
          <Detail
            rec={current}
            steps={steps}
            graph={graph}
            muted={muted.has(findingKey(current.kind, current.workload))}
            onMute={() => onMute(findingKey(current.kind, current.workload))}
            onUnmute={() => onUnmute(findingKey(current.kind, current.workload))}
            onOpenSteps={onOpenSteps}
          />
        )}
      </div>
    </div>
  );
}

function Detail({
  rec,
  steps,
  graph,
  muted,
  onMute,
  onUnmute,
  onOpenSteps,
}: {
  rec: Recommendation;
  steps: PlanStep[];
  graph: GraphPayload;
  muted: boolean;
  onMute: () => void;
  onUnmute: () => void;
  onOpenSteps: (steps: number[]) => void;
}) {
  const [copied, setCopied] = useState(false);
  const blocked = rec.unblocksSteps ?? [];
  const blockedNodes = blocked
    .map((seq) => steps.find((s) => s.sequenceNumber === seq)?.targetNode)
    .filter((n): n is string => !!n);

  // The workload's live footprint, from the graph the operator is already
  // looking at: replicas and the nodes they sit on.
  const [ns, kind, name] = rec.workload.split("/");
  const podEls = graph.elements.filter(
    (e) =>
      e.data.kind === "pod" &&
      e.data.namespace === ns &&
      e.data.ownerKind === kind &&
      e.data.ownerName === name,
  );

  return (
    <aside className="recsdetail">
      <div className="recsdetail-head">
        <div className="recsdetail-tags">
          <span className={"recsrow-sev recsrow-sev-" + rec.severity}>
            {SEV_LABEL[rec.severity]}
          </span>
          {blocked.length > 0 && (
            <span className="recsdetail-blocks mono">
              blocks step{blocked.length === 1 ? "" : "s"}{" "}
              {blocked.map((s) => String(s).padStart(2, "0")).join(", ")}
            </span>
          )}
        </div>
        <span className="recsdetail-subject mono">
          {rec.kind}/{rec.workload}
        </span>
        <p className="recsdetail-why">{rec.why}</p>
      </div>

      <div className="recsdetail-body">
        {(podEls.length > 0 || blockedNodes.length > 0) && (
          <div className="recsdetail-section">
            <span className="eyebrow mono">Affected</span>
            {podEls.length > 0 && (
              <div className="recsdetail-card">
                <span className="recsdetail-card-main mono">
                  {kind?.toLowerCase()}/{name}
                </span>
                <span className="recsdetail-card-side mono">
                  {podEls.length} replica{podEls.length === 1 ? "" : "s"}
                </span>
              </div>
            )}
            {blockedNodes.length > 0 && (
              <div className="recsdetail-card">
                <span className="recsdetail-card-main mono">nodes {blockedNodes.join(", ")}</span>
                <span className="recsdetail-card-side mono">
                  {blockedNodes.length} node{blockedNodes.length === 1 ? "" : "s"}
                </span>
              </div>
            )}
          </div>
        )}

        {rec.fix && (
          <div className="recsdetail-section recsdetail-fix">
            <div className="recsdetail-fix-head">
              <span className="eyebrow mono">Suggested change</span>
              <button
                type="button"
                className="trailpane-copy"
                onClick={() => {
                  void navigator.clipboard.writeText(rec.fix as string);
                  setCopied(true);
                  setTimeout(() => setCopied(false), 1500);
                }}
              >
                {copied ? "Copied" : "Copy patch"}
              </button>
            </div>
            <pre className="recsdetail-yaml mono">{rec.fix}</pre>
            <span className="recsdetail-fix-note">
              k8s-dencer does not apply this — it is your change to make, in your repo.
            </span>
          </div>
        )}
      </div>

      <div className="recsdetail-foot">
        <div className="recsdetail-buttons">
          {blocked.length > 0 && (
            <button
              type="button"
              className="btn stepdetail-add"
              onClick={() => onOpenSteps(blocked)}
            >
              Open the {blocked.length} blocked step{blocked.length === 1 ? "" : "s"}
            </button>
          )}
          <button type="button" className="btn" onClick={muted ? onUnmute : onMute}>
            {muted ? "Unmute" : "Mute"}
          </button>
        </div>
        <span className="recsdetail-note">
          Muting keeps the finding out of the queue but not out of the plan.
        </span>
      </div>
    </aside>
  );
}

/** ns/Kind/name → name, the part an operator scans for. */
function shortName(workload: string): string {
  const parts = workload.split("/");
  return parts[parts.length - 1] ?? workload;
}
