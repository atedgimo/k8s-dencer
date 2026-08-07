/**
 * The Cluster destination (assets/design/README.md, 3b/3d/3c): one screen,
 * three lenses. Rack answers "what is on each node", Wells answers "where
 * does displaced work land and does it fit", Load answers "where is the gap
 * between requested and used". None is redundant, which is why the old
 * four-equal-views switcher became lenses on one destination.
 *
 * Selection is shared with Review: selecting a node selects its step, and
 * vice versa.
 */

import { useMemo, useState } from "react";
import { GraphPayload, Impact, PlanStep, formatCPU } from "../../api";
import { FieldView, VIEW_LABELS } from "../../view";
import { VERDICT } from "../Impact";
import type { ObservedNode } from "../../useObserved";

interface Props {
  graph: GraphPayload;
  steps: PlanStep[];
  lens: FieldView;
  onLens: (v: FieldView) => void;
  selectedStep: number | null;
  onSelectStep: (seq: number | null) => void;
  observed: Map<string, ObservedNode>;
  /** Pods the current run genuinely evicted: drawn as ghosts, because their
   *  old position is a lie waiting to happen until the next snapshot. */
  evictedPods: Set<string>;
}

interface NodeModel {
  name: string;
  pool?: string;
  alloc: number;
  req: number;
  used?: number;
  memUsed?: number;
  memAlloc: number;
  pods: number;
  drainStep?: number;
  impact?: Impact;
  /** CPU arriving from the plan's moves, milli. */
  incoming: number;
  cordoned?: boolean;
  notReady?: boolean;
  awaiting?: boolean;
  gone?: boolean;
}

/**
 * Cloud node names share a long prefix and differ only at the end:
 * gke-<cluster>-<pool>-<hash>-0xk4. Truncating from the right — which is what
 * text-overflow does — throws away the only part that identifies the node, so
 * a real GKE fleet rendered as four cards all reading "gke-dencer-play-de…".
 *
 * Eliding the shared prefix instead adapts to whatever a cluster's convention
 * happens to be, and does nothing at all when names have nothing in common.
 */
function shortener(names: string[]): (name: string) => string {
  if (names.length < 2) return (n) => n;
  let prefix = names[0];
  for (const n of names) {
    let i = 0;
    while (i < prefix.length && i < n.length && prefix[i] === n[i]) i++;
    prefix = prefix.slice(0, i);
  }
  // Cut back to a separator so the remainder starts at a meaningful boundary,
  // and only bother when enough is shared to be worth hiding.
  const cut = Math.max(prefix.lastIndexOf("-"), prefix.lastIndexOf("."));
  if (cut < 8) return (n) => n;
  return (n) => (n.length > cut + 1 ? "…" + n.slice(cut) : n);
}

function buildNodes(graph: GraphPayload, steps: PlanStep[], observed: Map<string, ObservedNode>): NodeModel[] {
  const byStep = new Map(steps.map((s) => [s.sequenceNumber, s]));
  const incoming = new Map<string, number>();
  const podReq = new Map<string, number>();
  for (const e of graph.elements) {
    if (e.data.kind === "pod") podReq.set(e.data.id.replace(/^pod:/, ""), e.data.cpuRequest ?? 0);
  }
  for (const s of steps) {
    for (const m of s.moves) {
      const cpu = m.cpuMilli ?? podReq.get(`${m.namespace}/${m.pod}`) ?? 0;
      incoming.set(m.toNode, (incoming.get(m.toNode) ?? 0) + cpu);
    }
  }

  return graph.elements
    .filter((e) => e.data.kind === "node")
    .map((e) => {
      const d = e.data;
      const obs = observed.get(d.label);
      return {
        name: d.label,
        pool: d.instanceType || d.capacityType,
        alloc: d.cpuAllocatable ?? 0,
        req: d.cpuRequested ?? 0,
        used: d.cpuUsed || undefined,
        memUsed: d.memUsed || undefined,
        memAlloc: d.memAllocatable ?? 0,
        pods: d.podCount ?? 0,
        drainStep: d.drainStep || undefined,
        impact: d.drainStep ? byStep.get(d.drainStep)?.impact : undefined,
        incoming: incoming.get(d.label) ?? 0,
        cordoned: d.cordoned || obs?.cordoned,
        notReady: d.ready === false || obs?.notReady,
        awaiting: obs?.reclaim === "awaiting",
        gone: obs?.reclaim === "reclaimed",
      };
    })
    .filter((n) => !n.gone);
}

