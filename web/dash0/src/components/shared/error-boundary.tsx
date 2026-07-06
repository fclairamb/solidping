import { Component, useEffect } from "react";
import type { ReactNode, ErrorInfo } from "react";
import { useRouter, type ErrorComponentProps } from "@tanstack/react-router";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { AlertTriangle, RotateCw } from "lucide-react";

/**
 * Presentational error card shared by every error surface: the root
 * ErrorBoundary, the router's defaultErrorComponent (RouteErrorFallback),
 * and the design reference. The technical detail stays tucked away in a
 * collapsible block — visible when needed for a bug report, not shouted at
 * the user.
 */
export function ErrorFallbackCard({
  error,
  onRetry,
}: {
  error?: unknown;
  onRetry?: () => void;
}) {
  const detail =
    error instanceof Error ? error.message : error ? String(error) : null;

  return (
    <Card className="w-full max-w-md text-center">
      <CardHeader>
        <div className="flex justify-center mb-2">
          <AlertTriangle className="h-10 w-10 text-destructive" />
        </div>
        <CardTitle>Something went wrong</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <p className="text-muted-foreground">
          An unexpected error occurred. This is usually transient — trying
          again should bring the page back.
        </p>
        {detail ? (
          <details className="rounded-md border bg-muted/40 px-3 py-2 text-left text-xs">
            <summary className="cursor-pointer select-none text-muted-foreground">
              Technical details
            </summary>
            <pre className="mt-2 overflow-x-auto whitespace-pre-wrap break-words">
              {detail}
            </pre>
          </details>
        ) : null}
        <div className="flex flex-wrap justify-center gap-2">
          {onRetry ? (
            <Button onClick={onRetry}>
              <RotateCw />
              Try again
            </Button>
          ) : null}
          <Button
            variant={onRetry ? "outline" : "default"}
            onClick={() => window.location.reload()}
          >
            Reload page
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}

/**
 * Router-level error UI, wired as `defaultErrorComponent` in main.tsx. It
 * renders at the route match that threw, so parent layouts (sidebar,
 * header) stay mounted and navigating away still works — a crash in one
 * page no longer blanks the whole app. "Try again" re-runs loaders and
 * resets the boundary, which recovers from transient render/data errors.
 */
export function RouteErrorFallback({ error, reset }: ErrorComponentProps) {
  const router = useRouter();

  useEffect(() => {
    // Feed the bug-report ring buffer (see feedback/errorCollector.ts),
    // mirroring what the root boundary's componentDidCatch does.
    console.error("Route error boundary caught an error:", error);
  }, [error]);

  const onRetry = () => {
    void router.invalidate();
    reset();
  };

  return (
    <div className="flex min-h-[50vh] flex-1 items-center justify-center p-4">
      <ErrorFallbackCard error={error} onRetry={onRetry} />
    </div>
  );
}

interface Props {
  children: ReactNode;
}

interface State {
  hasError: boolean;
  error?: Error;
}

/**
 * Last-resort boundary around the entire app (outside the router), for
 * errors the router's per-route boundaries can't catch — provider or
 * render failures above the route tree.
 */
export class ErrorBoundary extends Component<Props, State> {
  constructor(props: Props) {
    super(props);
    this.state = { hasError: false };
  }

  static getDerivedStateFromError(error: Error): State {
    return { hasError: true, error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error("ErrorBoundary caught an error:", error, info);
  }

  render() {
    if (this.state.hasError) {
      return (
        <div className="min-h-screen flex items-center justify-center bg-background p-4">
          <ErrorFallbackCard
            error={this.state.error}
            onRetry={() => this.setState({ hasError: false, error: undefined })}
          />
        </div>
      );
    }

    return this.props.children;
  }
}
