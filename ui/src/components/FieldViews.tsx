/**
 * The wells and panel renderings of the field.
 *
 * They share everything that matters with the rack view — the same model, the
 * same placement at the current step, the same fill, the same idea of what is
 * cordoned and what the plan drains. Only the drawing differs. Keeping the
 * shared parts in PackingField and passing them down is what stops three views
 * becoming three subtly different products.
 */

import { Model } from "../layout";

export interface NodeDrawState {
  name: string;
  /** 0..1, requested over allocatable, at the scrubber's current step. */
  fill: number;
  pods: number;
  /** Cordoned in the cluster right now — observed, not predicted. */
  cordoned: boolean;
  /** The plan drains this node at or before the current step — predicted. */
  drained: boolean;
  /** This step is the one being hovered in the ledger. */
  targeted: boolean;
  selected: boolean;
}

interface Props {
  nodes: NodeDrawState[];
  model: Model;
  onSelectNode: (name: string) => void;
}

/* ------------------------------------------------------------------ wells */

/**
 * A node as a vessel, load as a level in it.
 *
 * Fullness is carried by height and luminance together, which is what makes a
 * wall of these readable without reading a single number — and why this is the
 * view that scales. Colour stays out of it entirely; on this surface colour
 * means risk, and how full a node is is not a risk.
 */
export function FieldWells({ nodes, onSelectNode }: Props) {
  return (
    <div className="wells">
      {nodes.map((n) => (
        <button
          key={n.name}
          type="button"
          className={[
            "well",
            n.cordoned ? "well-cordoned" : "",
            n.drained ? "well-drained" : "",
            n.targeted ? "well-targeted" : "",
            n.selected ? "well-selected" : "",
          ]
            .filter(Boolean)
            .join(" ")}
          onClick={() => onSelectNode(n.name)}
          aria-label={`${n.name}, ${Math.round(n.fill * 100)} percent requested, ${n.pods} pods${
            n.cordoned ? ", cordoned" : ""
          }`}
        >
          <span className="well-cap">
            <span className="well-name mono">{shortName(n.name)}</span>
            <span className="well-pct num">{Math.round(n.fill * 100)}</span>
          </span>
          <span className="well-foot mono">
            {n.cordoned ? "cordoned" : n.pods === 0 ? "empty" : `${n.pods} pods`}
          </span>
          {/* Inline height because it is data, not styling: this is the
              measurement the view exists to show. */}
          <span className="well-level" style={{ height: `${Math.min(100, n.fill * 100)}%` }} />
        </button>
      ))}
    </div>
  );
}

/* ------------------------------------------------------------------ panel */

/**
 * One row per node, fullest first.
 *
 * The ordering is the argument: a long tail of barely-used nodes at the bottom
 * of the list *is* the case for consolidating, and nobody has to be told. That
 * is worth more here than the spatial picture, which is what this view trades
 * away.
 */
export function FieldPanel({ nodes, onSelectNode }: Props) {
  const ordered = [...nodes].sort((a, b) => b.fill - a.fill);

  return (
    <div className="panel-view">
      <div className="panel-head">
        <span className="eyebrow">node</span>
        <span className="eyebrow">load</span>
        <span className="eyebrow panel-r">cpu</span>
        <span className="eyebrow panel-r">pods</span>
        <span className="eyebrow panel-r">state</span>
      </div>
      {ordered.map((n) => (
        <button
          key={n.name}
          type="button"
          className={[
            "panel-row",
            n.cordoned ? "panel-row-cordoned" : "",
            n.targeted ? "panel-row-targeted" : "",
            n.selected ? "panel-row-selected" : "",
          ]
            .filter(Boolean)
            .join(" ")}
          onClick={() => onSelectNode(n.name)}
          aria-label={`${n.name}, ${Math.round(n.fill * 100)} percent requested, ${n.pods} pods`}
        >
          <span className="panel-id mono">{shortName(n.name)}</span>
          <span className="panel-trace" aria-hidden="true">
            <span className="panel-trace-bar" style={{ width: `${Math.min(100, n.fill * 100)}%` }} />
          </span>
          <span className="panel-pct num">{Math.round(n.fill * 100)}</span>
          <span className="panel-pods num">{n.pods}</span>
          <span className="panel-state mono">{stateWord(n)}</span>
        </button>
      ))}
    </div>
  );
}

/** Observed states win over predicted ones: what is beats what would be. */
function stateWord(n: NodeDrawState): string {
  if (n.cordoned) return "cordoned";
  if (n.drained) return "drained";
  if (n.pods === 0) return "empty";
  return "";
}

/** Node names are long and share a prefix; the tail is the distinguishing part. */
function shortName(name: string): string {
  return name.length > 18 ? "…" + name.slice(-17) : name;
}
