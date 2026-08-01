import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Impact, PlanStep } from "./api";
import Inspector, { Selection } from "./components/Inspector";
import PackingField from "./components/PackingField";
import { ConfirmConverge, ConfirmRun, RunTrail } from "./components/RunPanel";
import Scrubber from "./components/Scrubber";
import { SignIn } from "./components/SignIn";
import StepLedger from "./components/StepLedger";
import Verdict from "./components/Verdict";
import Rail from "./components/Rail";
import TopBar from "./components/TopBar";
import History from "./components/History";
import Recommendations from "./components/Recommendations";
import { FieldView, Surface, VIEW_LABELS, defaultView, rememberView, storedView } from "./view";
import { authInfo, token as tokenStore } from "./auth";
import { onRenewed } from "./oidc";
import { runtimeConfig } from "./runtime-config";
import { useObserved } from "./useObserved";
import { usePlan } from "./usePlan";
import { useReclamations } from "./useReclamations";
import { useRecommendations } from "./useRecommendations";
import { useRun } from "./useRun";
import { useVersion } from "./useVersion";

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

  const reclamations = useReclamations();

  const server = useVersion();

  // null means "follow cluster size", which is right until someone disagrees.
  const [viewPref, setViewPref] = useState<FieldView | null>(storedView);

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

  const [selectedStep, setSelectedStep] = useState<number | null>(null);
  const [selection, setSelection] = useState<Selection>(null);
  const [focusedRating, setFocusedRating] = useState<Impact | null>(null);
  const [pending, setPending] = useState<{ steps: PlanStep[]; dryRun: boolean } | null>(null);
  const [convergeOpen, setConvergeOpen] = useState(false);
  // Which destination is showing. Deliberately not persisted: the others are
  // places you visit, and reopening the app anywhere but the plan would bury
  // the thing the product is for.
  const [surface, setSurface] = useState<Surface>("review");
  const lastToggled = useRef<number | null>(null);

  const recommendations = useRecommendations();

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

  const observed = useObserved(reclamations, run.state);

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

  // Node count drives the default: individual pods stop being worth drawing
  // long before a vessel per node does.
  const nodeCount = state.status === "ready" ? (state.graph.elements.filter((e) => e.data.kind === "node").length) : 0;
  const view: FieldView = viewPref ?? defaultView(nodeCount);

  const selectedNode = selection?.kind === "node" ? selection.name : null;
  const selectedPod = selection?.kind === "pod" ? selection.key : null;

  // The polled confirmation, but only when it is about the plan on screen.
  // While the view is pinned the server's latest is a *different* plan, and
  // dating this one by that one's confirmation would age a held plan by
  // someone else's clock.
  const confirmedAt =
    state.status === "ready"
      ? server?.latestPlanId === state.plan.plan.id && server?.planConfirmedAt
        ? server.planConfirmedAt
        : state.plan.storedAt
      : null;

  const highFindings = (recommendations ?? []).filter((r) => r.severity === "high").length;

  const packingField = state.status === "ready" && (
    <PackingField
      view={view}
      awaiting={reclamations.stats.awaiting}
      reclaimedForReal={reclamations.stats.reclaimed}
      ledgerCpuMilli={reclamations.stats.reclaimedCpuMilli}
      ledgerMemBytes={reclamations.stats.reclaimedMemBytes}
      noReclaimerEvidence={reclamations.noReclaimerEvidence}
      observed={observed.nodes}
      evictedPods={observed.evictedPods}
      graph={state.graph}
      steps={steps}
      step={step}
      selectedStep={selectedStep}
      selectedNode={selectedNode}
      selectedPod={selectedPod}
      onSelectNode={handleSelectNode}
      onSelectPod={handleSelectPod}
    />
  );

  const inspector = state.status === "ready" && (
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
  );

  const scrubber = (
    <Scrubber
      steps={steps}
      step={step}
      playing={playing}
      onStep={setStep}
      onPlayingChange={setPlaying}
      onSelect={setSelectedStep}
    />
  );

  return (
    <div className="frame">
      <Rail
        surface={surface}
        onSurface={setSurface}
        stepCount={steps.length}
        highFindings={highFindings}
        clusterLabel={server?.clusterLabel}
        identity={server?.identity}
        onSignOut={handleSignOut}
      />

      <div className="frame-main">
        <TopBar
          planId={planId}
          strategy={state.status === "ready" ? state.plan.strategy : undefined}
          confirmedAt={confirmedAt}
          stale={state.status === "ready" && state.superseded}
          onRecompute={
            state.status === "ready"
              ? state.superseded
                ? state.showLatest
                : state.reload
              : undefined
          }
        />

        <div className="frame-content">
          {state.status === "loading" && <Placeholder title="Reading the cluster…" />}

          {state.status === "empty" && (
            <Placeholder
              title="No plan yet"
              detail="The planner publishes one once it has read the cluster."
              /* The cause an operator hits and cannot otherwise guess: the
                 planner refuses to touch a node younger than minNodeAge, ten
                 minutes by default, so a freshly built cluster shows nothing
                 and looks broken. Naming it here is the difference between
                 waiting and filing a bug. */
              hint="On a cluster built in the last few minutes this is expected — the planner will not consider a node younger than 10 minutes."
            />
          )}

          {state.status === "error" && state.needsAuth && <SignIn onDone={state.reload} />}

          {state.status === "error" && !state.needsAuth && (
            <Placeholder
              title={state.grantWith ? "Not permitted" : "Cannot reach the planner"}
              detail={
                state.grantWith
                  ? `${state.message}\n\nGrant it with:\n${state.grantWith}`
                  : state.message
              }
              tone="error"
            />
          )}

          {state.status === "ready" && surface === "review" && (
            <>
              <Verdict
                stats={state.graph.stats}
                steps={steps}
                confirmedAt={confirmedAt ?? state.plan.storedAt}
                focusedRating={focusedRating}
                onFocusRating={setFocusedRating}
                onRun={requestRun}
                onConverge={() => setConvergeOpen(true)}
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
                {packingField}
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
                  {inspector}
                </aside>
              </main>

              {scrubber}
            </>
          )}

          {state.status === "ready" && surface === "cluster" && (
            <>
              {/* The lenses, until the Cluster destination's own screens land.
                  Rack / Wells / Panel are ways of drawing nodes, so the switch
                  lives with the field rather than in the frame. */}
              <div className="lensbar">
                <div className="viewswitch" role="group" aria-label="Cluster lens">
                  {(Object.keys(VIEW_LABELS) as FieldView[]).map((v) => (
                    <button
                      key={v}
                      type="button"
                      className={"viewswitch-btn" + (v === view ? " is-on" : "")}
                      aria-pressed={v === view}
                      onClick={() => {
                        setViewPref(v);
                        rememberView(v);
                      }}
                    >
                      {VIEW_LABELS[v]}
                    </button>
                  ))}
                </div>
              </div>

              <RunTrail state={run.state} onDismiss={run.dismiss} />

              <main className="workspace">
                {packingField}
                <aside className="sidebar">{inspector}</aside>
              </main>

              {scrubber}
            </>
          )}

          {state.status === "ready" && surface === "recommendations" && (
            <main className="workspace workspace-single">
              <div className="page-recs">
                <Recommendations recs={recommendations} variant="page" />
              </div>
            </main>
          )}

          {state.status === "ready" && surface === "history" && (
            <main className="workspace workspace-single">
              <History />
            </main>
          )}
        </div>

        {state.status === "ready" && (
          <footer className="appfooter mono">
            <span>{state.plan.plan.id}</span>
            <span>{state.plan.strategy}</span>
            <span>{new Date(state.plan.plan.generatedAt).toLocaleTimeString()}</span>
            {kagentUrl && (
              <a className="appfooter-link" href={kagentUrl} target="_blank" rel="noreferrer">
                ask the agent ↗
              </a>
            )}
          </footer>
        )}
      </div>

      {pending && (
        <ConfirmRun
          steps={pending.steps}
          dryRun={pending.dryRun}
          onConfirm={confirmRun}
          onCancel={() => setPending(null)}
        />
      )}
      {convergeOpen && state.status === "ready" && (
        <ConfirmConverge
          planReclaims={state.graph.stats.reclaimable}
          onConfirm={(maxNodes, maxImpact, dryRun) => {
            setConvergeOpen(false);
            void run.startConverge(maxNodes, maxImpact, dryRun);
          }}
          onCancel={() => setConvergeOpen(false)}
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
