import { useEffect, useState } from "react";
import { ApiError, PodConstraints, api } from "./api";

export type ConstraintsState =
  | { status: "idle" }
  | { status: "loading" }
  | { status: "missing" }
  | { status: "error"; message: string }
  | { status: "ready"; constraints: PodConstraints };

/**
 * Loads one pod's effective constraint set from the plan that is on screen.
 *
 * Deliberately scoped to the displayed plan rather than live cluster state:
 * the explanations must describe the same moment the canvas is drawing, or the
 * inspector would contradict the plan beside it.
 */
export function usePodConstraints(planId: string | null, podKey: string | null): ConstraintsState {
  const [state, setState] = useState<ConstraintsState>({ status: "idle" });

  useEffect(() => {
    if (!planId || !podKey) {
      setState({ status: "idle" });
      return;
    }
    const [namespace, name] = splitKey(podKey);
    if (!namespace || !name) {
      setState({ status: "missing" });
      return;
    }

    const controller = new AbortController();
    setState({ status: "loading" });

    (async () => {
      try {
        // Goes through the shared client so it carries the bearer token.
        // This used to be a bare fetch() and silently 401'd once M9 turned
        // authentication on: the plan loaded and only the inspector broke,
        // which is a nastier failure than the whole page refusing.
        const constraints = await api.podConstraints(planId, namespace, name, controller.signal);
        if (!controller.signal.aborted) {
          setState({ status: "ready", constraints });
        }
      } catch (err) {
        if (controller.signal.aborted) return;
        if (err instanceof ApiError && err.isEmpty) {
          setState({ status: "missing" });
          return;
        }
        setState({
          status: "error",
          message: err instanceof Error ? err.message : String(err),
        });
      }
    })();

    return () => controller.abort();
  }, [planId, podKey]);

  return state;
}

/** Pod keys are "namespace/name"; a name never contains a slash. */
function splitKey(key: string): [string, string] {
  const i = key.indexOf("/");
  if (i < 0) return ["", ""];
  return [key.slice(0, i), key.slice(i + 1)];
}