const pct = (part: number, whole: number) => (whole > 0 ? Math.round((part / whole) * 100) : 0);

export default function ClusterPage({
  graph,
  steps,
  lens,
  onLens,
  selectedStep,
  onSelectStep,
  observed,
  evictedPods,
}: Props) {
  const nodes = useMemo(() => buildNodes(graph, steps, observed), [graph, steps, observed]);
  const pools = new Set(nodes.map((n) => n.pool).filter(Boolean)).size;
  const pods = nodes.reduce((n, x) => n + x.pods, 0);
  const short = useMemo(() => shortener(nodes.map((n) => n.name)), [nodes]);

  return (
    <div className="clusterpage">
      <div className="clusterpage-head">
        <span className="clusterpage-title">Cluster</span>
        <span className="clusterpage-counts mono">
          {nodes.length} nodes · {pods} pods · {pools} pool{pools === 1 ? "" : "s"}
        </span>
        <div className="viewswitch clusterpage-lenses" role="group" aria-label="Cluster lens">
          {(Object.keys(VIEW_LABELS) as FieldView[]).map((v) => (
            <button
              key={v}
              type="button"
              className={"viewswitch-btn" + (v === lens ? " is-on" : "")}
              aria-pressed={v === lens}
              onClick={() => onLens(v)}
            >
              {VIEW_LABELS[v]}
            </button>
          ))}
        </div>
      </div>

      {lens === "rack" && (
        <RackLens
          nodes={nodes}
          steps={steps}
          graph={graph}
          selectedStep={selectedStep}
          onSelectStep={onSelectStep}
          evictedPods={evictedPods}
          short={short}
        />
      )}
      {lens === "wells" && (
        <WellsLens nodes={nodes} steps={steps} ceiling={graph.stats.packCeiling} onSelectStep={onSelectStep} short={short} />
      )}
      {lens === "load" && <LoadLens nodes={nodes} short={short} />}
    </div>
  );
}

/* ------------------------------------------------------------------ rack */

function intentOf(impact?: Impact): { cls: string; label: string } {
  switch (impact) {
    case "Green":
      return { cls: "safe", label: "Safe to drain" };
    case "Yellow":
      return { cls: "caution", label: VERDICT.Yellow };
    case "Red":
      return { cls: "held", label: VERDICT.Red };
    default:
      return { cls: "stays", label: "Stays" };
  }
}

