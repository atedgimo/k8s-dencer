/**
 * Which face the instrument wears.
 *
 * The palette has always supported both, but nothing ever set `data-theme`, so
 * the operating system was the only vote and an operator who wanted the dark
 * instrument on a light desktop had no way to ask for it.
 *
 * Dark is the default rather than "follow the OS". This is a wall-mounted
 * instrument as much as an app — the ground goes black so that a saturated
 * rating is the only lit thing on it, which is the whole premise of the
 * palette. A light desktop is a statement about documents and mail, not about
 * how someone wants to watch a drain.
 *
 * The choice persists in localStorage, which is fine here. The rule in
 * test/ui is scoped to auth.ts and exists so a bearer token cannot outlive the
 * tab; a colour preference is not a credential, and putting it in
 * sessionStorage would mean re-picking the theme in every new tab.
 */

export type Theme = "dark" | "light";

const KEY = "dencer.theme";

export function storedTheme(): Theme {
  try {
    const v = localStorage.getItem(KEY);
    if (v === "dark" || v === "light") return v;
  } catch {
    // Private browsing, or storage disabled. Fall through to the default.
  }
  return "dark";
}

/** Stamps the root element, which is what theme.css keys off. */
export function applyTheme(theme: Theme): void {
  document.documentElement.setAttribute("data-theme", theme);
  try {
    localStorage.setItem(KEY, theme);
  } catch {
    // The theme still applies for this page; it just will not be remembered.
  }
}
