import { useCallback, useEffect, useRef, useState } from "react";
import { ApiError, GraphPayload, PlanResponse, api, subscribePlans } from "./api";
import { onTokenChange } from "./auth";

export type PlanState =
  | { status: "loading" }
  | { status: "empty" }
  | { status: "error"; message: string; needsAuth?: boolean; grantWith?: string }
  | { status: "ready"; plan: PlanResponse; graph: GraphPayload };

/**
 * Loads a plan and its graph, and decides when to swap it for a newer one.
 *
 * The plan and graph are fetched together and swapped together: rendering a
 * graph against a different plan's steps would put the scrubber out of step
 * with the field, which is worse than a moment of staleness.
 *
 * `hold` pins the displayed plan. The planner republishes continuously — every
 * resync, and again immediately after any drain — so without pinning, ticking
 * "steps 4 through 9" and pausing to think is enough to have the plan swapped
 * underneath you. Step numbers are positional, so the selection would survive
 * as numbers while silently coming to mean different nodes. That is the one
 * failure in this UI that could cause an operator to drain something they did
 * not choose.
 *
 * While held, a newer plan sets `superseded` and waits to be asked for.
 */
export function usePlan(hold: boolean): PlanState & {
  reload: () => void;
  /** A newer plan exists and is being withheld because the view is pinned. */
  superseded: boolean;
  /** Drop the pin and move to the latest plan. */
  showLatest: () => void;
} {
  const [state, setState] = useState<PlanState>({ status: "loading" });
  const [nonce, setNonce] = useState(0);
  const [superseded, setSuperseded] = useState(false);

  // The plan being displayed, so a refresh can re-fetch *this* one rather than
  // whatever is newest.
  const pinnedID = useRef<string | null>(null);
  const holdRef = useRef(hold);
  holdRef.current = hold;

  const reload = useCallback(() => setNonce((n) => n + 1), []);

  const showLatest = useCallback(() => {
    pinnedID.current = null;
    setSuperseded(false);
    reload();
  }, [reload]);

  useEffect(() => {
    const controller = new AbortController();

    (async () => {
      try {
        const id = pinnedID.current;
        const plan = id
          ? await api.plan(id, controller.signal)
          : await api.latestPlan(controller.signal);
        const graph = await api.graph(plan.plan.id, controller.signal);
        if (controller.signal.aborted) return;

        pinnedID.current = holdRef.current ? plan.plan.id : null;
        setState({ status: "ready", plan, graph });
      } catch (err) {
        if (controller.signal.aborted) return;
        if (err instanceof ApiError && err.isEmpty) {
          // A pinned plan can be pruned out from under us; falling back to the
          // latest beats showing "no plan" on a cluster that has one.
          if (pinnedID.current) {
            pinnedID.current = null;
            setSuperseded(false);
            setNonce((n) => n + 1);
            return;
          }
          // No plan at all is a normal state for a fresh install, not a fault.
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

  // Pin the plan on screen the moment the view is held, so a publish arriving
  // a millisecond later cannot swap it.
  useEffect(() => {
    if (hold) {
      if (state.status === "ready" && !pinnedID.current) {
        pinnedID.current = state.plan.plan.id;
      }
    } else {
      pinnedID.current = null;
    }
  }, [hold, state]);

  useEffect(
    () =>
      subscribePlans(() => {
        if (holdRef.current) {
          setSuperseded(true);
          return;
        }
        reload();
      }),
    [reload],
  );

  // Signing in has to re-drive the fetch: without this the operator enters a
  // valid token and stares at the same 401 until they reload the page.
  useEffect(() => onTokenChange(reload), [reload]);

  return { ...state, reload, superseded, showLatest };
}
