import { useMemo } from "react";
import { GraphPayload, PlanStep } from "../api";
import {
  NODE_GAP_X,
  NODE_GAP_Y,
  POD_SIZE,
  Model,
  computeLayout,
  gridColumns,
  peakOccupancy,
  placementAtStep,
  toModel,
} from "../layout";

/**
 * The packing field — nodes as containers, pods as blocks that move between
 * them.
 *
 * Replaces the node-link graph this used to be. Consolidation is a *packing*
 * question — "which pods sit in which box, and how full is each box" — and at
 * thirty nodes a force-directed graph is a hairball that answers none of it.
 * Blocks sliding out of a box that then dims and collapses answers all of it
 * without a legend.
 *
 * Fullness is brightness, never hue: colour on this page always means risk.
 *
 * Positions come from layout.ts, which computes them deterministically for any
 * step. Animation is therefore free — the same element simply gets a new
 * transform, and the browser interpolates. Nothing here animates a layout
 * property, because ~300 blocks reflowing at once would drop frames on a
 * laptop, which is exactly the machine this runs on.
 */

interface Props {
  graph: GraphPayload;
  steps: PlanStep[];
  /** Steps 1..step are applied. 0 is the cluster as it stands. */
  step: number;
  selectedStep: number | null;
  onSelectNode: (name: string | null) => void;
  onSelectPod: (key: string | null) => void;
  selectedNode: string | null;
  selectedPod: string | null;
}