function RackLens({
  nodes,
  steps,
  graph,
  selectedStep,
  onSelectStep,
  evictedPods,
  short,
}: {
  nodes: NodeModel[];
  steps: PlanStep[];
  graph: GraphPayload;
  selectedStep: number | null;
  onSelectStep: (seq: number | null) => void;
  evictedPods: Set<string>;
  short: (n: string) => string;
}) {
  const moved = new Map<string, Impact>();
  for (const s of steps) for (const m of s.moves) moved.set(`${m.namespace}/${m.pod}`, s.impact);
  const podsByNode = new Map<string, Array<{ id: string; cpu: number; impact?: Impact }>>();
  for (const e of graph.elements) {
    if (e.data.kind !== "pod" || !e.data.parent) continue;
    const list = podsByNode.get(e.data.parent) ?? [];
    const key = e.data.id.replace(/^pod:/, "");
    list.push({ id: key, cpu: e.data.cpuRequest ?? 0, impact: moved.get(key) });
    podsByNode.set(e.data.parent, list);
  }

  const safeCount = steps.filter((s) => s.impact === "Green").length;
  const focused = nodes.find((n) => n.drainStep != null && n.drainStep === selectedStep);
  const focusedStep = steps.find((s) => s.sequenceNumber === selectedStep);

  // A node with no plan step used to select nothing and say nothing, which on
  // a cluster with no plan meant the pane was inert for the whole session. But
  // "what is running here" is answerable about every node, always.
  const [pickedNode, setPickedNode] = useState<string | null>(null);
  const picked = nodes.find((n) => n.name === pickedNode) ?? focused;
  const pickedPods = picked ? (podsByNode.get(`node:${picked.name}`) ?? []) : [];
  const selectNode = (n: NodeModel) => {
    setPickedNode(n.name);
    onSelectStep(n.drainStep ?? null);
  };

  return (
    <>
      <div className="clusterpage-hero">
        <div className="clusterpage-hero-lead">
          <span className="eyebrow mono">The cluster as it stands, against the plan</span>
          <h2 className="clusterpage-headline">
            {safeCount > 0
              ? safeCount === 1
                ? "1 node is safe to drain today"
                : `${safeCount} nodes are safe to drain today`
              : "Nothing is safe to drain today"}
          </h2>
        </div>
        <span className="clusterpage-hint mono">pod width = CPU request · bar = utilisation</span>
      </div>

      <div className="clusterpage-body">
        <div className="rackgrid">
          {nodes.map((n) => {
            const intent = intentOf(n.impact);
            const pods = podsByNode.get(`node:${n.name}`) ?? [];
            return (
              <button
                type="button"
                key={n.name}
                className={
                  "racknode racknode-" + intent.cls + (picked?.name === n.name ? " is-on" : "")
                }
                aria-pressed={picked?.name === n.name}
                onClick={() => selectNode(n)}
              >
                <div className="racknode-head">
                  <span className={"racknode-dot racknode-dot-" + intent.cls} aria-hidden="true" />
                  <span className="racknode-name mono" title={n.name}>
                    {short(n.name)}
                  </span>
                  <span className="racknode-util mono">{pct(n.req, n.alloc)}%</span>
                </div>
                <div className="racknode-pods" aria-hidden="true">
                  {pods.length === 0 && <div className="rackpod rackpod-empty" style={{ flex: 1 }} />}
                  {pods.map((p) => (
                    <div
                      key={p.id}
                      className={
                        "rackpod rackpod-" +
                        intentOf(p.impact).cls +
                        (evictedPods.has(p.id) ? " is-evicted" : "")
                      }
                      style={{ flex: Math.max(p.cpu, 50) }}
                      title={p.id}
                    />
                  ))}
                </div>
                <div className="racknode-foot">
                  {/* Observed facts outrank the plan's predictions: a real
                      cordon, a NotReady kubelet or a drained node awaiting
                      removal must be visible, or the screen narrates a
                      cluster that no longer exists. */}
                  <span className="racknode-count mono">
                    {n.pods} pod{n.pods === 1 ? "" : "s"}
                    {n.awaiting ? " · awaiting removal" : n.cordoned ? " · cordoned" : ""}
                    {n.notReady ? " · NotReady" : ""}
                  </span>
                  <span className={"racknode-label racknode-label-" + intent.cls}>{intent.label}</span>
                </div>
              </button>
            );
          })}
        </div>

        <aside className="clusterdetail">
          {focused && focusedStep ? (
            <>
              <div className="clusterdetail-head">
                <span className={"steprow-verdict steprow-verdict-" + focusedStep.impact.toLowerCase()}>
                  {VERDICT[focusedStep.impact]} · step {String(focusedStep.sequenceNumber).padStart(2, "0")}
                </span>
                <span className="clusterdetail-name mono">{focused.name}</span>
                <p className="clusterdetail-why">{focusedStep.rationale}</p>
              </div>
              <div className="clusterdetail-body">
                <span className="eyebrow mono">Where its pods go</span>
                {focusedStep.moves.map((m) => (
                  <div key={m.namespace + m.pod} className="move">
                    <span className="move-pod mono">{m.pod}</span>
                    <span className="move-arrow" aria-hidden="true">
                      →
                    </span>
                    <span className="move-to mono">{m.toNode}</span>
                  </div>
                ))}
              </div>
            </>
          ) : picked ? (
            <>
              <div className="clusterdetail-head">
                <span className="eyebrow mono">
                  {picked.drainStep == null ? "Stays in this plan" : "On this node"}
                </span>
                <span className="clusterdetail-name mono">{picked.name}</span>
                <p className="clusterdetail-why">
                  {formatCPU(picked.req)} of {formatCPU(picked.alloc)} requested by{" "}
                  {picked.pods} pod{picked.pods === 1 ? "" : "s"}
                  {picked.cordoned ? " · cordoned" : ""}
                  {picked.notReady ? " · NotReady" : ""}.
                </p>
              </div>
              <div className="clusterdetail-body">
                <span className="eyebrow mono">What is running here</span>
                <ul className="podlist">
                  {pickedPods.length === 0 && (
                    <li className="podlist-empty">Nothing but the node's own daemons.</li>
                  )}
                  {[...pickedPods]
                    .sort((a, b) => b.cpu - a.cpu)
                    .map((p) => (
                      <li key={p.id} className="podlist-row">
                        <span className={"podlist-dot podlist-dot-" + intentOf(p.impact).cls} aria-hidden="true" />
                        <span className="podlist-name mono">{p.id}</span>
                        <span className="podlist-cpu mono">{formatCPU(p.cpu)}</span>
                      </li>
                    ))}
                </ul>
              </div>
            </>
          ) : (
            <p className="clusterdetail-empty">
              Select a node to see what is running on it. Selection is shared with Review, so a
              node with a step selects that step too.
            </p>
          )}
        </aside>
      </div>
    </>
  );
}

