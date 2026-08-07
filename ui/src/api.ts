import { authHeaders } from "./auth";
import { runtimeConfig } from "./runtime-config";

export type Impact = "Green" | "Yellow" | "Red";

export interface Move {
  namespace: string;
  pod: string;
  fromNode: string;
  toNode: string;
  /**
   * What the pod takes with it. Present so the density view can show receiving
   * nodes filling as the scrubber advances — above the graph endpoint's detail
   * limit there is no per-pod data to look this up in.
   */
  cpuMilli?: number;
  memoryBytes?: number;
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
}

export interface PlanResponse {
  plan: Plan;
  strategy: string;
  storedAt: string;
  ratings: Record<Impact, number>;
  readOnly: boolean;
}

// Mirrors graph.Data in internal/api/graph. Kept exactly in step with it by
// graph_contract_test.go, which fails both ways: on a Go field the UI never
// reads, and on a field declared here that the payload no longer sends. The
// second half matters because every field is optional, so a stale declaration
// reads as undefined at runtime rather than failing to compile.
//
// Where a pod moves and which step moves it are deliberately not here. Both
// come from the plan's steps, which the UI already holds.
export interface GraphData {
  id: string;
  parent?: string;
  kind: "node" | "pod";
  label: string;

  zone?: string;
  instanceType?: string;
  capacityType?: string;
  /** The node group, from the provider's own label. Distinct from
   *  instanceType: a pool can hold mixed shapes, and two pools can share one. */
  pool?: string;
  ready?: boolean;
  cordoned?: boolean;
  cpuAllocatable?: number;
  cpuRequested?: number;
  memAllocatable?: number;
  memRequested?: number;
  /** Measured usage summed from the node's pods; absent when unmeasured. */
  cpuUsed?: number;
  memUsed?: number;
  drainStep?: number;
  podCount?: number;
  blockedCount?: number;
  pinnedCount?: number;

  namespace?: string;
  cpuRequest?: number;
  memRequest?: number;
  ownerKind?: string;
  ownerName?: string;
  movable?: boolean;
  blocked?: boolean;
}

export interface GraphElement {
  data: GraphData;
}

export interface GraphStats {
  nodesBefore: number;
  reclaimable: number;
  steps: number;
  /** The utilisation fraction THIS plan refused to pack above; the Wells
   *  lens draws its ceiling line here. Absent on plans that predate it. */
  packCeiling?: number;
  // ratings/podsMoved/cpuReclaimableMilli/memReclaimableBytes are gone with
  // their reader, the old verdict panel: the hero derives per-verdict counts
  // and pricing from the steps it already renders, so the numbers cannot
  // disagree with the list under them.
}

export interface GraphPayload {
  planId: string;
  elements: GraphElement[];
  stats: GraphStats;
  /**
   * The server summarised pods onto their nodes rather than sending one
   * element each, because there were more than it will draw individually.
   */
  aggregated?: boolean;
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
  /**
   * namespace/name of each moved pod that is its workload's only replica.
   * Evicting one takes the workload to zero while rescheduling runs — the
   * review pane flags exactly these.
   */
  singletons?: string[];
}

/** One point on the planner's timeline. Mirrors store.Sample. */
export interface HistorySample {
  takenAt: string;
  nodes: number;
  pods: number;
  cpuReqMilli: number;
  cpuAllocMilli: number;
  memReqBytes: number;
  memAllocBytes: number;
  cpuUsedMilli: number;
  memUsedBytes: number;
  hasUsage: boolean;
  reclaimable: number;
}

export interface HistoryRunMarker {
  id: string;
  status: RunStatus;
  mode?: string;
  dryRun: boolean;
  finishedAt?: string;
  /** The audit ledger's point: who authorised it, of what plan, how much. */
  actor?: string;
  planId?: string;
  steps?: number;
}

export interface HistoryResponse {
  hours: number;
  samples: HistorySample[];
  plans: Array<{ id: string; generatedAt: string; nodesBefore: number; nodesAfter: number }>;
  reclamations: Reclamation[];
  runs: HistoryRunMarker[];
}

