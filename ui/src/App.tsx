import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, Impact, PlanStep, VersionResponse } from "./api";
import Inspector, { Selection } from "./components/Inspector";
import PackingField from "./components/PackingField";
import { ConfirmRun, RunTrail } from "./components/RunPanel";
import Scrubber from "./components/Scrubber";
import { SignIn } from "./components/SignIn";
import StepLedger from "./components/StepLedger";
import Verdict from "./components/Verdict";
import AppBar from "./components/AppBar";
import { authInfo, token as tokenStore } from "./auth";
import { onRenewed } from "./oidc";
import { runtimeConfig } from "./runtime-config";
import { usePlan } from "./usePlan";
import { useRun } from "./useRun";

export default function App() {
  // Pinned while there is a selection to protect or a run to watch. Step
  // numbers are positional, so a plan swapped underneath a ticked selection
  // would leave it meaning different nodes.
  const [checked, setChecked] = useState<Set<number>>(new Set());
  const [runActive, setRunActive] = useState(false);
  const state = usePlan(checked.size > 0 || runActive);
  const { kagentUrl } = runtimeConfig();

  const [step, setStep] = useState(0);
  const [playing, setPlaying] = useState(false);

  // Observed reclamation, as distinct from what the plan predicts. Polled
  // rather than pushed: it changes on the planner's resync, which is tens of
  // seconds, and it is never the reason someone is watching the screen.
  const [reclaimed, setReclaimed] = useState({ awaiting: 0, reclaimed: 0 });

  // Identity and cluster, for the header. Fetched once — neither changes
  // within a session, and re-polling them would be noise.
  const [server, setServer] = useState<VersionResponse | null>(null);
  useEffect(() => {
    let cancelled = false;
    api
      .version()
      .then((v) => {
        if (!cancelled) setServer(v);
      })
      .catch(() => {
        // The header simply shows less. Failing to learn the cluster name is
        // not a reason to withhold the plan.
      });
    return () => {
      cancelled = true;
    };
  }, []);

  // Sign out clears the credential this tab is carrying, and asks the OIDC
  // provider to forget the session too when there is one. Without the second
  // half, "sign out" would silently re-authenticate on the next page load.
  const handleSignOut = useCallback(async () => {
    try {
      const info = await authInfo();
      if (info.oidc.enabled) {
        const { signOut } = await import("./oidc");
        await signOut(info);
      }
    } catch {
      // Clearing the local token is the part that must happen regardless.
    }
    tokenStore.clear();
  }, []);

  useEffect(() => {
    let cancelled = false;
    const load = async () => {
      try {
        const r = await api.reclamations();
        if (!cancelled && r.tracking) {
          setReclaimed({ awaiting: r.stats.awaiting, reclaimed: r.stats.reclaimed });
        }
      } catch {
        // Reclamation tracking is supplementary. A backend that does not have
        // it, or a transient failure, must never blank the page — the field
        // and the ledger are what the operator came for.
      }
    };
    void load();
    const t = setInterval(load, 30_000);
    return () => {
      cancelled = true;
      clearInterval(t);
    };
  }, []);
  const [selectedStep, setSelectedStep] = useState<number | null>(null);
  const [selection, setSelection] = useState<Selection>(null);
  const [focusedRating, setFocusedRating] = useState<Impact | null>(null);
  const [pending, setPending] = useState<{ steps: PlanStep[]; dryRun: boolean } | null>(null);
  const lastToggled = useRef<number | null>(null);

  const planId = state.status === "ready" ? state.plan.plan.id : null;
  // Coalesced deliberately. The API guarantees an array, but a client that
  // unmounts its entire tree because a list arrived as null is too brittle for
  // something an operator watches a drain through.
  const steps = useMemo(
    () => (state.status === "ready" ? (state.plan.plan.steps ?? []) : []),
    [state],
  );

  // A finished run has changed the cluster, so the plan on screen is stale.
  // Reloading is the honest response — anything else leaves an operator acting
  // on a picture that no longer matches their cluster.
  const run = useRun(state.status === "ready" ? state.reload : undefined);

  const greenSteps = useMemo(() => steps.filter((s) => s.impact === "Green"), [steps]);

  const pickedSteps = useMemo(
    () => steps.filter((s) => checked.has(s.sequenceNumber)),
    [steps, checked],
  );

  // Shift-click extends from the last tick, the way a file list does. Ranges
  // are the common case here: "steps 4 through 9" is how a plan gets read.
  const toggleStep = useCallback(
    (seq: number, shiftKey: boolean) => {
      setChecked((prev) => {
        const next = new Set(prev);
        const anchor = lastToggled.current;
        if (shiftKey && anchor !== null) {
          const [lo, hi] = anchor < seq ? [anchor, seq] : [seq, anchor];
          for (const s of steps) {
            if (s.sequenceNumber >= lo && s.sequenceNumber <= hi && s.impact !== "Red") {
              next.add(s.sequenceNumber);
            }
          }
        } else if (next.has(seq)) {
          next.delete(seq);
        } else {
          next.add(seq);
        }
        return next;
      });
      lastToggled.current = seq;
    },
    [steps],
  );

  // An explicit selection wins; otherwise the button means "the safe ones".
  const requestRun = useCallback(
    (dryRun: boolean) =>
      setPending({ steps: pickedSteps.length > 0 ? pickedSteps : greenSteps, dryRun }),
    [greenSteps, pickedSteps],
  );

  const confirmRun = useCallback(() => {
    if (!pending || !planId) return;
    void run.start(
      planId,
      pending.steps.map((s) => s.sequenceNumber),
      pending.dryRun,
    );
    setPending(null);
    if (!pending.dryRun) setChecked(new Set());
  }, [pending, planId, run]);

  const busy = run.state.status === "starting" || run.state.status === "active";

  // Keep the pin in step with the run's lifetime.
  useEffect(() => setRunActive(busy), [busy]);

  // A silently renewed ID token has to replace the one requests are carrying,
  // or the session expires mid-consolidation despite having been refreshed.
  useEffect(() => {
    let stop = () => {};
    void authInfo().then(async (i) => {
      stop = await onRenewed(i, (idToken) => tokenStore.set(idToken));
    });
    return () => stop();
  }, []);

  const handleSelectStep = useCallback((seq: number | null) => {
    setSelectedStep(seq);
    // Selecting a step moves the field to the moment just before it runs, so
    // the pods it is about to move are still on the node being drained.
    if (seq != null) {
      setPlaying(false);
      setStep(seq - 1);
    }
  }, []);

  const handleSelectNode = useCallback((name: string | null) => {
    setSelection(name ? { kind: "node", name } : null);
  }, []);

  const handleSelectPod = useCallback((key: string | null) => {
    setSelection(key ? { kind: "pod", key } : null);
  }, []);

  const selectedNode = selection?.kind === "node" ? selection.name : null;
  const selectedPod = selection?.kind === "pod" ? selection.key : null;

  return (
    <div className="shell">
      <AppBar
        clusterLabel={server?.clusterLabel}
        identity={server?.identity}
        onSignOut={handleSignOut}
      />
      {state.status === "loading" && <Placeholder title="Reading the cluster…" />}

      {state.status === "empty" && (
        <Placeholder
          title="No plan yet"
          detail="The planner publishes one once it has read the cluster."
          /* The cause an operator hits and cannot otherwise guess: the planner
             refuses to touch a node younger than minNodeAge, ten minutes by
             default, so a freshly built cluster shows nothing and looks
             broken. Naming it here is the difference between waiting and
             filing a bug. */
          hint="On a cluster built in the last few minutes this is expected — the planner will not consider a node younger than 10 minutes."
        />
      )}

      {state.status === "error" && state.needsAuth && <SignIn onDone={state.reload} />}

      {state.status === "error" && !state.needsAuth && (
        <Placeholder
          title={state.grantWith ? "Not permitted" : "Cannot reach the planner"}
          detail={
            state.grantWith ? `${state.message}\n\nGrant it with:\n${state.grantWith}` : state.message
          }
          tone="error"
        />
      )}

      {state.status === "ready" && (
        <>
          <Verdict
            stats={state.graph.stats}
            steps={steps}
            generatedAt={state.plan.storedAt}
            focusedRating={focusedRating}
            onFocusRating={setFocusedRating}
            onRun={requestRun}
            busy={busy}
            picked={pickedSteps}
            onClearPicked={() => setChecked(new Set())}
          />

          {state.superseded && (
            <div className="supersede" role="status">
              <span>
                The planner has published a newer plan. This one is pinned while you have a
                selection or a run in progress.
              </span>
              <button className="btn" onClick={state.showLatest}>
                Show the new plan
              </button>
            </div>
          )}

          <RunTrail state={run.state} onDismiss={run.dismiss} />

          <main className="workspace">
            <PackingField
              awaiting={reclaimed.awaiting}
              reclaimedForReal={reclaimed.reclaimed}
              graph={state.graph}
              steps={steps}
              step={step}
              selectedStep={selectedStep}
              selectedNode={selectedNode}
              selectedPod={selectedPod}
              onSelectNode={handleSelectNode}
              onSelectPod={handleSelectPod}
            />

            <aside className="sidebar">
              <StepLedger
                steps={steps}
                selected={selectedStep}
                current={step}
                focusedRating={focusedRating}
                onSelect={handleSelectStep}
                checked={checked}
                onToggle={toggleStep}
              />
              <Inspector
                onSelectPod={(key) => setSelection({ kind: "pod", key })}
                onSelectNode={(name) => setSelection({ kind: "node", name })}
                planId={planId ?? ""}
                graph={state.graph}
                steps={steps}
                selection={selection}
                onClose={() => setSelection(null)}
                onSelectStep={handleSelectStep}
              />
            </aside>
          </main>

          <Scrubber
            steps={steps}
            step={step}
            playing={playing}
            onStep={setStep}
            onPlayingChange={setPlaying}
            onSelect={setSelectedStep}
          />

          <footer className="statusbar mono">
            <span>{state.plan.plan.id}</span>
            <span>{state.plan.strategy}</span>
            <span>{new Date(state.plan.plan.generatedAt).toLocaleTimeString()}</span>
            {kagentUrl && (
              <a className="statusbar-link" href={kagentUrl} target="_blank" rel="noreferrer">
                ask the agent ↗
              </a>
            )}
          </footer>
        </>
      )}

      {pending && (
        <ConfirmRun
          steps={pending.steps}
          dryRun={pending.dryRun}
          onConfirm={confirmRun}
          onCancel={() => setPending(null)}
        />
      )}
    </div>
  );
}

/**
 * An empty screen is an invitation to act, and an error is a thing to fix.
 *
 * Both used to be a heading and a sentence. Neither said what to do next, in a
 * product where "no plan yet" almost always has a cause the operator can
 * either fix or wait out — if someone tells them which.
 */
function Placeholder({
  title,
  detail,
  hint,
  tone = "muted",
}: {
  title: string;
  detail?: string;
  /** The cause, or the next action. Quieter than detail, and often the only
   *  part that actually helps. */
  hint?: string;
  tone?: "muted" | "error";
}) {
  return (
    <div className={`placeholder placeholder-${tone}`}>
      <h2>{title}</h2>
      {detail && <p>{detail}</p>}
      {hint && <p className="placeholder-hint">{hint}</p>}
    </div>
  );
}