export default function PackingField({
  graph,
  steps,
  step,
  selectedStep,
  onSelectNode,
  onSelectPod,
  selectedNode,
  selectedPod,
}: Props) {
  const model: Model = useMemo(() => toModel(graph), [graph]);
  const peak = useMemo(() => peakOccupancy(model, steps), [model, steps]);
  const columns = useMemo(() => gridColumns(model.nodes.length), [model.nodes.length]);

  const placement = useMemo(
    () => placementAtStep(model, steps, step),
    [model, steps, step],
  );
  const layout = useMemo(
    () => computeLayout(model, placement, peak, columns),
    [model, placement, peak, columns],
  );

  // Nodes this plan drains at or before the current step. They stay on the
  // field rather than vanishing: an operator wants to see what was reclaimed,
  // and a box that disappeared would just look like a rendering bug.
  const reclaimed = useMemo(() => {
    const out = new Set<string>();
    for (const s of steps) {
      if (s.sequenceNumber <= step && s.targetNode) out.add(s.targetNode);
    }
    return out;
  }, [steps, step]);

  // Which node each step drains, so hovering a step can light up its target.
  const stepTarget = useMemo(() => {
    const s = steps.find((x) => x.sequenceNumber === selectedStep);
    return s?.targetNode ?? null;
  }, [steps, selectedStep]);

  // Page-load reveal, ordered fullest-first so the very first thing the eye
  // follows is the ordering that drives the whole plan. The stagger itself is
  // a CSS animation keyed off --delay; this only computes the ordering.
  const revealOrder = useMemo(() => {
    const order = new Map<string, number>();
    [...model.nodes]
      .sort((a, b) => (layout.nodeFill.get(b.name) ?? 0) - (layout.nodeFill.get(a.name) ?? 0))
      .forEach((n, i) => order.set(n.name, i));
    return order;
    // Deliberately keyed to the model alone: the reveal happens once, and
    // re-ordering it on every scrub would be motion that means nothing.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [model]);

  const width = columns * (layout.nodeWidth + NODE_GAP_X);
  const rows = Math.ceil(model.nodes.length / columns);
  const height = rows * (layout.nodeHeight + NODE_GAP_Y);

  const podsByNode = useMemo(() => {
    const out = new Map<string, number>();
    for (const pod of model.pods) {
      const host = placement.get(pod.key) ?? pod.homeNode;
      out.set(host, (out.get(host) ?? 0) + 1);
    }
    return out;
  }, [model, placement]);

  return (
    <div className="field-wrap">
      <div
        className="field"
        style={{ width, height }}
        onClick={() => {
          onSelectNode(null);
          onSelectPod(null);
        }}
      >
        {model.nodes.map((node) => {
          const pos = layout.nodes.get(node.id);
          if (!pos) return null;
          const fill = layout.nodeFill.get(node.name) ?? 0;
          const gone = reclaimed.has(node.name);
          const count = podsByNode.get(node.name) ?? 0;

          return (
            <div
              key={node.id}
              className={[
                "box",
                gone ? "box-reclaimed" : "",
                selectedNode === node.name ? "box-selected" : "",
                stepTarget === node.name ? "box-targeted" : "",
              ]
                .filter(Boolean)
                .join(" ")}
              style={
                {
                  transform: `translate(${pos.x}px, ${pos.y}px)`,
                  width: layout.nodeWidth,
                  height: layout.nodeHeight,
                  "--delay": `${(revealOrder.get(node.name) ?? 0) * 14}ms`,
                } as React.CSSProperties
              }
              onClick={(e) => {
                e.stopPropagation();
                onSelectNode(selectedNode === node.name ? null : node.name);
              }}
              role="button"
              tabIndex={0}
              onKeyDown={(e) => {
                if (e.key === "Enter" || e.key === " ") {
                  e.preventDefault();
                  onSelectNode(selectedNode === node.name ? null : node.name);
                }
              }}
              aria-label={`${node.name}, ${Math.round(fill * 100)} percent requested, ${count} pods${
                gone ? ", reclaimed" : ""
              }`}
            >
              <div className="box-head">
                <span className="box-name mono">{shortName(node.name)}</span>
                <span className="box-pct num">{gone ? "—" : `${Math.round(fill * 100)}`}</span>
              </div>
              {/* The fill bar is the node's own gauge: it drains visibly as
                  pods leave, which is the moment the whole view exists for. */}
              <div className="box-gauge" aria-hidden="true">
                <div
                  className="box-gauge-fill"
                  style={{ width: `${Math.min(100, fill * 100)}%`, background: rampColor(fill) }}
                />
              </div>
            </div>
          );
        })}

        {model.pods.map((pod) => {
          const pos = layout.pods.get(pod.id);
          if (!pos) return null;
          const host = placement.get(pod.key) ?? pod.homeNode;
          const leaving = reclaimed.has(host);

          return (
            <div
              key={pod.id}
              className={[
                "blk",
                pod.blocked ? "blk-blocked" : "",
                !pod.movable ? "blk-pinned" : "",
                selectedPod === pod.key ? "blk-selected" : "",
                leaving ? "blk-orphan" : "",
              ]
                .filter(Boolean)
                .join(" ")}
              style={{
                transform: `translate(${pos.x}px, ${pos.y}px)`,
                width: POD_SIZE,
                height: POD_SIZE,
              }}
              onClick={(e) => {
                e.stopPropagation();
                onSelectPod(selectedPod === pod.key ? null : pod.key);
              }}
              role="button"
              tabIndex={-1}
              title={`${pod.key}${pod.blocked ? " — blocked" : ""}${
                !pod.movable ? " — pinned to this node" : ""
              }`}
            />
          );
        })}
      </div>

      <ReclaimedTray count={reclaimed.size} total={model.nodes.length} />
    </div>
  );
}

/**
 * The running tally of what the plan has emptied.
 *
 * Says "drained", not "reclaimed". k8s-dencer cordons and empties a node; it
 * never deletes one. Removing the machine means calling a cloud provider, and
 * deleting the Node object alone achieves nothing — the kubelet re-registers
 * seconds later. So the honest end state is "empty and cordoned, ready for
 * your autoscaler or your node-pool tooling to take away", and the label has
 * to say that rather than implying the capacity is already gone.
 */
function ReclaimedTray({ count, total }: { count: number; total: number }) {
  return (
    <div className="tray" aria-live="polite">
      <span className="tray-label mono">drained</span>
      <span className="tray-count num">{count}</span>
      <span className="tray-of mono">of {total}</span>
      {count > 0 && <span className="tray-hint">empty and cordoned — ready to remove</span>}
      <div className="tray-marks" aria-hidden="true">
        {Array.from({ length: total }, (_, i) => (
          <span key={i} className={i < count ? "tray-mark tray-mark-on" : "tray-mark"} />
        ))}
      </div>
    </div>
  );
}

/**
 * Maps utilisation onto the neutral brightness ramp.
 *
 * Stepped rather than continuous: five discrete levels are easier to compare
 * across a field of thirty boxes than a smooth gradient, where everything
 * mid-range looks alike.
 */
function rampColor(fill: number): string {
  if (fill >= 0.85) return "var(--fill-95)";
  if (fill >= 0.6) return "var(--fill-75)";
  if (fill >= 0.35) return "var(--fill-50)";
  if (fill >= 0.12) return "var(--fill-25)";
  return "var(--fill-05)";
}

/** Node names are long and repetitive; the distinguishing tail is what matters. */
function shortName(name: string): string {
  const m = /(\d+)$/.exec(name);
  return m ? m[1].padStart(2, "0") : name.slice(-6);
}