export interface Recommendation {
  kind: string;
  severity: "high" | "medium" | "info";
  workload: string;
  why: string;
  fix?: string;
  /**
   * Plan steps this finding holds back, by sequence number. The queue is
   * ranked by this: fixing the top finding unlocks the most nodes. Empty or
   * absent means the finding is advice, not a blocker.
   */
  unblocksSteps?: number[];
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

/**
 * What actually became of a drained node.
 *
 * Every other reclamation figure in this app is a plan-time prediction. This
 * one is observed: the planner watches nodes, and a drained node either
 * disappears (something removed it) or comes back (someone uncordoned it).
 */
export interface Reclamation {
  node: string;
  drainedAt: string;
  runId?: string;
  planId?: string;
  step?: number;
  resolvedAt?: string;
  outcome?: "reclaimed" | "returned";
  /** Allocatable captured at drain time — the ledger's measured inputs. */
  cpuMilli?: number;
  memBytes?: number;
  /** The node left without this product draining it: an autoscaler, or
   *  someone with kubectl. Shown, never counted as ours. */
  external?: boolean;
}

export interface ReclamationsResponse {
  tracking: boolean;
  /**
   * Whether anything here removes drained nodes. observedWorking is proof —
   * a recorded removal; detected is a promise — an autoscaler pod is
   * visible. Neither being true is NOT "no reclaimer": managed control
   * planes run theirs out of sight.
   */
  reclaimer?: { observedWorking: boolean; detected: string };
  awaiting: Reclamation[];
  recent: Reclamation[];
  stats: {
    awaiting: number;
    reclaimed: number;
    returned: number;
    medianReclamationSeconds: number;
    windowDays: number;
    /** The ledger — measured capacity actually returned, from drain-time records. */
    reclaimedCpuMilli: number;
    reclaimedMemBytes: number;
    uncountedNodes: number;
    /** Nodes that left without this product draining them. Reported, never
     *  added to the ledger — the savings are real but not ours. */
    externallyReclaimed?: number;
    /** Absent unless the operator configured uiBackend.pricing. Absent means
     *  no claim about money, not a claim of zero. */
    pricing?: {
      currency: string;
      perHour: number;
      perMonth: number;
      pricedNodes: number;
      unpricedNodes: number;
      externalPerMonth: number;
      externalPriced: number;
    };
  };
}

/**
 * Who the API server says you are, and which cluster you are looking at.
 *
 * identity is what TokenReview verified — not a claim this page decoded from a
 * token it happens to be holding. clusterLabel is operator-set and absent
 * unless someone configured it, because a guessed environment name is worse
 * than none.
 */
export interface VersionResponse {
  version: string;
  readOnly: boolean;
  latestPlanId?: string;
  planGeneratedAt?: string;
  /**
   * When the cluster was last seen to still match the latest plan.
   *
   * Refreshed every planner resync even when the plan does not change, which
   * is the whole reason it is polled: the plan itself only re-publishes on a
   * content change, so on a steady cluster this is the only signal that
   * anything is still watching.
   */
  planConfirmedAt?: string;
  identity?: string;
  clusterLabel?: string;
}

export const api = {
  version: (signal?: AbortSignal) => get<VersionResponse>("/api/v1/version", signal),

  latestPlan: (signal?: AbortSignal) => get<PlanResponse>("/api/v1/plans/latest", signal),

  reclamations: (signal?: AbortSignal) =>
    get<ReclamationsResponse>("/api/v1/reclamations", signal),

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

  /** Queues a closed-loop run: the executor re-plans after every drain,
   *  inside the envelope. Consent to a policy, not a list — the caller must
   *  have shown the policy wording before calling this. */
  converge: (maxNodes: number, maxImpact: "Green" | "Yellow", dryRun: boolean) =>
    post<{ runId: string; status: RunStatus }>(`/api/v1/converge`, {
      maxNodes,
      maxImpact,
      dryRun,
    }),

  /** What is missing, with fixes. */
  recommendations: (signal?: AbortSignal) =>
    get<{ takenAt: string; recommendations: Recommendation[] }>(`/api/v1/recommendations`, signal),

  /** The cluster's timeline, chart-ready. */
  history: (hours: number, signal?: AbortSignal) =>
    get<HistoryResponse>(`/api/v1/history?hours=${hours}`, signal),

  run: (runId: string, signal?: AbortSignal) => get<RunDetail>(`/api/v1/runs/${runId}`, signal),

  /** Whether Red steps can run right now, and if not, when they could. */
  windows: (signal?: AbortSignal) => get<Windows>(`/api/v1/windows`, signal),

  /** Simulate losing nodes or a zone: does everything still fit? */
  whatif: (body: { removeNodes?: string[]; removeZone?: string }) =>
    post<WhatIf>(`/api/v1/whatif`, body),

  /** Requests against observed usage, per workload. Needs metrics-server. */
  rightsizing: (signal?: AbortSignal) => get<Rightsizing>(`/api/v1/rightsizing`, signal),
  /** Per node: will it drain, and if not, what is in the way. */
  preflight: (signal?: AbortSignal) => get<Preflight>(`/api/v1/preflight`, signal),

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

/** One workload's requests against what it actually uses. */
export interface RightsizingRow {
  workload: string;
  pods: number;
  requestedMilli: number;
  usedMilli: number;
  requestedBytes: number;
  usedBytes: number;
}

export interface Rightsizing {
  /** False when no usage source is configured — unmeasured, never idle. */
  available: boolean;
  reason?: string;
  takenAt?: string;
  workloads?: RightsizingRow[];
  totalRequestedMilli?: number;
  totalUsedMilli?: number;
}

export interface PreflightBlocker {
  pod: string;
  kind: string;
  explanation: string;
}

export interface PreflightNode {
  node: string;
  ready: boolean;
  cordoned: boolean;
  pods: number;
  drainable: boolean;
  blockers: PreflightBlocker[];
}

export interface Preflight {
  takenAt: string;
  planId: string;
  nodes: PreflightNode[];
  drainable: number;
  total: number;
}

export interface WindowState {
  name: string;
  open: boolean;
  allowsRed: boolean;
  /** Why it is closed, in words. The window package writes one for every
   *  failure — an unparseable cron, an unknown zone, a suspended window —
   *  and until now nobody could read any of them. */
  reason: string;
  closesAt?: string;
  nextOpen?: string;
  selector?: Record<string, string>;
  maxNodes?: number;
}

export interface Windows {
  /** False when the deployment cannot read windows at all. Distinct from
   *  "none are defined": only one of those means Red can never run. */
  available: boolean;
  reason?: string;
  evaluatedAt?: string;
  anyOpen?: boolean;
  windows?: WindowState[];
}

export interface WhatIfHomeless {
  pod: string;
  why: string[];
}

export interface WhatIf {
  removed: string[];
  displaced: number;
  /** False when at least one displaced pod has nowhere legal to go. */
  fits: boolean;
  homeless: WhatIfHomeless[];
  basedOn: string;
  takenAt: string;
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
