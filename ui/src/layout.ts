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

/**
 * Widest a node box is allowed to get, in pod columns.
 *
 * Boxes are sized to the busiest node in the whole plan so the grid never
 * reflows while scrubbing. With four columns that made a node peaking at 28
 * pods seven rows tall, and since most nodes hold three or four, the field
 * became mostly empty box. Widening instead of heightening keeps the box near
 * square and the field dense.
 */
const MAX_POD_COLS = 10;

/**
 * Side of a node box in density mode, where nothing is drawn inside it but a
 * count and a fill bar. Small enough that a thousand nodes fit a scroll or two,
 * large enough for the count to stay readable.
 */
const DENSITY_BOX = 92;

/** Columns for a box that must hold `peak` pods without becoming a tower. */
export function podColumns(peak: number): number {
  if (peak <= POD_COLS * 2) return POD_COLS;
  return Math.min(MAX_POD_COLS, Math.ceil(Math.sqrt(peak * 1.6)));
}
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
  cpuRequestedMilli: number;
  memAllocatable: number;
  cordoned: boolean;
  ready: boolean;
  drainStep: number;

  /**
   * Occupancy as the server counted it.
   *
   * The single source in both modes. In density mode there are no pod elements
   * to count; in detail mode counting them locally would give a second answer
   * that could disagree with this one, and two numbers for "how full is this
   * node" is worse than either.
   */
  podCount: number;
  blockedCount: number;
  pinnedCount: number;
}

export interface Positioned {
  nodes: Map<string, { x: number; y: number }>;
  pods: Map<string, { x: number; y: number }>;
  nodeWidth: number;
  nodeHeight: number;
  /** Requested milliCPU per node at this step, for the utilisation fill. */
  nodeLoad: Map<string, number>;
  /**
   * Requested / allocatable per node, 0..1.
   *
   * Drives the brightness ramp, which is how fullness is encoded now that
   * colour is reserved for risk. A node at 0.94 is nearly white; one at 0.12
   * barely lifts off the ground.
   */
  nodeFill: Map<string, number>;
  /** Nodes that are empty of movable pods at this step. */
  emptied: Set<string>;
}

export interface Model {
  nodes: NodeInfo[];
  pods: PodInfo[];
  /**
   * True when the server summarised pods onto their nodes instead of sending
   * them, because there were too many to draw individually.
   *
   * Read from the payload rather than inferred from an empty pod list: a
   * fully-drained cluster has no pods either, and rendering that as "too big
   * to show" would be a lie in the one case an operator most wants confirmed.
   */
  aggregated: boolean;
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
        cpuRequestedMilli: d.cpuRequested ?? 0,
        memAllocatable: d.memAllocatable ?? 0,
        cordoned: d.cordoned ?? false,
        ready: d.ready ?? false,
        drainStep: d.drainStep ?? 0,
        podCount: d.podCount ?? 0,
        blockedCount: d.blockedCount ?? 0,
        pinnedCount: d.pinnedCount ?? 0,
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
  return { nodes, pods, aggregated: graph.aggregated ?? false };
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

  for (const s of steps ?? []) {
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
  steps: PlanStep[] = [],
  step = 0,
): Positioned {
  if (model.aggregated) {
    return densityLayout(model, columns, steps, step);
  }
  const cols = podColumns(maxPodsPerNode);
  const rows = Math.max(2, Math.ceil(maxPodsPerNode / cols));
  const innerWidth = cols * POD_SIZE + (cols - 1) * POD_GAP;
  const innerHeight = rows * POD_SIZE + (rows - 1) * POD_GAP;
  const nodeWidth = innerWidth + NODE_PAD * 2;
  const nodeHeight = innerHeight + NODE_HEADER + NODE_PAD;

  const nodes = new Map<string, { x: number; y: number }>();
  const pods = new Map<string, { x: number; y: number }>();
  const nodeLoad = new Map<string, number>();
  const nodeFill = new Map<string, number>();
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
      const pc = j % cols;
      const pr = Math.floor(j / cols);
      // Top-left origin. Positions were centres while this fed Cytoscape;
      // the packing field places absolutely-positioned elements instead, and
      // half-box offsets everywhere were pure friction.
      pods.set(pod.id, {
        x: cx + NODE_PAD + pc * (POD_SIZE + POD_GAP),
        y: cy + NODE_HEADER + pr * (POD_SIZE + POD_GAP),
      });
    });
    nodeLoad.set(node.name, load);
    nodeFill.set(node.name, node.cpuAllocatable > 0 ? load / node.cpuAllocatable : 0);
    if (movable === 0) emptied.add(node.name);
  });

  return { nodes, pods, nodeWidth, nodeHeight, nodeLoad, nodeFill, emptied };
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


/**
 * Layout when the server summarised pods onto their nodes.
 *
 * There are no pod elements to place, so the box is compact and its fill comes
 * from the counts and requests the server sent. The scrubber still works: the
 * plan's moves carry what each pod takes with it, so applying steps 1..n gives
 * the exact occupancy and load at that step, not an estimate.
 */
function densityLayout(
  model: Model,
  columns: number,
  steps: PlanStep[],
  step: number,
): Positioned {
  const nodeWidth = DENSITY_BOX;
  const nodeHeight = DENSITY_BOX;

  const nodes = new Map<string, { x: number; y: number }>();
  const nodeLoad = new Map<string, number>();
  const nodeFill = new Map<string, number>();
  const emptied = new Set<string>();

  // Start from what the server counted, then apply the moves up to this step.
  const count = new Map<string, number>();
  const load = new Map<string, number>();
  for (const n of model.nodes) {
    count.set(n.name, n.podCount);
    load.set(n.name, n.cpuRequestedMilli);
  }
  for (const s of steps) {
    if (s.sequenceNumber > step) break;
    for (const m of s.moves) {
      count.set(m.fromNode, (count.get(m.fromNode) ?? 0) - 1);
      count.set(m.toNode, (count.get(m.toNode) ?? 0) + 1);
      load.set(m.fromNode, (load.get(m.fromNode) ?? 0) - (m.cpuMilli ?? 0));
      load.set(m.toNode, (load.get(m.toNode) ?? 0) + (m.cpuMilli ?? 0));
    }
  }

  model.nodes.forEach((node, i) => {
    const col = i % columns;
    const row = Math.floor(i / columns);
    nodes.set(node.id, {
      x: col * (nodeWidth + NODE_GAP_X),
      y: row * (nodeHeight + NODE_GAP_Y),
    });

    const cpu = Math.max(0, load.get(node.name) ?? 0);
    nodeLoad.set(node.name, cpu);
    nodeFill.set(
      node.name,
      node.cpuAllocatable > 0 ? cpu / node.cpuAllocatable : 0,
    );
    // Pinned pods never move, so a node holding only pinned pods is never
    // emptied however far the scrubber runs.
    if ((count.get(node.name) ?? 0) <= node.pinnedCount) emptied.add(node.name);
  });

  return { nodes, pods: new Map(), nodeWidth, nodeHeight, nodeLoad, nodeFill, emptied };
}

/** Occupancy per node at a step, when pods were summarised rather than sent. */
export function densityCounts(
  model: Model,
  steps: PlanStep[],
  step: number,
): Map<string, number> {
  const count = new Map<string, number>();
  for (const n of model.nodes) count.set(n.name, n.podCount);
  for (const s of steps) {
    if (s.sequenceNumber > step) break;
    for (const m of s.moves) {
      count.set(m.fromNode, (count.get(m.fromNode) ?? 0) - 1);
      count.set(m.toNode, (count.get(m.toNode) ?? 0) + 1);
    }
  }
  return count;
}
