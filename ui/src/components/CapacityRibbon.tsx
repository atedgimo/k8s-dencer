import { useMemo } from "react";
import { GraphPayload, PlanStep } from "../api";

interface Props {
  graph: GraphPayload;
  steps: PlanStep[];
  step: number;
  onSelectNode: (name: string) => void;
}

/**
 * The capacity ribbon — the page's thesis.
 *
 * One cell per node, ordered fullest to emptiest, each cell's fill showing how
 * much of that machine is actually requested. The argument the product exists
 * to make is visible in a single glance: a long tail of barely-used cells, and
 * a shorter block of full ones once the plan has run.
 *
 * This is deliberately not a big number with a small label. "15 nodes
 * reclaimed" is a conclusion; the ribbon is the evidence, and it also does the
 * work a side-by-side before/after pane would do, in one strip, leaving the
 * canvas its full width for detail.
 */
export default function CapacityRibbon({ graph, steps, step, onSelectNode }: Props) {
  const cells = useMemo(() => buildCells(graph, steps, step), [graph, steps, step]);

  const live = cells.filter((c) => !c.drained && c.load > 0).length;
  const idle = cells.filter((c) => !c.drained && c.load === 0).length;
  const reclaimed = cells.filter((c) => c.drained).length;

  return (
    <section className="ribbon" aria-label="Cluster capacity by node">
      <div className="ribbon-lede">
        <p className="ribbon-claim">
          <span className="ribbon-figure">{live}</span>
          <span className="ribbon-claim-text">
            machines carry the workload
            {reclaimed > 0 && (
              <>
                {" "}
                once <strong>{reclaimed}</strong> more are drained
              </>
            )}
          </span>
        </p>
        <p className="ribbon-sub">
          {idle > 0 && `${idle} already empty · `}
          each bar is one node, filled to its requested CPU
        </p>
      </div>

      <ol className="ribbon-track">
        {cells.map((c) => (
          <li key={c.name}>
            <button
              type="button"
              className={`cell${c.drained ? " cell-drained" : ""}${c.load === 0 ? " cell-idle" : ""}`}
              onClick={() => onSelectNode(c.name)}
              title={`${c.name} — ${Math.round(c.fill * 100)}% requested${c.drained ? " · drained by this plan" : ""}`}
              aria-label={`${c.name}, ${Math.round(c.fill * 100)} percent requested${c.drained ? ", drained by this plan" : ""}`}
            >
              <span className="cell-fill" style={{ height: `${Math.max(c.fill * 100, c.load > 0 ? 4 : 0)}%` }} />
            </button>
          </li>
        ))}
      </ol>
    </section>
  );
}

interface Cell {
  name: string;
  fill: number;
  load: number;
  drained: boolean;
}

/**
 * Cells keep a fixed order — fullest first, as the cluster stands today — and
 * never re-sort while scrubbing. A bar that jumped position as it drained
 * would read as motion carrying meaning it does not have; holding position
 * makes the drain itself the only thing moving.
 */
function buildCells(graph: GraphPayload, steps: PlanStep[], step: number): Cell[] {
  const capacity = new Map<string, number>();
  const baseline = new Map<string, number>();
  const drainStep = new Map<string, number>();

  for (const el of graph.elements) {
    const d = el.data;
    if (d.kind !== "node") continue;
    capacity.set(d.label, d.cpuAllocatable ?? 0);
    baseline.set(d.label, 0);
    if (d.drainStep) drainStep.set(d.label, d.drainStep);
  }

  const host = new Map<string, string>();
  const request = new Map<string, number>();
  for (const el of graph.elements) {
    const d = el.data;
    if (d.kind !== "pod" || !d.parent) continue;
    const key = `${d.namespace}/${d.label}`;
    host.set(key, d.parent.replace(/^node:/, ""));
    request.set(key, d.cpuRequest ?? 0);
  }
  for (const s of steps) {
    if (s.sequenceNumber > step) break;
    for (const m of s.moves) host.set(`${m.namespace}/${m.pod}`, m.toNode);
  }

  for (const [key, node] of host) {
    baseline.set(node, (baseline.get(node) ?? 0) + (request.get(key) ?? 0));
  }

  const cells: Cell[] = [];
  for (const [name, cap] of capacity) {
    const load = baseline.get(name) ?? 0;
    const drained = (drainStep.get(name) ?? 0) > 0 && (drainStep.get(name) ?? 0) <= step;
    cells.push({ name, load, fill: cap > 0 ? Math.min(load / cap, 1) : 0, drained });
  }

  // Ordered once, by how full each node is *today*, then held.
  const initial = new Map<string, number>();
  for (const el of graph.elements) {
    if (el.data.kind !== "node") continue;
    const cap = el.data.cpuAllocatable ?? 0;
    initial.set(el.data.label, cap > 0 ? (el.data.cpuRequested ?? 0) / cap : 0);
  }
  cells.sort((a, b) => {
    const d = (initial.get(b.name) ?? 0) - (initial.get(a.name) ?? 0);
    return d !== 0 ? d : a.name.localeCompare(b.name, undefined, { numeric: true });
  });
  return cells;
}
