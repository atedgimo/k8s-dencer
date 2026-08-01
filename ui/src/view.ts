/**
 * How the field draws a node.
 *
 * Three renderings, and they are zoom levels rather than skins. Each answers a
 * different question, and which question an operator has depends mostly on how
 * big their cluster is:
 *
 *   rack   — a node as a rack unit with a bay per pod. The only view where an
 *            individual pod is an object you can point at, and the only one
 *            where the drain animation is pods moving. Falls apart past a few
 *            hundred nodes, which is exactly when nobody is looking at
 *            individual pods anyway.
 *   wells  — a node as a vessel with a level. Legible at a thousand nodes with
 *            no numbers read, and the drain animation becomes the thing
 *            draining. This is what M19's density mode was reaching for.
 *   panel  — one row per node, fullest-first, with a load trace. Densest, and
 *            the ordering makes the argument for consolidating without anyone
 *            explaining it. Trades the spatial picture away entirely.
 *
 * The default follows cluster size, because the right answer usually does. An
 * explicit choice overrides it and persists — same reasoning as the theme: a
 * view preference is not a credential, so localStorage is the right home and
 * sessionStorage would mean re-picking it in every tab.
 */

export type FieldView = "rack" | "wells" | "panel";

/**
 * The app's top-level surface: the field (in whichever FieldView) or the
 * History timeline. A separate axis from FieldView on purpose — History is
 * not a way of drawing nodes, it is a different question ("over time" rather
 * than "right now"), and conflating the axes would make "come back from
 * History to the view I was using" impossible.
 */
export type Surface = "plan" | "history" | "advice";

export const SURFACE_LABELS: Record<Surface, string> = {
  plan: "Plan",
  history: "History",
  advice: "Advice",
};

const KEY = "dencer.view";

/** Above this many nodes, individual pods stop being worth drawing. */
export const RACK_LIMIT = 120;
/** Above this, even a vessel per node is too much geometry to scan. */
export const WELLS_LIMIT = 600;

export function defaultView(nodeCount: number): FieldView {
  if (nodeCount > WELLS_LIMIT) return "panel";
  if (nodeCount > RACK_LIMIT) return "wells";
  return "rack";
}

/** The stored choice, or null when the operator has not expressed one. */
export function storedView(): FieldView | null {
  try {
    const v = localStorage.getItem(KEY);
    if (v === "rack" || v === "wells" || v === "panel") return v;
  } catch {
    // Private browsing. The size-based default still applies.
  }
  return null;
}

export function rememberView(v: FieldView): void {
  try {
    localStorage.setItem(KEY, v);
  } catch {
    // Applies for this page; simply will not be remembered.
  }
}

export const VIEW_LABELS: Record<FieldView, string> = {
  rack: "Rack",
  wells: "Wells",
  panel: "Panel",
};
