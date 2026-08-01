import { useEffect } from "react";
import { GraphPayload, PlanStep, formatBytes, formatCPU } from "../api";
import { usePodConstraints } from "../useConstraints";

/**
 * The pod, in its own box.
 *
 * Pod detail used to live in a corner of the sidebar, squeezed under node
 * affordances it had to share — the back-link overlapped the title, and the
 * facts clipped. A pod click now opens this floating card instead: identity,
 * resources, movement, constraints, each with room.
 *
 * Movement is the point. The plan already knows what it intends for this pod
 * — which step, from where, to where — and the observed overlay knows when a
 * run has actually evicted it. Both are shown, labelled as what they are:
 * the plan's intention is a forecast, and after a real eviction the
 * replacement is a NEW pod under the same workload, so live provenance
 * belongs to the owner, not the name. The card says so rather than
 * pretending pod names survive moves.
 */

interface Props {
  planId: string;
  podKey: string;
  graph: GraphPayload;
  steps: PlanStep[];
  evicted: boolean;
  onClose: () => void;
  onSelectNode: (name: string) => void;
  onSelectStep: (seq: number) => void;
}

export default function PodCard({
  planId,
  podKey,
  graph,
  steps,
  evicted,
  onClose,
  onSelectNode,
  onSelectStep,
}: Props) {
  const state = usePodConstraints(planId, podKey);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  const el = graph.elements.find(
    (e) => e.data.kind === "pod" && `${e.data.namespace}/${e.data.label}` === podKey,
  )?.data;
  const homeNode = el?.parent?.replace(/^node:/, "");

  // The plan's intention for this pod, if it has one.
  const [ns, name] = [podKey.slice(0, podKey.indexOf("/")), podKey.slice(podKey.indexOf("/") + 1)];
  const plannedMove = (() => {
    for (const s of steps) {
      for (const m of s.moves) {
        if (m.namespace === ns && m.pod === name) {
          return { step: s.sequenceNumber, from: m.fromNode, to: m.toNode };
        }
      }
    }
    return null;
  })();

  return (
    <aside className="podcard" role="dialog" aria-label={`Pod ${podKey}`}>
      <header className="podcard-head">
        <div>
          <span className="eyebrow">pod · {ns}</span>
          <h2 className="podcard-name mono">{name}</h2>
        </div>
        <button type="button" className="inspector-close" onClick={onClose} aria-label="Close">
          ✕
        </button>
      </header>

      <dl className="facts">
        <dt>Node</dt>
        <dd>
          {homeNode ? (
            <button type="button" className="podcard-link mono" onClick={() => onSelectNode(homeNode)}>
              {homeNode}
            </button>
          ) : (
            "—"
          )}
        </dd>
        {el?.ownerKind && (
          <>
            <dt>Owner</dt>
            <dd className="mono">
              {el.ownerKind}/{el.ownerName}
            </dd>
          </>
        )}
        {(el?.cpuRequest ?? 0) > 0 || (el?.memRequest ?? 0) > 0 ? (
          <>
            <dt>Requests</dt>
            <dd className="num">
              {formatCPU(el?.cpuRequest ?? 0)} cpu · {formatBytes(el?.memRequest ?? 0)}
            </dd>
          </>
        ) : null}
      </dl>

      {/* Movement: the forecast, and the observed fact when there is one. */}
      <section className="podcard-move">
        <h3 className="inspector-subhead">Movement</h3>
        {evicted && (
          <p className="podcard-observed">
            Evicted by the run in flight. Its replacement is a new pod under{" "}
            {el?.ownerKind ?? "its controller"} — where it landed arrives with the next snapshot.
          </p>
        )}
        {plannedMove ? (
          <button
            type="button"
            className="podcard-planned"
            onClick={() => onSelectStep(plannedMove.step)}
            title="The plan's intention — the scheduler decides on the day. Click to open the step."
          >
            <span className="eyebrow">planned · step {plannedMove.step}</span>
            <span className="mono">
              {plannedMove.from} <span aria-hidden="true">→</span> {plannedMove.to}
            </span>
          </button>
        ) : (
          !evicted && <p className="muted">The current plan leaves this pod where it is.</p>
        )}
      </section>

      {state.status === "loading" && <p className="muted">Loading constraints…</p>}
      {state.status === "missing" && (
        <p className="muted">This pod is not part of the displayed plan's snapshot.</p>
      )}
      {state.status === "error" && <p className="error-text">{state.message}</p>}

      {state.status === "ready" && (
        <>
          <dl className="facts">
            <dt>Movable</dt>
            <dd>
              {state.constraints.movable ? "Yes" : <span className="flag-blocked">✕ Blocked</span>}
            </dd>
            <dt>Can move to</dt>
            <dd>
              {!state.constraints.movable ? (
                "—"
              ) : state.constraints.candidateNodes == null ? (
                <span className="muted">not computed for this snapshot</span>
              ) : state.constraints.candidateNodes.length === 0 ? (
                "no node has room right now"
              ) : (
                <>
                  <span className="num">{state.constraints.candidateNodes.length}</span> node(s)
                  <span className="podcard-candidates mono">
                    {" "}
                    — {state.constraints.candidateNodes.slice(0, 3).join(", ")}
                    {state.constraints.candidateNodes.length > 3 &&
                      ` +${state.constraints.candidateNodes.length - 3} more`}
                  </span>
                </>
              )}
            </dd>
          </dl>

          <h3 className="inspector-subhead">
            Effective constraints
            <span className="inspector-count">{state.constraints.constraints.length}</span>
          </h3>
          <ul className="constraint-list">
            {state.constraints.constraints.map((c, i) => (
              <li
                key={`${c.kind}-${c.subject ?? i}`}
                className={c.blocking ? "constraint blocking" : "constraint"}
              >
                <div className="constraint-head">
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
    </aside>
  );
}
