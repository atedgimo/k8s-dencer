import { useCallback, useState } from "react";

/**
 * Muted findings. Client-side by design: muting removes a finding from the
 * QUEUE, never from the plan — the planner keeps seeing it, the guard keeps
 * enforcing it, and another operator's browser keeps showing it. A
 * server-side mute would be a policy decision wearing a UI toggle.
 *
 * localStorage, same reasoning as the theme: not a credential, and a mute
 * that reset in every new tab would just teach people to stop using it.
 */
const KEY = "dencer.mutedFindings";

function load(): Set<string> {
  try {
    const raw = localStorage.getItem(KEY);
    if (raw) return new Set(JSON.parse(raw) as string[]);
  } catch {
    // Private browsing, or a corrupt entry. An empty set is the safe read.
  }
  return new Set();
}

export function findingKey(kind: string, workload: string): string {
  return kind + "|" + workload;
}

export function useMuted(): {
  muted: Set<string>;
  mute: (key: string) => void;
  unmute: (key: string) => void;
} {
  const [muted, setMuted] = useState<Set<string>>(load);

  const persist = useCallback((next: Set<string>) => {
    setMuted(next);
    try {
      localStorage.setItem(KEY, JSON.stringify([...next]));
    } catch {
      // Applies for this page; simply will not be remembered.
    }
  }, []);

  return {
    muted,
    mute: useCallback((k: string) => persist(new Set([...load(), k])), [persist]),
    unmute: useCallback(
      (k: string) => {
        const next = load();
        next.delete(k);
        persist(next);
      },
      [persist],
    ),
  };
}
