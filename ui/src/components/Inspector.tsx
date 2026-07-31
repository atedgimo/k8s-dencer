import { GraphPayload, PlanStep, formatBytes, formatCPU } from "../api";
import { usePodConstraints } from "../useConstraints";
import { ImpactChip } from "./Impact";

export type Selection =
  | { kind: "pod"; key: string }
  | { kind: "node"; name: string }
  | null;

interface Props {
  planId: string;
  graph: GraphPayload;
  steps: PlanStep[];
  selection: Selection;
  onClose: () => void;
  onSelectStep: (seq: number) => void;
  onSelectPod: (key: string) => void;
  onSelectNode: (name: string) => void;
}

/**
 * Constraint inspector.
 *
 * Every explanation shown here is the string the constraint analyzer produced,
 * rendered verbatim. The UI deliberately does not paraphrase or re-derive
 * them: the Kagent agent answers from the same text, and an inspector that
 * worded things differently would leave an operator unsure which to believe.
 */
export default function Inspector({
  planId,
  graph,
  steps,
  selection,
  onClose,
  onSelectStep,
  onSelectPod,
  onSelectNode,
}: Props) {
  if (!selection) return null;

  // Where a pod lives, so a pod opened from a node offers a way back up. The
  // graph carries it as the parent; in density mode there are no pod elements
  // and the answer is simply unavailable, which is why this is optional.
  const parentNode =
    selection.kind === "pod"
      ? graph.elements
          .find((e) => e.data.kind === "pod" && `${e.data.namespace}/${e.data.label}` === selection.key)
          ?.data.parent?.replace(/^node:/, "")
      : undefined;

  return (
    <section className="inspector" aria-label="Constraint inspector">
      <header className="inspector-header">
        <h2>{selection.kind === "pod" ? "Pod" : "Node"}</h2>
        <button type="button" className="inspector-close" onClick={onClose} aria-label="Close inspector">
          ✕
        </button>
      </header>

      {/* Drilling down without a way back is a trapdoor. */}
      {parentNode && (
        <button type="button" className="inspector-up mono" onClick={() => onSelectNode(parentNode)}>
          ← {parentNode}
        </button>
      )}

      {/* Keyed so React remounts on every change: the panel animates in, and
          without a key the content would swap silently inside a box that never
          moved, which reads as nothing having happened. */}
      <div className="inspector-swap" key={selection.kind === "pod" ? selection.key : selection.name}>
        {selection.kind === "pod" ? (
          <PodPanel planId={planId} podKey={selection.key} />
        ) : (
          <NodePanel
            graph={graph}
            steps={steps}
            name={selection.name}
            onSelectStep={onSelectStep}
            onSelectPod={onSelectPod}
          />
        )}
      </div>
    </section>
  );
}

function PodPanel({ planId, podKey }: { planId: string; podKey: string }) {
  const state = usePodConstraints(planId, podKey);

  return (
    <div className="inspector-body">
      <div className="inspector-title">{podKey}</div>

      {state.status === "loading" && <p className="muted">Loading constraints…</p>}
      {state.status === "missing" && (
        <p className="muted">This pod is not part of the displayed plan's snapshot.</p>
      )}
      {state.status === "error" && <p className="error-text">{state.message}</p>}

      {state.status === "ready" && (
        <>
          <dl className="facts">
            <dt>Node</dt>
            <dd>{state.constraints.nodeName || "unscheduled"}</dd>
            <dt>Movable</dt>
            <dd>
              {state.constraints.movable ? (
                "Yes"
              ) : (
                <span className="flag-blocked">✕ Blocked</span>
              )}
            </dd>
            <dt>Can move to</dt>
            <dd>
              {state.constraints.movable
                ? `${state.constraints.candidateNodes?.length ?? 0} node(s)`
                : "—"}
            </dd>
          </dl>

          <h3 className="inspector-subhead">
            Effective constraints
            <span className="inspector-count">{state.constraints.constraints.length}</span>
          </h3>

          <ul className="constraint-list">
            {state.constraints.constraints.map((c, i) => (
              <li key={`${c.kind}-${c.subject ?? i}`} className={c.blocking ? "constraint blocking" : "constraint"}>
                <div className="constraint-head">
                  {/* Blocking status carries a glyph as well as colour. */}
                  <span className="constraint-kind">
                    {c.blocking && <span aria-hidden="true">✕ </span>}
                    {c.kind}
                  </span>
                  <span className={`constraint-tag${c.hard ? " hard" : ""}`}>
                    {c.hard ? "hard" : "preference"}
                  </span>
                </div>
                {c.subject && <div className="constraint-subject">{c.subject}</div>}
                <p className="constraint-text">{c.explanation}</p>
              </li>
            ))}
            {state.constraints.constraints.length === 0 && (
              <li className="muted">No constraints apply to this pod.</li>
            )}
          </ul>
        </>
      )}
    </div>
  );
}

