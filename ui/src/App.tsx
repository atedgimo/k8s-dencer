import { useCallback, useState } from "react";
import { Impact } from "./api";
import Inspector, { Selection } from "./components/Inspector";
import PackingField from "./components/PackingField";
import Scrubber from "./components/Scrubber";
import { SignIn } from "./components/SignIn";
import StepLedger from "./components/StepLedger";
import Verdict from "./components/Verdict";
import { runtimeConfig } from "./runtime-config";
import { usePlan } from "./usePlan";

export default function App() {
  const state = usePlan();
  const { kagentUrl } = runtimeConfig();

  const [step, setStep] = useState(0);
  const [playing, setPlaying] = useState(false);
  const [selectedStep, setSelectedStep] = useState<number | null>(null);
  const [selection, setSelection] = useState<Selection>(null);
  const [focusedRating, setFocusedRating] = useState<Impact | null>(null);

  const planId = state.status === "ready" ? state.plan.plan.id : null;

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
            steps={state.plan.plan.steps}
            focusedRating={focusedRating}
            onFocusRating={setFocusedRating}
          />

          <main className="workspace">
            <PackingField
              graph={state.graph}
              steps={state.plan.plan.steps}
              step={step}
              selectedStep={selectedStep}
              selectedNode={selectedNode}
              selectedPod={selectedPod}
              onSelectNode={handleSelectNode}
              onSelectPod={handleSelectPod}
            />

            <aside className="sidebar">
              <StepLedger
                steps={state.plan.plan.steps}
                selected={selectedStep}
                current={step}
                focusedRating={focusedRating}
                onSelect={handleSelectStep}
              />
              <Inspector
                planId={planId ?? ""}
                graph={state.graph}
                steps={state.plan.plan.steps}
                selection={selection}
                onClose={() => setSelection(null)}
                onSelectStep={handleSelectStep}
              />
            </aside>
          </main>

          <Scrubber
            steps={state.plan.plan.steps}
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
