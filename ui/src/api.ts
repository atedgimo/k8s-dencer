import { runtimeConfig } from "./runtime-config";

export type Impact = "Green" | "Yellow" | "Red";

export interface Move {
  namespace: string;
  pod: string;
  fromNode: string;
  toNode: string;
}

export interface ImpactReason {
  kind: string;
  subject?: string;
  detail?: string;
}

export interface PlanStep {
  id: string;
  sequenceNumber: number;
  targetNode?: string;
  moves: Move[];
  impact: Impact;
  rationale: string;
  reasons?: ImpactReason[];
}

export interface Plan {
  id: string;
  generatedAt: string;
  snapshotTakenAt: string;
  status: string;
  steps: PlanStep[];
  nodesBefore: number;
  nodesAfter: number;
}

export interface PlanResponse {
  plan: Plan;
  strategy: string;
  storedAt: string;
  ratings: Record<Impact, number>;
  readOnly: boolean;
}

export interface GraphData {
  id: string;
  parent?: string;
  source?: string;
  target?: string;
  kind: "node" | "pod" | "edge";
  label: string;

  zone?: string;
  ready?: boolean;
  cordoned?: boolean;
  cpuAllocatable?: number;
  cpuRequested?: number;
  memAllocatable?: number;
  memRequested?: number;
  utilization?: number;
  drained?: boolean;
  drainStep?: number;

  namespace?: string;
  cpuRequest?: number;
  memRequest?: number;
  ownerKind?: string;
  ownerName?: string;
  movable?: boolean;
  targetNode?: string;
  moveStep?: number;
  blocked?: boolean;

  relation?: "move" | "pdb" | "anti-affinity";
  impact?: string;
}

export interface GraphElement {
  group: "nodes" | "edges";
  data: GraphData;
}

export interface GraphStats {
  nodesBefore: number;
  nodesAfter: number;
  reclaimed: number;
  steps: number;
  ratings: Record<Impact, number>;
  podsMoved: number;
  cpuReclaimedMilli: number;
  memoryReclaimedBytes: number;
}

export interface GraphPayload {
  planId: string;
  elements: GraphElement[];
  stats: GraphStats;
}

export interface PodConstraint {
  kind: string;
  subject?: string;
  hard: boolean;
  blocking: boolean;
  explanation: string;
}

export interface PodConstraints {
  namespace: string;
  name: string;
  nodeName?: string;
  movable: boolean;
  constraints: PodConstraint[];
  candidateNodes: string[] | null;
}

export interface StepDetail {
  planId: string;
  step: PlanStep;
  constraints: PodConstraints[];
}

const base = () => runtimeConfig().apiBaseUrl;

/** ApiError distinguishes "nothing planned yet" from a real failure — a fresh
 *  install legitimately has no plan, and the UI must not show that as broken. */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
  }
  get isEmpty() {
    return this.status === 404;
  }
}

async function get<T>(path: string, signal?: AbortSignal): Promise<T> {
  const res = await fetch(`${base()}${path}`, { signal });
  if (!res.ok) {
    let detail = res.statusText;
    try {
      const body = (await res.json()) as { error?: string };
      if (body.error) detail = body.error;
    } catch {
      /* non-JSON error body */
    }
    throw new ApiError(res.status, detail);
  }
  return (await res.json()) as T;
}

export const api = {
  latestPlan: (signal?: AbortSignal) => get<PlanResponse>("/api/v1/plans/latest", signal),
  graph: (planId: string, signal?: AbortSignal) =>
    get<GraphPayload>(`/api/v1/plans/${planId}/graph`, signal),
  step: (planId: string, seq: number, signal?: AbortSignal) =>
    get<StepDetail>(`/api/v1/plans/${planId}/steps/${seq}`, signal),
};

/** Subscribes to plan changes. Returns an unsubscribe function.
 *
 *  EventSource reconnects on its own, so there is no retry logic here. */
export function subscribePlans(onChange: (planId: string) => void): () => void {
  const source = new EventSource(`${base()}/api/v1/events`);
  const handler = (e: MessageEvent) => {
    try {
      const data = JSON.parse(e.data) as { planId?: string };
      if (data.planId) onChange(data.planId);
    } catch {
      /* ignore malformed frames */
    }
  };
  source.addEventListener("plan", handler as EventListener);
  return () => {
    source.removeEventListener("plan", handler as EventListener);
    source.close();
  };
}

export function formatCPU(milli: number): string {
  if (milli >= 1000) {
    const cores = milli / 1000;
    return `${cores % 1 === 0 ? cores : cores.toFixed(1)}`;
  }
  return `${milli}m`;
}

export function formatBytes(bytes: number): string {
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let v = bytes;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v % 1 === 0 ? v : v.toFixed(1)} ${units[i]}`;
}
