import { useState } from "react";
import { Recommendation } from "../api";

/**
 * The fix list — what is missing: the PDB nobody wrote, the second replica,
 * the requests — each with its why and, where the fix is YAML, the YAML on a
 * copy button.
 *
 * A destination of its own since the redesign gave it a rail entry; the data
 * arrives from useRecommendations via App so the rail badge and this list
 * can never disagree. Severity is impact-on-consolidation, not risk: words
 * and weight carry it. The full 2d design (cards ranked by nodes unlocked)
 * replaces this rendering; this is the readable interim.
 */
export default function Recommendations({
  recs,
}: {
  recs: Recommendation[] | null;
  /** Reserved for the 2d redesign; the sidebar variant is gone. */
  variant?: "page";
}) {
  const [copied, setCopied] = useState<string | null>(null);

  if (!recs || recs.length === 0) {
    return (
      <section className="recs">
        <span className="eyebrow">recommendations</span>
        <p className="recs-why">Nothing to fix — no findings against the current cluster.</p>
      </section>
    );
  }
  const high = recs.filter((r) => r.severity === "high").length;

  return (
    <section className="recs">
      <span className="eyebrow">recommendations</span>
      <span className="recs-count num">
        {recs.length}
        {high > 0 && <span className="recs-high"> · {high} high</span>}
      </span>
      <ul className="recs-list">
        {recs.map((r) => (
          <li key={r.kind + r.workload} className="recs-item">
            <div className="recs-item-head">
              <span className={"recs-sev recs-sev-" + r.severity}>{r.severity}</span>
              <span className="recs-workload mono">{r.workload}</span>
            </div>
            <p className="recs-why">{r.why}</p>
            {r.fix && (
              <button
                type="button"
                className="recs-copy"
                onClick={() => {
                  void navigator.clipboard.writeText(r.fix as string);
                  setCopied(r.kind + r.workload);
                  setTimeout(() => setCopied(null), 1500);
                }}
              >
                {copied === r.kind + r.workload ? "Copied" : "Copy fix YAML"}
              </button>
            )}
          </li>
        ))}
      </ul>
    </section>
  );
}