/**
 * Node detail is derived from the graph payload already in memory rather than
 * fetched. Everything shown — occupancy, utilisation, which step drains it —
 * is in the payload, so a round trip would only add latency.
 */
function NodePanel({
  graph,
  steps,
  name,
  onSelectStep,
  onSelectPod,
}: {
  graph: GraphPayload;
  steps: PlanStep[];
  name: string;
  onSelectStep: (seq: number) => void;
  onSelectPod: (key: string) => void;
}) {
  const node = graph.elements.find((e) => e.data.kind === "node" && e.data.label === name)?.data;
  const pods = graph.elements.filter(
    (e) => e.data.kind === "pod" && e.data.parent === `node:${name}`,
  );
  const drainStep = node?.drainStep ?? 0;
  const step = steps.find((s) => s.sequenceNumber === drainStep);
  const blocked = pods.filter((p) => p.data.blocked);

  // Above the graph endpoint's detail limit there are no pod elements at all,
  // so counting them would report every node as empty — a node holding 40 pods
  // rendering as "0 pods" reads as a successful drain rather than a missing
  // payload. The server sends the tallies for exactly this case.
  const podCount = graph.aggregated ? (node?.podCount ?? 0) : pods.length;
  const blockedCount = graph.aggregated ? (node?.blockedCount ?? 0) : blocked.length;

  if (!node) return <div className="inspector-body muted">Node not found in this plan.</div>;

  const util = node.cpuAllocatable ? (node.cpuRequested ?? 0) / node.cpuAllocatable : 0;

  return (
    <div className="inspector-body">
      <div className="inspector-title">{name}</div>

      <dl className="facts">
        <dt>Zone</dt>
        <dd>{node.zone || "—"}</dd>
        {/* The machine, priced in kind if not in money. Absent on fixtures
            and bare metal — shown only when the platform actually said. */}
        {node.instanceType && (
          <>
            <dt>Machine</dt>
            <dd>
              {node.instanceType}
              {node.capacityType && <span className="mono"> · {node.capacityType}</span>}
            </dd>
          </>
        )}
        <dt>CPU</dt>
        <dd>
          {formatCPU(node.cpuRequested ?? 0)} / {formatCPU(node.cpuAllocatable ?? 0)} cores (
          {Math.round(util * 100)}%)
        </dd>
        <dt>Memory</dt>
        <dd>
          {formatBytes(node.memRequested ?? 0)} / {formatBytes(node.memAllocatable ?? 0)}
        </dd>
        <dt>Pods</dt>
        <dd>{podCount}</dd>
      </dl>

      {/* The list, not just the count. Without it a node is a dead end: you can
          see that four pods are here and have no way to ask about any of them,
          which is the question the count immediately provokes. */}
      {pods.length > 0 && (
        <div className="podlist">
          <h3 className="eyebrow podlist-head">Pods on this node</h3>
          <ul className="podlist-items">
            {pods.map((p, i) => {
              const key = `${p.data.namespace}/${p.data.label}`;
              return (
                <li key={key}>
                  <button
                    type="button"
                    className="podlist-row"
                    /* Staggered so the list assembles rather than appearing,
                       which makes it read as a consequence of the click. */
                    style={{ "--i": i } as React.CSSProperties}
                    onClick={() => onSelectPod(key)}
                  >
                    <span className="podlist-name mono">{p.data.label}</span>
                    <span className="podlist-ns mono">{p.data.namespace}</span>
                    {p.data.blocked ? (
                      <span className="podlist-flag podlist-flag-blocked mono">■ blocked</span>
                    ) : p.data.movable === false ? (
                      <span className="podlist-flag mono">pinned</span>
                    ) : (
                      <span className="podlist-flag podlist-flag-ok mono">movable</span>
                    )}
                  </button>
                </li>
              );
            })}
          </ul>
        </div>
      )}

      {graph.aggregated && podCount > 0 && (
        <p className="podlist-elided">
          {podCount} pods on this node. Individual pods are not listed above the
          graph endpoint&rsquo;s detail limit — at that size the payload would be
          tens of megabytes.
        </p>
      )}

      {drainStep > 0 && step ? (
        <div className="inspector-callout">
          <div className="callout-head">
            <span>Drained by step {drainStep}</span>
            <ImpactChip impact={step.impact} compact />
          </div>
          <p className="constraint-text">{step.rationale}</p>
          <button type="button" className="linkish" onClick={() => onSelectStep(drainStep)}>
            Show this step →
          </button>
        </div>
      ) : (
        <div className="inspector-callout">
          <div className="callout-head">
            <span>Not drained by this plan</span>
          </div>
          <p className="constraint-text">
            {blockedCount > 0
              ? `${blockedCount} pod(s) here cannot be evicted, so the node cannot be emptied. Select one above to see why.`
              : "This node is either a destination for other pods, excluded by policy, or already reclaimable."}
          </p>
        </div>
      )}
    </div>
  );
}
