import { Component, ErrorInfo, ReactNode } from "react";

/**
 * Turns a render crash into a readable failure instead of a white page.
 *
 * React unmounts the entire tree when a component throws, so without this the
 * symptom of any bug anywhere is a blank screen with the cause only in the
 * browser console. For an operator that is indistinguishable from the backend
 * being down, and it sends them looking in the wrong place — which is exactly
 * what happened during the M11 rebuild.
 *
 * The message is shown rather than hidden behind "something went wrong".
 * This is an operator tool: the person reading it can act on a stack trace,
 * and hiding it only means they have to open devtools to learn anything.
 */
interface Props {
  children: ReactNode;
}

interface State {
  error: Error | null;
  stack: string | null;
}

export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null, stack: null };

  static getDerivedStateFromError(error: Error): Partial<State> {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    this.setState({ stack: info.componentStack ?? null });
    // Still log it: the console keeps the full trace, which the panel trims.
    console.error("k8s-dencer UI crashed", error, info);
  }

  render() {
    const { error, stack } = this.state;
    if (!error) return this.props.children;

    return (
      <div className="crash" role="alert">
        <h2>The interface crashed</h2>
        <p className="crash-detail">
          This is a bug in k8s-dencer, not a problem with your cluster — the planner and executor
          are unaffected and any run in progress is still running.
        </p>

        <pre className="crash-message">{error.message}</pre>

        {stack && (
          <details className="crash-more">
            <summary>Where</summary>
            <pre>{stack.trim()}</pre>
          </details>
        )}

        <div className="crash-actions">
          <button className="btn btn-primary" onClick={() => window.location.reload()}>
            Reload
          </button>
          <button
            className="btn"
            onClick={() => this.setState({ error: null, stack: null })}
            title="Re-render without reloading. Useful when the cause was transient state."
          >
            Try again
          </button>
        </div>
      </div>
    );
  }
}
