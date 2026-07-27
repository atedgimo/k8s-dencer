import { authHeaders } from "./auth";
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
    /** How to obtain the missing permission, when the server supplied it. */
    readonly grantWith?: string,
  ) {
    super(message);
  }
  get isEmpty() {
    return this.status === 404;
  }
  /** No credentials, or they expired. The caller should offer a sign-in. */
  get needsAuth() {
    return this.status === 401;
  }
  /** Authenticated, but lacking the permission. Signing in again will not help. */
  get isForbidden() {
    return this.status === 403;
  }
}

/** Turns a failed response into an ApiError carrying the server's own words. */
async function toApiError(res: Response): Promise<ApiError> {
  let detail = res.statusText || `request failed (${res.status})`;
  let grantWith: string | undefined;
  try {
    const body = (await res.json()) as { error?: string; grantWith?: string };
    if (body.error) detail = body.error;
    grantWith = body.grantWith;
  } catch {
    /* non-JSON error body */
  }
  return new ApiError(res.status, detail, grantWith);
}

/** Reports a transport failure in terms an operator can act on.
 *
 *  A bare "Failed to fetch" is what the browser gives us when the backend is
 *  unreachable, and it tells nobody anything. */
function toNetworkError(err: unknown): Error {
  if (err instanceof ApiError) return err;
  const target = base() || "the ui-backend";
  return new Error(
    `Cannot reach ${target}. The backend may be restarting, or a port-forward may have dropped.`,
  );
}

async function get<T>(path: string, signal?: AbortSignal): Promise<T> {
  let res: Response;
  try {
    res = await fetch(`${base()}${path}`, { signal, headers: authHeaders() });
  } catch (err) {
    if (signal?.aborted) throw err;
    throw toNetworkError(err);
  }
  if (!res.ok) throw await toApiError(res);
  return (await res.json()) as T;
}

export type RunStatus = "Pending" | "Running" | "Succeeded" | "Blocked" | "Failed";

/** Terminal states. Blocked is deliberately not Failed: the rails working is
 *  a different outcome from something breaking, and the UI says so. */
export function isTerminal(status: RunStatus): boolean {
  return status === "Succeeded" || status === "Blocked" || status === "Failed";
}

export interface Run {
  id: string;
  planId: string;
  steps: number[];
  dryRun: boolean;
  status: RunStatus;
  actor: string;
  actorGroups?: string[];
  requestedAt: string;
  startedAt?: string;
  finishedAt?: string;
  worker?: string;
  summary?: string;
}

export interface RunEvent {
  runId: string;
  sequence: number;
  at: string;
  level: "Info" | "Blocked" | "Error";
  step?: number;
  node?: string;
  pod?: string;
  action: string;
  /** The Safety Guard rail that refused, on Blocked events. */
  rule?: string;
  message: string;
}

export interface RunDetail {
  run: Run;
  events: RunEvent[];
}

async function post<T>(path: string, body: unknown): Promise<T> {
  const headers = authHeaders({ "Content-Type": "application/json" });
  let res: Response;
  try {
    res = await fetch(`${base()}${path}`, { method: "POST", headers, body: JSON.stringify(body) });
  } catch (err) {
    throw toNetworkError(err);
  }
  if (!res.ok) throw await toApiError(res);
  return (await res.json()) as T;
}

export const api = {
  latestPlan: (signal?: AbortSignal) => get<PlanResponse>("/api/v1/plans/latest", signal),

  /** A specific plan by id. Used to keep a pinned view on the plan an operator
   *  is actually working against, rather than whatever is newest. */
  plan: (planId: string, signal?: AbortSignal) =>
    get<PlanResponse>(`/api/v1/plans/${encodeURIComponent(planId)}`, signal),
  graph: (planId: string, signal?: AbortSignal) =>
    get<GraphPayload>(`/api/v1/plans/${planId}/graph`, signal),
  step: (planId: string, seq: number, signal?: AbortSignal) =>
    get<StepDetail>(`/api/v1/plans/${planId}/steps/${seq}`, signal),

  /** Queues steps for execution. Returns immediately with a run id — the
   *  executor claims the work separately, so this never blocks on a drain. */
  execute: (planId: string, steps: number[], dryRun: boolean) =>
    post<{ runId: string; status: RunStatus }>(`/api/v1/plans/${planId}/execute`, {
      steps,
      dryRun,
    }),

  podConstraints: (planId: string, namespace: string, pod: string, signal?: AbortSignal) =>
    get<PodConstraints>(
      `/api/v1/plans/${encodeURIComponent(planId)}/constraints/` +
        `${encodeURIComponent(namespace)}/${encodeURIComponent(pod)}`,
      signal,
    ),

  run: (runId: string, signal?: AbortSignal) => get<RunDetail>(`/api/v1/runs/${runId}`, signal),

  /** The in-flight run, if any. Lets a page reload rejoin a consolidation
   *  already in progress rather than losing sight of it. */
  activeRun: (signal?: AbortSignal) => get<{ active: Run | null }>("/api/v1/runs", signal),
};

