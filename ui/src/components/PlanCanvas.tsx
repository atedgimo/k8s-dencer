import { useEffect, useMemo, useRef } from "react";
import cytoscape, { Core, ElementDefinition } from "cytoscape";
import { GraphPayload, PlanStep } from "../api";
import {
  Model,
  NODE_HEADER,
  POD_SIZE,
  computeLayout,
  gridColumns,
  peakOccupancy,
  placementAtStep,
  toModel,
} from "../layout";

interface Props {
  graph: GraphPayload;
  steps: PlanStep[];
  /** 0 shows the cluster as it is now; N shows it after step N. */
  step: number;
  selectedStep: number | null;
  onSelectStep: (seq: number | null) => void;
  onSelectPod: (podKey: string | null) => void;
  onSelectNode: (name: string | null) => void;
}

const css = (name: string) =>
  getComputedStyle(document.documentElement).getPropertyValue(name).trim();

/**
 * The packing canvas.
 *
 * Rather than two static before/after panes, one canvas morphs between them
 * under the scrubber. That shows the same comparison plus every intermediate
 * state, and makes the causal story — this pod leaves, so this node empties —
 * visible instead of inferred.
 */
export default function PlanCanvas({
  graph,
  steps,
  step,
  selectedStep,
  onSelectStep,
  onSelectPod,
  onSelectNode,
}: Props) {
  const container = useRef<HTMLDivElement>(null);
  const cyRef = useRef<Core | null>(null);

  const model: Model = useMemo(() => toModel(graph), [graph]);
  const peak = useMemo(() => peakOccupancy(model, steps), [model, steps]);
  const columns = useMemo(() => gridColumns(model.nodes.length), [model.nodes.length]);

  // Build the graph once per plan. Steps only move things, so rebuilding on
  // every scrubber tick would throw away the animation entirely.
  useEffect(() => {
    if (!container.current) return;

    const placement = placementAtStep(model, steps, 0);
    const laid = computeLayout(model, placement, peak, columns);

    const elements: ElementDefinition[] = [];
    for (const node of model.nodes) {
      const pos = laid.nodes.get(node.id)!;
      elements.push({
        group: "nodes",
        data: {
          id: node.id,
          kind: "node",
          label: node.name,
          zone: node.zone ?? "",
          drainStep: node.drainStep,
          w: laid.nodeWidth,
          h: laid.nodeHeight,
        },
        position: { ...pos },
        selectable: false,
        grabbable: false,
      });
    }
    for (const pod of model.pods) {
      const pos = laid.pods.get(pod.id)!;
      elements.push({
        group: "nodes",
        data: {
          id: pod.id,
          kind: "pod",
          key: pod.key,
          label: pod.label,
          blocked: pod.blocked,
          movable: pod.movable,
          owner: pod.ownerKind ?? "",
        },
        position: { ...pos },
        selectable: false,
        grabbable: false,
      });
    }

    const cy = cytoscape({
      container: container.current,
      elements,
      layout: { name: "preset" },
      style: styleSheet(),
      minZoom: 0.15,
      maxZoom: 3,
      wheelSensitivity: 0.2,
      // Boxes and pods are positioned deliberately; letting a drag move them
      // would destroy the packing picture the layout exists to convey.
      autoungrabify: true,
      boxSelectionEnabled: false,
    });
    cyRef.current = cy;

    cy.on("tap", "node[kind = 'node']", (e) => {
      const drain = e.target.data("drainStep") as number;
      onSelectNode(e.target.data("label") as string);
      onSelectStep(drain > 0 ? drain : null);
    });
    cy.on("tap", "node[kind = 'pod']", (e) => {
      onSelectPod(e.target.data("key") as string);
    });
    cy.on("tap", (e) => {
      if (e.target === cy) {
        onSelectStep(null);
        onSelectPod(null);
        onSelectNode(null);
      }
    });

    cy.fit(undefined, 30);

    return () => {
      cy.destroy();
      cyRef.current = null;
    };
  }, [model, steps, peak, columns, onSelectStep, onSelectPod, onSelectNode]);

  // Animate to the requested step.
  useEffect(() => {
    const cy = cyRef.current;
    if (!cy) return;

    const placement = placementAtStep(model, steps, step);
    const laid = computeLayout(model, placement, peak, columns);
    const reduceMotion = window.matchMedia("(prefers-reduced-motion: reduce)").matches;

    cy.batch(() => {
      for (const node of model.nodes) {
        const el = cy.getElementById(node.id);
        if (el.empty()) continue;
        const drained = node.drainStep > 0 && node.drainStep <= step;
        el.data("emptied", laid.emptied.has(node.name));
        el.data("drained", drained);
        el.data("load", laid.nodeLoad.get(node.name) ?? 0);
        el.data(
          "fill",
          node.cpuAllocatable > 0
            ? (laid.nodeLoad.get(node.name) ?? 0) / node.cpuAllocatable
            : 0,
        );
      }
      for (const pod of model.pods) {
        const el = cy.getElementById(pod.id);
        if (el.empty()) continue;
        const target = laid.pods.get(pod.id);
        if (!target) continue;
        if (reduceMotion) {
          el.position(target);
        } else {
          el.stop().animate({ position: target }, { duration: 420, easing: "ease-in-out-cubic" });
        }
      }
    });
  }, [model, steps, step, peak, columns]);

  // Highlight whichever step is selected.
  useEffect(() => {
    const cy = cyRef.current;
    if (!cy) return;
    cy.batch(() => {
      cy.nodes().removeClass("highlight dim");
      if (selectedStep == null) return;
      const target = steps.find((s) => s.sequenceNumber === selectedStep);
      if (!target) return;

      const involved = new Set<string>();
      if (target.targetNode) involved.add(`node:${target.targetNode}`);
      for (const m of target.moves) {
        involved.add(`pod:${m.namespace}/${m.pod}`);
        involved.add(`node:${m.toNode}`);
      }
      cy.nodes().forEach((n) => {
        n.addClass(involved.has(n.id()) ? "highlight" : "dim");
      });
    });
  }, [selectedStep, steps]);

  return <div className="canvas" ref={container} role="img" aria-label="Cluster packing" />;
}

