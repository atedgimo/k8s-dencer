import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Impact, PlanStep } from "./api";
import { ConfirmConverge, ConfirmRun, RunTrail } from "./components/RunPanel";
import { SignIn } from "./components/SignIn";
import ClusterPage from "./components/cluster/ClusterPage";
import Rail from "./components/Rail";
import TopBar from "./components/TopBar";
import History from "./components/History";
import RecsPage from "./components/recs/RecsPage";
import { findingKey, useMuted } from "./useMuted";
import Hero from "./components/review/Hero";
import StepList from "./components/review/StepList";
import StepDetail from "./components/review/StepDetail";
import ReviewFooter from "./components/review/ReviewFooter";
import RunScreen from "./components/run/RunScreen";
import { FieldView, Surface, defaultView, rememberView, storedView } from "./view";
import { authInfo, readOnlySession, token as tokenStore } from "./auth";
import { onRenewed } from "./oidc";
import { runtimeConfig } from "./runtime-config";
import { useObserved } from "./useObserved";
import { usePlan } from "./usePlan";
import { useReclamations } from "./useReclamations";
import ResiliencePage from "./components/resilience/ResiliencePage";
import RightsizingPage from "./components/rightsizing/RightsizingPage";
import WhyNothing from "./components/review/WhyNothing";
import { useRecommendations } from "./useRecommendations";
import { useRun } from "./useRun";
import { useVersion } from "./useVersion";