/* ----------------------------------------------------------------- wells */

function WellsLens({
  nodes,
  steps,
  ceiling,
  onSelectStep,
  short,
}: {
  nodes: NodeModel[];
  steps: PlanStep[];
  ceiling?: number;
  onSelectStep: (seq: number | null) => void;
  short: (n: string) => string;
}) {
  const ceilPct = ceiling && ceiling > 0 && ceiling < 1 ? Math.round(ceiling * 100) : null;

  type WellState = "receiving" | "room" | "tight" | "emptying" | "blocked";
  const stateOf = (n: NodeModel): WellState => {
    if (n.drainStep != null) return n.impact === "Red" ? "blocked" : "emptying";
    if (n.incoming > 0) return "receiving";
    const total = pct(n.req + n.incoming, n.alloc);
    if (ceilPct != null && total >= ceilPct - 5) return "tight";
    return "room";
  };
  const TAG: Record<WellState, string> = {
    receiving: "receiving",
    room: "has room",
    tight: "near limit",
    emptying: "emptying",
    blocked: "held back",
  };

  const receiving = nodes.filter((n) => n.incoming > 0);
  const pours = steps.flatMap((s) =>
    s.moves.map((m) => ({
      pod: m.pod,
      from: m.fromNode,
      to: m.toNode,
      cost: m.cpuMilli ?? 0,
    })),
  );
  const worst = receiving.reduce(
    (acc, n) => Math.max(acc, pct(n.req + n.incoming, n.alloc)),
    0,
  );
  const crossings = receiving.filter(
    (n) => ceilPct != null && pct(n.req + n.incoming, n.alloc) > ceilPct,
  ).length;

  return (
    <>
      <div className="clusterpage-hero">
        <div className="clusterpage-hero-lead">
          <span className="eyebrow mono">Allocatable, and what the plan pours in</span>
          <h2 className="clusterpage-headline">
            {pours.length} pod{pours.length === 1 ? "" : "s"} land on {receiving.length} node
            {receiving.length === 1 ? "" : "s"}.
            {ceilPct != null &&
              (crossings === 0 ? ` None crosses ${ceilPct}%.` : ` ${crossings} would cross ${ceilPct}%.`)}
          </h2>
        </div>
        <div className="wellslegend">
          <span className="wellslegend-item">
            <span className="wellslegend-req" aria-hidden="true" />
            already requested
          </span>
          <span className="wellslegend-item">
            <span className="wellslegend-inc" aria-hidden="true" />
            arriving from the plan
          </span>
          {ceilPct != null && (
            <span className="wellslegend-item">
              <span className="wellslegend-ceil" aria-hidden="true" />
              packing ceiling, {ceilPct}%
            </span>
          )}
        </div>
      </div>

      <div className="clusterpage-body">
        <div className="wellsgrid-wrap">
          <div className="wellsgrid">
            {nodes.map((n) => {
              const st = stateOf(n);
              const reqPct = pct(n.req, n.alloc);
              const incPct = pct(n.incoming, n.alloc);
              return (
                <button
                  type="button"
                  key={n.name}
                  className={"well well-" + st}
                  onClick={() => onSelectStep(n.drainStep ?? null)}
                >
                  <div className="well-vessel" aria-hidden="true">
                    {ceilPct != null && (
                      <div className="well-ceiling" style={{ top: `${100 - ceilPct}%` }} />
                    )}
                    <div className="well-inc" style={{ height: `${incPct}%` }} />
                    <div className={"well-req well-req-" + st} style={{ height: `${reqPct}%` }} />
                  </div>
                  <span className="well-name mono" title={n.name}>{short(n.name)}</span>
                  <div className="well-line">
                    <span className="well-total mono">{reqPct + incPct}%</span>
                    <span className={"well-tag well-tag-" + st}>{TAG[st]}</span>
                  </div>
                </button>
              );
            })}
          </div>
        </div>

        <aside className="clusterdetail clusterdetail-wells">
          <div className="clusterdetail-head">
            <span className="eyebrow mono">Every pour in this plan</span>
            <p className="clusterdetail-why">
              Each pour is a request added to a node that has to hold it — not an average.
            </p>
          </div>
          <div className="clusterdetail-body">
            {pours.map((p) => (
              <div key={p.pod + p.to} className="move">
                <span className="move-pod mono" title={`${p.from} → ${p.to}`}>
                  {p.pod}
                </span>
                <span className="move-arrow" aria-hidden="true">
                  →
                </span>
                <span className="move-to mono">{p.to}</span>
                {p.cost > 0 && <span className="move-flag mono">+{formatCPU(p.cost)}</span>}
              </div>
            ))}
            {receiving.length > 0 && (
              <div className="wells-worst">
                <span className="eyebrow mono">Worst case after the pour</span>
                <span className="wells-worst-value mono">
                  {worst}%{ceilPct != null && ` of a ${ceilPct}% ceiling`}
                </span>
              </div>
            )}
            {pours.length === 0 && (
              <p className="clusterdetail-empty">The plan moves nothing right now.</p>
            )}
          </div>
        </aside>
      </div>
    </>
  );
}

