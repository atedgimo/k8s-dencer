import { useCallback, useMemo, useRef, useState } from "react";
import { Impact, PlanStep } from "./api";
import Inspector, { Selection } from "./components/Inspector";
import PackingField from "./components/PackingField";
import { ConfirmRun, RunTrail } from "./components/RunPanel";
import Scrubber from "./components/Scrubber";
import { SignIn } from "./components/SignIn";
import StepLedger from "./components/StepLedger";
import Verdict from "./components/Verdict";
import { runtimeConfig } from "./runtime-config";
import { usePlan } from "./usePlan";
import { useRun } from "./useRun";

export default function App() {
  const state = usePlan();
  const { kagentUrl } = runtimeConfig();

  const [step, setStep] = useState(0);
  const [playing, setPlaying] = useState(false);
  const [selectedStep, setSelectedStep] = useState<number | null>(null);
  const [selection, setSelection] = useState<Selection>(null);
  const [focusedRating, setFocusedRating] = useState<Impact | null>(null);
  const [pending, setPending] = useState<{ steps: PlanStep[]; dryRun: boolean } | null>(null);
  const [checked, setChecked] = useState<Set<number>>(new Set());
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
      {state.status === "loading" && <Placeholder title="Reading the cluster…" />}

      {state.status === "empty" && (
        <Placeholder
          title="No plan yet"
          detail="The planner publishes one once it has read the cluster. This takes a few seconds after install."
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
            focusedRating={focusedRating}
            onFocusRating={setFocusedRating}
            onRun={requestRun}
            busy={busy}
            picked={pickedSteps}
            onClearPicked={() => setChecked(new Set())}
          />

          <RunTrail state={run.state} onDismiss={run.dismiss} />

          <main className="workspace">
            <PackingField
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

function Placeholder({
  title,
  detail,
  tone = "muted",
}: {
  title: string;
  detail?: string;
  tone?: "muted" | "error";
}) {
  return (
    <div className={`placeholder placeholder-${tone}`}>
      <h2>{title}</h2>
      {detail && <p>{detail}</p>}
    </div>
  );
}
