import { useCallback, useEffect, useRef, useState } from "react";
import { ApiError, Run, RunDetail, RunEvent, api, isTerminal } from "./api";

export type RunState =
  | { status: "idle" }
  | { status: "starting" }
  | { status: "active"; run: Run; events: RunEvent[] }
  | { status: "done"; run: Run; events: RunEvent[] }
  | { status: "error"; message: string; grantWith?: string };

/**
 * Starts a consolidation and follows it to the end.
 *
 * Polls rather than subscribing. The SSE stream carries plan changes and a
 * run-queued notice, but the per-pod eviction detail lives in the run's audit
 * trail — and that trail is the thing worth showing, because it is the same
 * record an operator will read afterwards when asked what happened. Polling
 * one endpoint for the seconds a run lasts is cheaper than maintaining a
 * second event channel that has to agree with it.
 */
export function useRun(onFinished?: () => void) {
  const [state, setState] = useState<RunState>({ status: "idle" });
  const timer = useRef<number | null>(null);
  const finished = useRef(onFinished);
  finished.current = onFinished;

  const stop = useCallback(() => {
    if (timer.current !== null) {
      clearTimeout(timer.current);
      timer.current = null;
    }
  }, []);

  const follow = useCallback(
    (runId: string) => {
      const tick = async () => {
        let detail: RunDetail;
        try {
          detail = await api.run(runId);
        } catch (err) {
          setState({ status: "error", message: describe(err) });
          return;
        }

        if (isTerminal(detail.run.status)) {
          setState({ status: "done", run: detail.run, events: (detail.events ?? []) });
          // The cluster has changed, so whatever is on screen is now stale.
          finished.current?.();
          return;
        }
        setState({ status: "active", run: detail.run, events: (detail.events ?? []) });
        timer.current = window.setTimeout(tick, 1200);
      };
      void tick();
    },
    [],
  );

  const start = useCallback(
    async (planId: string, steps: number[], dryRun: boolean) => {
      stop();
      setState({ status: "starting" });
      try {
        const { runId } = await api.execute(planId, steps, dryRun);
        follow(runId);
      } catch (err) {
        setState({
          status: "error",
          message: describe(err),
          grantWith: err instanceof ApiError ? err.grantWith : undefined,
        });
      }
    },
    [follow, stop],
  );

  // Converge shares everything with start except the request: same follow,
  // same trail, same rejoin-on-reload. The run mode lives server-side.
  const startConverge = useCallback(
    async (maxNodes: number, maxImpact: "Green" | "Yellow", dryRun: boolean) => {
      stop();
      setState({ status: "starting" });
      try {
        const { runId } = await api.converge(maxNodes, maxImpact, dryRun);
        follow(runId);
      } catch (err) {
        setState({
          status: "error",
          message: describe(err),
          grantWith: err instanceof ApiError ? err.grantWith : undefined,
        });
      }
    },
    [follow, stop],
  );

  const dismiss = useCallback(() => {
    stop();
    setState({ status: "idle" });
  }, [stop]);

  // Rejoin a run already in flight. Reloading the page during a consolidation
  // should not lose sight of it — that is exactly the moment an operator most
  // wants to be watching.
  useEffect(() => {
    let live = true;
    void api
      .activeRun()
      .then(({ active }) => {
        if (live && active) follow(active.id);
      })
      .catch(() => {
        /* no active run, or reads are not permitted; neither is worth a message */
      });
    return () => {
      live = false;
      stop();
    };
  }, [follow, stop]);

  return { state, start, startConverge, dismiss };
}

function describe(err: unknown): string {
  if (err instanceof ApiError) return err.message;
  return err instanceof Error ? err.message : String(err);
}