/** Subscribes to plan changes. Returns an unsubscribe function.
 *
 *  Built on fetch rather than EventSource because the stream is authenticated
 *  and EventSource cannot send an Authorization header. The alternative —
 *  putting the token in the query string — would write a working credential
 *  into every access log between here and the backend.
 *
 *  Reconnection is therefore ours to handle, where EventSource gave it to us. */
export function subscribePlans(onChange: (planId: string) => void): () => void {
  const controller = new AbortController();
  let attempt = 0;

  const run = async () => {
    while (!controller.signal.aborted) {
      try {
        const res = await fetch(`${base()}/api/v1/events`, {
          signal: controller.signal,
          headers: authHeaders({ Accept: "text/event-stream" }),
        });
        if (!res.ok || !res.body) throw await toApiError(res);

        attempt = 0; // a successful connection resets the backoff
        await readEventStream(res.body, (event, data) => {
          if (event !== "plan") return;
          try {
            const parsed = JSON.parse(data) as { planId?: string };
            if (parsed.planId) onChange(parsed.planId);
          } catch {
            /* ignore malformed frames */
          }
        });
      } catch (err) {
        if (controller.signal.aborted) return;
        // A 401/403 will not fix itself by retrying harder; back off to the
        // ceiling immediately rather than hammering the API server with
        // TokenReviews for a token that is not going to start working.
        if (err instanceof ApiError && (err.needsAuth || err.isForbidden)) attempt = 5;
      }
      if (controller.signal.aborted) return;
      const delay = Math.min(1000 * 2 ** attempt++, 30_000);
      await new Promise((r) => setTimeout(r, delay));
    }
  };

  void run();
  return () => controller.abort();
}

/** Parses an SSE byte stream, invoking onEvent for each complete frame. */
async function readEventStream(
  body: ReadableStream<Uint8Array>,
  onEvent: (event: string, data: string) => void,
): Promise<void> {
  const reader = body.getReader();
  // Decoding incrementally rather than via TextDecoderStream: a multi-byte
  // character can straddle two chunks, and { stream: true } is what holds the
  // partial sequence until the rest arrives.
  const decoder = new TextDecoder();
  let buffer = "";

  for (;;) {
    const { done, value } = await reader.read();
    if (done) return;
    buffer += decoder.decode(value, { stream: true });

    // Frames are separated by a blank line. A chunk can split a frame
    // anywhere, so only complete frames are consumed and the tail is kept.
    let boundary = buffer.indexOf("\n\n");
    while (boundary !== -1) {
      const frame = buffer.slice(0, boundary);
      buffer = buffer.slice(boundary + 2);
      dispatchFrame(frame, onEvent);
      boundary = buffer.indexOf("\n\n");
    }
  }
}

function dispatchFrame(frame: string, onEvent: (event: string, data: string) => void) {
  let event = "message";
  const data: string[] = [];
  for (const line of frame.split("\n")) {
    if (line.startsWith(":")) continue; // comment / keep-alive
    const colon = line.indexOf(":");
    const field = colon === -1 ? line : line.slice(0, colon);
    // A single leading space after the colon is part of the framing, not data.
    const value = colon === -1 ? "" : line.slice(colon + 1).replace(/^ /, "");
    if (field === "event") event = value;
    else if (field === "data") data.push(value);
  }
  if (data.length > 0) onEvent(event, data.join("\n"));
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
