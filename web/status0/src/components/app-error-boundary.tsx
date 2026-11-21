import { Component } from "react";
import type { ErrorInfo, ReactNode } from "react";
import i18n from "@/i18n";

interface Props {
  children: ReactNode;
}

interface State {
  failed: boolean;
}

/**
 * Last line of defence: a render error anywhere in the status page would
 * otherwise unmount the whole React tree and leave a blank page. This shows a
 * short message with a reload button instead.
 */
export class AppErrorBoundary extends Component<Props, State> {
  state: State = { failed: false };

  static getDerivedStateFromError(): State {
    return { failed: true };
  }

  componentDidCatch(error: unknown, info: ErrorInfo) {
    console.error("status page render error", error, info.componentStack);
  }

  render() {
    if (!this.state.failed) return this.props.children;
    return (
      <div
        role="alert"
        data-testid="status-page-unavailable"
        className="flex min-h-screen flex-col items-center justify-center gap-4 p-6 text-center"
      >
        <p>
          {i18n.t("pageUnavailable", {
            defaultValue: "This status page is unavailable right now.",
          })}
        </p>
        <button
          type="button"
          data-testid="status-page-reload"
          className="rounded border px-4 py-2"
          onClick={() => window.location.reload()}
        >
          {i18n.t("pageUnavailableReload", { defaultValue: "Reload" })}
        </button>
      </div>
    );
  }
}
