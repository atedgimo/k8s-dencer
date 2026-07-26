import { useCallback, useEffect, useState } from "react";
import CapacityRibbon from "./components/CapacityRibbon";
import Inspector, { Selection } from "./components/Inspector";
import PlanCanvas from "./components/PlanCanvas";
import Scrubber from "./components/Scrubber";
import StatTiles from "./components/StatTiles";
import StepList from "./components/StepList";
import { runtimeConfig } from "./runtime-config";
import { usePlan } from "./usePlan";

export default function App() {
  const state = usePlan();
  const { kagentUrl } = runtimeConfig();

  const [step, setStep] = useState(0);
  const [playing, setPlaying] = useState(false);
  const [selectedStep, setSelectedStep] = useState<number | null>(null);
  const [selection, setSelection] = useState<Selection>(null);

  const planId = state.status === "ready" ? state.plan.plan.id : null;

  // A new plan invalidates the scrubber position: step 7 of the old plan is
  // not step 7 of the new one.
  useEffect(() => {
    setStep(0);
    setPlaying(false);
    setSelectedStep(null);
    setSelection(null);
  }, [planId]);

  const handleSelectPod = useCallback((key: string | null) => {
    setSelection(key ? { kind: "pod", key } : null);
  }, []);

  const handleSelectNode = useCallback((name: string | null) => {
    setSelection(name ? { kind: "node", name } : null);
  }, []);

  const handleSelectStep = useCallback((seq: number | null) => {
    setSelectedStep(seq);
    // Selecting a step moves the canvas to the moment just before it runs, so
    // the highlighted pods are still on the node being drained.
    if (seq != null) {
      setPlaying(false);
      setStep(seq - 1);
    }
  }, []);

  return (
    <div className="shell">
      <header className="topbar">
        <div className="brand">
          <h1>k8s-dencer</h1>
          <span className="tagline">Capacity plan</span>
        </div>
        <div className="topbar-right">
          <span className="badge" title="This release plans and explains. It never drains, cordons or evicts.">
            Plans only
          </span>
          {kagentUrl && (
            <a className="link" href={kagentUrl} target="_blank" rel="noreferrer">
              Ask the agent →
            </a>
          )}
        </div>
      </header>

      {state.status === "loading" && <Placeholder title="Reading the cluster…" />}

      {state.status === "empty" && (
        <Placeholder
          title="No plan yet"
          detail="The planner publishes one once it has read the cluster. This takes a few seconds after install."
        />
      )}

      {state.status === "error" && (
        <Placeholder title="Cannot reach the planner" detail={state.message} tone="error" />
      )}

      {state.status === "ready" && (
        <>
          <CapacityRibbon
            graph={state.graph}
            steps={state.plan.plan.steps}
            step={step}
            onSelectNode={handleSelectNode}
          />
          <StatTiles stats={state.graph.stats} />

          <main className="workspace">
            <PlanCanvas
              graph={state.graph}
              steps={state.plan.plan.steps}
              step={step}
              selectedStep={selectedStep}
              onSelectStep={handleSelectStep}
              onSelectPod={handleSelectPod}
              onSelectNode={handleSelectNode}
            />
            <div className="sidebar">
              <StepList
                steps={state.plan.plan.steps}
                appliedThrough={step}
                selected={selectedStep}
                onSelect={handleSelectStep}
              />
              <Inspector
                planId={state.plan.plan.id}
                graph={state.graph}
                steps={state.plan.plan.steps}
                selection={selection}
                onClose={() => setSelection(null)}
                onSelectStep={handleSelectStep}
              />
            </div>
          </main>

          <Scrubber
            steps={state.plan.plan.steps}
            step={step}
            onStep={setStep}
            playing={playing}
            onPlayingChange={setPlaying}
          />

          <footer className="statusbar">
            <span>
              plan <code>{state.plan.plan.id}</code>
            </span>
            <span>{state.plan.strategy}</span>
            <span>{new Date(state.plan.plan.generatedAt).toLocaleTimeString()}</span>
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