/**
 * Node fill encodes utilisation on the sequential blue ramp — one hue,
 * light to dark, per the palette's rule for magnitude.
 *
 * Impact ratings are never encoded by colour alone here: a drained node also
 * gets a distinct border style per rating (solid / dashed / double), because
 * the validator puts Red and Green at CVD deltaE 4.1, which is indistinguishable
 * for deuteranopia.
 */
function styleSheet(): cytoscape.StylesheetJson {
  const seq = [css("--seq-100"), css("--seq-250"), css("--seq-400"), css("--seq-550"), css("--seq-700")];
  const surface = css("--surface");
  const raised = css("--surface-raised");
  const border = css("--border-strong");
  const muted = css("--text-muted");
  const text = css("--text-secondary");

  return [
    {
      selector: "node[kind = 'node']",
      style: {
        shape: "round-rectangle",
        width: "data(w)",
        height: "data(h)",
        "background-color": surface,
        "background-opacity": 1,
        "border-width": 1,
        "border-color": border,
        label: "data(label)",
        "text-valign": "top",
        "text-halign": "center",
        "text-margin-y": NODE_HEADER - 6,
        "font-size": 8,
        color: muted,
        "font-family": css("--font-mono") || "monospace",
        "z-index": 1,
      },
    },
    // Utilisation, five steps of one hue. Bucketed rather than continuous so
    // the difference between two nodes is actually readable.
    { selector: "node[kind = 'node'][fill >= 0.01]", style: { "border-color": seq[0], "border-width": 2 } },
    { selector: "node[kind = 'node'][fill >= 0.25]", style: { "border-color": seq[1] } },
    { selector: "node[kind = 'node'][fill >= 0.5]", style: { "border-color": seq[2] } },
    { selector: "node[kind = 'node'][fill >= 0.75]", style: { "border-color": seq[3] } },
    { selector: "node[kind = 'node'][fill >= 0.95]", style: { "border-color": seq[4] } },
    {
      selector: "node[kind = 'node'][?drained]",
      style: {
        "background-opacity": 0.25,
        "border-style": "dashed",
        "border-color": muted,
        color: muted,
      },
    },
    {
      selector: "node[kind = 'pod']",
      style: {
        shape: "round-rectangle",
        width: POD_SIZE,
        height: POD_SIZE,
        "background-color": raised,
        "border-width": 1,
        "border-color": border,
        "z-index": 10,
      },
    },
    {
      selector: "node[kind = 'pod'][?movable]",
      style: { "background-color": css("--seq-400"), "background-opacity": 0.55 },
    },
    // A blocked pod is why a node cannot be emptied, so it is marked by shape
    // as well as colour.
    {
      selector: "node[kind = 'pod'][?blocked]",
      style: {
        shape: "diamond",
        "background-color": css("--impact-red"),
        "background-opacity": 0.9,
        "border-color": css("--impact-red"),
      },
    },
    { selector: ".dim", style: { opacity: 0.18 } },
    {
      selector: "node[kind = 'pod'].highlight",
      style: { "border-width": 2, "border-color": css("--text-primary"), "z-index": 30 },
    },
    {
      selector: "node[kind = 'node'].highlight",
      style: { "border-width": 3, "border-color": css("--text-primary"), color: text },
    },
  ] as cytoscape.StylesheetJson;
}
