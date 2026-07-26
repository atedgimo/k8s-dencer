import { GraphPayload, PlanStep } from "./api";

/**
 * Deterministic packing layout.
 *
 * Cytoscape's own layouts are not used. Node consolidation is a *packing*
 * picture — "which pods sit in which box, and how full is each box" — and an
 * organic force layout communicates none of that while jittering on every
 * relayout. Positions are computed here instead: node boxes on a fixed grid,
 * pods in a sub-grid inside each box. That makes the picture stable between
 * renders and, more importantly, makes the step scrubber possible: morphing
 * between two steps is just animating each pod to a new computed position.
 *
 * Node boxes are plain nodes rather than Cytoscape compound parents, because a
 * compound parent auto-sizes to its children and would collapse to nothing the
 * moment a node is drained — exactly the node the operator most wants to see.
 */

export const POD_SIZE = 22;
export const POD_GAP = 4;
export const POD_COLS = 4;
export const NODE_HEADER = 26;
export const NODE_PAD = 8;
export const NODE_GAP_X = 34;
export const NODE_GAP_Y = 30;

export interface PodInfo {
  id: string;
  key: string;
  label: string;
  namespace: string;
  cpuRequest: number;
  memRequest: number;
  ownerKind?: string;
  ownerName?: string;
  movable: boolean;
  blocked: boolean;
  homeNode: string;
}

export interface NodeInfo {
  id: string;
  name: string;
  zone?: string;
  cpuAllocatable: number;
  memAllocatable: number;
  cordoned: boolean;
  ready: boolean;
  drainStep: number;
}

export interface Positioned {
  nodes: Map<string, { x: number; y: number }>;
  pods: Map<string, { x: number; y: number }>;
  nodeWidth: number;
  nodeHeight: number;
  /** Requested milliCPU per node at this step, for the utilisation fill. */
  nodeLoad: Map<string, number>;
  /** Nodes that are empty of movable pods at this step. */
  emptied: Set<string>;
}

export interface Model {
  nodes: NodeInfo[];
  pods: PodInfo[];
}

/** Extracts the domain model from the graph payload. */
export function toModel(graph: GraphPayload): Model {
  const nodes: NodeInfo[] = [];
  const pods: PodInfo[] = [];

  for (const el of graph.elements) {
    const d = el.data;
    if (d.kind === "node") {
      nodes.push({
        id: d.id,
        name: d.label,
        zone: d.zone,
        cpuAllocatable: d.cpuAllocatable ?? 0,
        memAllocatable: d.memAllocatable ?? 0,
        cordoned: d.cordoned ?? false,
        ready: d.ready ?? false,
        drainStep: d.drainStep ?? 0,
      });
    } else if (d.kind === "pod" && d.parent) {
      pods.push({
        id: d.id,
        key: `${d.namespace}/${d.label}`,
        label: d.label,
        namespace: d.namespace ?? "",
        cpuRequest: d.cpuRequest ?? 0,
        memRequest: d.memRequest ?? 0,
        ownerKind: d.ownerKind,
        ownerName: d.ownerName,
        movable: d.movable ?? false,
        blocked: d.blocked ?? false,
        homeNode: d.parent.replace(/^node:/, ""),
      });
    }
  }

  nodes.sort((a, b) => collate(a.name, b.name));
  pods.sort((a, b) => collate(a.key, b.key));
  return { nodes, pods };
}

/**
 * Where each pod lives after steps 1..step have been applied.
 *
 * Steps are applied in order rather than indexed, because a pod can be moved
 * by one step onto a node that a later step drains again.
 */
export function placementAtStep(model: Model, steps: PlanStep[], step: number): Map<string, string> {
  const placement = new Map<string, string>();
  for (const pod of model.pods) placement.set(pod.key, pod.homeNode);

  for (const s of steps) {
    if (s.sequenceNumber > step) break;
    for (const m of s.moves) {
      placement.set(`${m.namespace}/${m.pod}`, m.toNode);
    }
  }
  return placement;
}

/**
 * Computes positions for one step.
 *
 * Every node box is the same size, sized to the busiest node across the whole
 * plan rather than to the current step. A box that resized as pods left it
 * would make the grid reflow on every scrubber tick, and the reflow would read
 * as motion that means nothing.
 */
export function computeLayout(
  model: Model,
  placement: Map<string, string>,
  maxPodsPerNode: number,
  columns: number,
): Positioned {
  const rows = Math.max(2, Math.ceil(maxPodsPerNode / POD_COLS));
  const innerWidth = POD_COLS * POD_SIZE + (POD_COLS - 1) * POD_GAP;
  const innerHeight = rows * POD_SIZE + (rows - 1) * POD_GAP;
  const nodeWidth = innerWidth + NODE_PAD * 2;
  const nodeHeight = innerHeight + NODE_HEADER + NODE_PAD;

  const nodes = new Map<string, { x: number; y: number }>();
  const pods = new Map<string, { x: number; y: number }>();
  const nodeLoad = new Map<string, number>();
  const emptied = new Set<string>();

  const occupants = new Map<string, PodInfo[]>();
  for (const n of model.nodes) occupants.set(n.name, []);
  for (const pod of model.pods) {
    const host = placement.get(pod.key) ?? pod.homeNode;
    const list = occupants.get(host);
    if (list) list.push(pod);
  }

  model.nodes.forEach((node, i) => {
    const col = i % columns;
    const row = Math.floor(i / columns);
    const cx = col * (nodeWidth + NODE_GAP_X);
    const cy = row * (nodeHeight + NODE_GAP_Y);
    nodes.set(node.id, { x: cx, y: cy });

    const here = occupants.get(node.name) ?? [];
    let load = 0;
    let movable = 0;
    here.forEach((pod, j) => {
      load += pod.cpuRequest;
      if (pod.movable) movable++;
      const pc = j % POD_COLS;
      const pr = Math.floor(j / POD_COLS);
      pods.set(pod.id, {
        // Positions are centres in Cytoscape, so offset by half the box and
        // half the cell to land on the grid.
        x: cx - nodeWidth / 2 + NODE_PAD + POD_SIZE / 2 + pc * (POD_SIZE + POD_GAP),
        y: cy - nodeHeight / 2 + NODE_HEADER + POD_SIZE / 2 + pr * (POD_SIZE + POD_GAP),
      });
    });
    nodeLoad.set(node.name, load);
    if (movable === 0) emptied.add(node.name);
  });

  return { nodes, pods, nodeWidth, nodeHeight, nodeLoad, emptied };
}

/** The busiest a node ever gets across every step, so boxes never resize. */
export function peakOccupancy(model: Model, steps: PlanStep[]): number {
  let peak = 1;
  for (let step = 0; step <= steps.length; step++) {
    const placement = placementAtStep(model, steps, step);
    const counts = new Map<string, number>();
    for (const pod of model.pods) {
      const host = placement.get(pod.key) ?? pod.homeNode;
      counts.set(host, (counts.get(host) ?? 0) + 1);
    }
    for (const c of counts.values()) peak = Math.max(peak, c);
  }
  return peak;
}

/** Grid columns chosen to keep the whole cluster roughly square. */
export function gridColumns(nodeCount: number): number {
  return Math.max(1, Math.ceil(Math.sqrt(nodeCount * 1.4)));
}

/** Sorts names so node-2 comes before node-10. */
function collate(a: string, b: string): number {
  return a.localeCompare(b, undefined, { numeric: true, sensitivity: "base" });
}
