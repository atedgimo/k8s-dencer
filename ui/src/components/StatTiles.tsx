import { GraphStats, formatBytes, formatCPU } from "../api";
import { ImpactLegend } from "./Impact";

/**
 * Supporting figures.
 *
 * These sit *under* the capacity ribbon and stay deliberately small. The
 * ribbon is the argument; these are the receipts. Promoting one of them to a
 * display-size hero would put a conclusion where the evidence should be.
 *
 * Figures are Archivo with tabular numerals, because this is a row of numbers
 * that should align — the rule against tabular figures applies to large
 * standalone values, which none of these are any more.
 */
export default function StatTiles({ stats }: { stats: GraphStats }) {
  return (
    <section className="figures" aria-label="Plan summary">
      <Figure label="CPU freed" value={formatCPU(stats.cpuReclaimedMilli)} unit="cores" />
      <Figure label="Memory freed" value={formatBytes(stats.memoryReclaimedBytes)} />
      <Figure label="Pods moved" value={String(stats.podsMoved)} unit={`in ${stats.steps} steps`} />
      <div className="figure figure-legend">
        <span className="figure-label">Steps by impact</span>
        <ImpactLegend counts={stats.ratings} />
      </div>
    </section>
  );
}

function Figure({ label, value, unit }: { label: string; value: string; unit?: string }) {
  return (
    <div className="figure">
      <span className="figure-label">{label}</span>
      <span className="figure-value">
        {value}
        {unit && <span className="figure-unit">{unit}</span>}
      </span>
    </div>
  );
}
