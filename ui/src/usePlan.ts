import { useCallback, useEffect, useState } from "react";
import { ApiError, GraphPayload, PlanResponse, api, subscribePlans } from "./api";
import { onTokenChange } from "./auth";

export type PlanState =
  | { status: "loading" }
  | { status: "empty" }
  | { status: "error"; message: string; needsAuth?: boolean; grantWith?: string }
  | { status: "ready"; plan: PlanResponse; graph: GraphPayload };

/**
 * Loads the latest plan and its graph, and reloads when the planner publishes
 * a new one.
 *
 * The plan and graph are fetched together and swapped together: rendering a
 * graph against a different plan's steps would put the scrubber out of step
 * with the canvas, which is worse than a moment of staleness.
 */
export function usePlan(): PlanState & { reload: () => void } {
  const [state, setState] = useState<PlanState>({ status: "loading" });
  const [nonce, setNonce] = useState(0);

  const reload = useCallback(() => setNonce((n) => n + 1), []);

  useEffect(() => {
    const controller = new AbortController();

    (async () => {
      try {
        const plan = await api.latestPlan(controller.signal);
        const graph = await api.graph(plan.plan.id, controller.signal);
        if (!controller.signal.aborted) {
          setState({ status: "ready", plan, graph });
        }
      } catch (err) {
        if (controller.signal.aborted) return;
        if (err instanceof ApiError && err.isEmpty) {
          // No plan yet is a normal state for a fresh install, not a fault.
          setState({ status: "empty" });
          return;
        }
        setState({
          status: "error",
          message: err instanceof Error ? err.message : String(err),
          needsAuth: err instanceof ApiError && err.needsAuth,
          grantWith: err instanceof ApiError ? err.grantWith : undefined,
        });
      }
    })();

    return () => controller.abort();
  }, [nonce]);

  useEffect(() => {
    return subscribePlans(() => reload());
  }, [reload]);

  // Signing in has to re-drive the fetch: without this the operator enters a
  // valid token and stares at the same 401 until they reload the page.
  useEffect(() => onTokenChange(reload), [reload]);

  return { ...state, reload };
}