export default function App() {
  // Pinned while there is a selection to PROTECT or a run to watch. Step
  // numbers are positional, so a plan swapped underneath a ticked selection
  // would leave it meaning different nodes. Since the redesign checks the
  // safe steps by default, "a selection exists" is no longer the signal —
  // only a selection the operator has touched is theirs to protect; the
  // pristine default re-derives happily on every new plan.
  const [checked, setChecked] = useState<Set<number>>(new Set());
  const touched = useRef(false);
  const [runActive, setRunActive] = useState(false);
  const state = usePlan((touched.current && checked.size > 0) || runActive);
  const { kagentUrl } = runtimeConfig();

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
  const [focusedRating, setFocusedRating] = useState<Impact | null>(null);
  const [pending, setPending] = useState<{ steps: PlanStep[]; dryRun: boolean } | null>(null);
  const [convergeOpen, setConvergeOpen] = useState(false);
  // Which destination is showing. Deliberately not persisted: the others are
  // places you visit, and reopening the app anywhere but the plan would bury
  // the thing the product is for.
  const [surface, setSurface] = useState<Surface>("review");
  const lastToggled = useRef<number | null>(null);

  const recommendations = useRecommendations();
  const { muted, mute, unmute } = useMuted();

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

  // Safe rows are checked by default (the design's "one primary action"
  // depends on the button meaning something before anyone clicks a box).
  // Only an untouched selection is re-derived; the moment the operator
  // expresses intent, the selection is theirs and pins the plan.
  useEffect(() => {
    if (planId != null && !touched.current) {
      setChecked(new Set(greenSteps.map((s) => s.sequenceNumber)));
    }
  }, [planId, greenSteps]);

  const pickedSteps = useMemo(
    () => steps.filter((s) => checked.has(s.sequenceNumber)),
    [steps, checked],
  );

  // Shift-click extends from the last tick, the way a file list does. Ranges
  // are the common case here: "steps 4 through 9" is how a plan gets read.
  const toggleStep = useCallback(
    (seq: number, shiftKey: boolean) => {
      touched.current = true;
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
    if (!pending.dryRun) {
      // The run consumes the selection; the next plan re-derives the default.
      touched.current = false;
      setChecked(new Set());
    }
  }, [pending, planId, run]);

  const busy = run.state.status === "starting" || run.state.status === "active";

  // The read-only session hides drain affordances; RBAC enforces regardless.
  const readOnly = readOnlySession.get();

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
  }, []);

  // The pane opens on the step most worth reading: the first judgement call,
  // or failing that the first step. An empty pane on a screen whose job is
  // explaining steps would waste the 392px.
  useEffect(() => {
    if (planId == null || steps.length === 0) return;
    setSelectedStep((cur) => {
      if (cur != null && steps.some((s) => s.sequenceNumber === cur)) return cur;
      const firstCall = steps.find((s) => s.impact === "Yellow");
      return (firstCall ?? steps[0]).sequenceNumber;
    });
  }, [planId, steps]);

  // Node count drives the default lens: individual pods stop being worth
  // drawing long before a vessel per node does.
  const nodeCount = state.status === "ready" ? (state.graph.elements.filter((e) => e.data.kind === "node").length) : 0;
  const view: FieldView = viewPref ?? defaultView(nodeCount);

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

  // The rail badge counts the queue, not the raw findings: a muted finding
  // left the queue, and a badge that disagreed with the page it opens would
  // read as the tool arguing with itself.
  const highFindings = (recommendations ?? []).filter(
    (r) => r.severity === "high" && !muted.has(findingKey(r.kind, r.workload)),
  ).length;

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
        runNote={
          run.state.status === "active"
            ? {
                label: run.state.run.dryRun ? "Rehearsing" : "Run in progress",
                value: `${run.state.events.filter((e) => e.action === "Drained").length} of ${run.state.run.steps.length} steps done`,
              }
            : run.state.status === "done" && run.state.run.status === "Blocked"
              ? { label: "Run halted", value: "the guard refused a step" }
              : undefined
        }
      />

      <div className="frame-main">
        <TopBar
          planId={planId}
          strategy={state.status === "ready" ? state.plan.strategy : undefined}
          confirmedAt={confirmedAt}
          stale={state.status === "ready" && state.superseded}
          onRecompute={
            state.status === "ready"
              ? () => {
                  // A recompute hands the screen to a fresh plan; the default
                  // selection re-derives rather than surviving as a ghost.
                  touched.current = false;
                  (state.superseded ? state.showLatest : state.reload)();
                }
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

          {state.status === "error" && state.needsAuth && (
            <SignIn onDone={state.reload} clusterLabel={server?.clusterLabel} />
          )}

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

          {state.status === "ready" &&
            surface === "review" &&
            (run.state.status === "active" || run.state.status === "done") && (
              // A run exists: the Review destination IS the run — rehearsal
              // result, execution in progress, or halted by the guard.
              <RunScreen
                run={run.state.run}
                events={run.state.events}
                active={run.state.status === "active"}
                graph={state.graph}
                steps={steps}
                planMatches={run.state.run.planId === state.plan.plan.id}
                readOnly={readOnly}
                reclaimed={new Map(reclamations.recent.map((r) => [r.node, r]))}
                onDismiss={run.dismiss}
                onRehearse={() => requestRun(true)}
                onDrain={() => requestRun(false)}
                onRecompute={() => {
                  touched.current = false;
                  run.dismiss();
                  (state.superseded ? state.showLatest : state.reload)();
                }}
                onOpenRecommendations={() => setSurface("recommendations")}
              />
            )}

          {state.status === "ready" &&
            surface === "review" &&
            run.state.status !== "active" &&
            run.state.status !== "done" && (
            <>
              <Hero
                graph={state.graph}
                steps={steps}
                focusedRating={focusedRating}
                onFocusRating={setFocusedRating}
              />

              <RunTrail state={run.state} onDismiss={run.dismiss} />

              {steps.length === 0 && <WhyNothing graph={state.graph} />}

              <main className="review-main">
                <StepList
                  steps={steps}
                  graph={state.graph}
                  checked={checked}
                  onToggle={toggleStep}
                  focused={selectedStep}
                  onFocus={handleSelectStep}
                  filter={focusedRating}
                  onFilter={setFocusedRating}
                />
                <StepDetail
                  planId={state.plan.plan.id}
                  step={steps.find((s) => s.sequenceNumber === selectedStep) ?? null}
                  pool={poolOf(state.graph, steps.find((s) => s.sequenceNumber === selectedStep))}
                  checked={selectedStep != null && checked.has(selectedStep)}
                  stale={state.superseded}
                  onAdd={(seq) => toggleStep(seq, false)}
                  onSkip={(seq) => toggleStep(seq, false)}
                />
              </main>

              <ReviewFooter
                planId={state.plan.plan.id}
                picked={pickedSteps}
                readOnly={readOnly}
                stale={state.superseded}
                busy={busy}
                onRehearse={() => requestRun(true)}
                onDrain={() => requestRun(false)}
                onDrainConfirmed={() => {
                  // The typed amber gate WAS the confirmation; a second sheet
                  // on top of it would teach people to click through sheets.
                  if (!planId) return;
                  void run.start(
                    planId,
                    pickedSteps.map((s) => s.sequenceNumber),
                    false,
                  );
                  touched.current = false;
                  setChecked(new Set());
                }}
                onConverge={() => setConvergeOpen(true)}
              />
            </>
          )}

          {state.status === "ready" && surface === "cluster" && (
            <ClusterPage
              graph={state.graph}
              steps={steps}
              lens={view}
              onLens={(v) => {
                setViewPref(v);
                rememberView(v);
              }}
              selectedStep={selectedStep}
              onSelectStep={handleSelectStep}
              observed={observed.nodes}
              evictedPods={observed.evictedPods}
            />
          )}

          {state.status === "ready" && surface === "recommendations" && (
            <RecsPage
              recs={recommendations}
              steps={steps}
              graph={state.graph}
              muted={muted}
              onMute={mute}
              onUnmute={unmute}
              onOpenSteps={(seqs) => {
                setSurface("review");
                setFocusedRating(null);
                handleSelectStep(seqs[0] ?? null);
              }}
            />
          )}

          {state.status === "ready" && surface === "resilience" && <ResiliencePage />}

          {state.status === "ready" && surface === "rightsizing" && <RightsizingPage />}

          {state.status === "ready" && surface === "history" && <History pricing={reclamations.stats.pricing} />}
        </div>

        {/* Review carries its own action footer; the plan-identity strip
            serves the destinations that do not. */}
        {state.status === "ready" && surface !== "review" && (
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

/** The focused step's pool chip, joined from the graph's node metadata. */
function poolOf(
  graph: {
    elements: Array<{
      data: { kind: string; label: string; pool?: string; instanceType?: string; capacityType?: string };
    }>;
  },
  step?: PlanStep,
): string | undefined {
  if (!step?.targetNode) return undefined;
  const n = graph.elements.find((e) => e.data.kind === "node" && e.data.label === step.targetNode);
  // The pool is the thing an operator recognises and the thing that scales;
  // the machine shape is the fallback when the provider names no pool.
  return n?.data.pool || n?.data.instanceType || n?.data.capacityType || undefined;
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