/* ------------------------------------------------------------------ load */

function LoadLens({ nodes, short }: { nodes: NodeModel[]; short: (n: string) => string }) {
  const measured = nodes.filter((n) => n.used != null);
  if (measured.length === 0) {
    return (
      <div className="loadempty">
        <p>
          No measured usage. Enable it with <span className="mono">planner.usageSource=metrics-server</span>;
          without measurements this lens refuses to draw a guess.
        </p>
      </div>
    );
  }

  const rows = [...measured].sort(
    (a, b) => pct(b.req - (b.used ?? 0), b.alloc) - pct(a.req - (a.used ?? 0), a.alloc),
  );
  const totReq = measured.reduce((n, x) => n + x.req, 0);
  const totUsed = measured.reduce((n, x) => n + (x.used ?? 0), 0);
  const ratio = totUsed > 0 ? (totReq / totUsed).toFixed(1) : null;

  return (
    <>
      <div className="clusterpage-hero">
        <div className="clusterpage-hero-lead">
          <span className="eyebrow mono">Requested vs actually used</span>
          <h2 className="clusterpage-headline">
            {ratio != null
              ? `Pods reserve ${ratio}× the CPU they use`
              : "Usage is being measured"}
          </h2>
        </div>
        <div className="wellslegend">
          <span className="wellslegend-item">
            <span className="wellslegend-inc" aria-hidden="true" />
            used
          </span>
          <span className="wellslegend-item">
            <span className="wellslegend-gap" aria-hidden="true" />
            reserved but idle
          </span>
        </div>
      </div>

      <div className="loadtable">
        <div className="loadrow loadrow-head">
          <span className="eyebrow mono">node</span>
          <span className="eyebrow mono">pool</span>
          <span className="eyebrow mono">used vs reserved</span>
          <span className="eyebrow mono">used</span>
          <span className="eyebrow mono">req</span>
          <span className="eyebrow mono">mem</span>
          <span className="eyebrow mono">pods</span>
          <span className="eyebrow mono">plan</span>
        </div>
        {rows.map((n) => {
          const used = pct(n.used ?? 0, n.alloc);
          const req = pct(n.req, n.alloc);
          const intent = intentOf(n.impact);
          return (
            <div key={n.name} className="loadrow">
              <span className="loadrow-name mono" title={n.name}>{short(n.name)}</span>
              <span className="loadrow-pool">{n.pool}</span>
              <div className="loadbar" aria-hidden="true">
                <div className="loadbar-used" style={{ width: `${used}%` }} />
                <div className="loadbar-gap" style={{ width: `${Math.max(0, req - used)}%` }} />
              </div>
              <span className="loadrow-num mono">{used}%</span>
              <span className="loadrow-num loadrow-req mono">{req}%</span>
              <span className="loadrow-num mono">{pct(n.memUsed ?? 0, n.memAlloc)}%</span>
              <span className="loadrow-num mono">{n.pods}</span>
              <span className={"racknode-label racknode-label-" + intent.cls}>{intent.label}</span>
            </div>
          );
        })}
      </div>
    </>
  );
}
