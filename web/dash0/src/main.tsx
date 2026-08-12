import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createRouter } from "@tanstack/react-router";
import { routeTree } from "./routeTree.gen";
import { TooltipProvider } from "@/components/ui/tooltip";
import { AuthProvider, useAuth } from "@/contexts/AuthContext";
import { ErrorBoundary, RouteErrorFallback } from "@/components/shared/error-boundary";
import { ApiError, NetworkError } from "@/api/client";
import { installErrorCollector } from "@/components/feedback/errorCollector";
import {
  applyOAuthHandoff,
  parseOAuthHandoff,
  resolveHandoffDestination,
} from "@/lib/oauth-handoff";
import "@fontsource-variable/inter/index.css";
import "@fontsource-variable/jetbrains-mono/index.css";
import "./i18n";
import "./index.css";

installErrorCollector();

// Get base URL from Vite config (empty string means root "/")
const basepath = import.meta.env.VITE_BASE_URL || "";

// OAuth sign-in handoff. External providers (Slack, GitHub, Google) redirect
// back to `/orgs/<slug>?access_token=…&refresh_token=…&expires_in=…&org=<slug>`.
// We persist the full session synchronously here — before the router or any
// React Query mounts. Doing it later (in a component effect) loses a race:
// org-scoped queries fire un-authenticated, 401, and apiFetch's global
// handler redirects to /login before the token is stored, bouncing the user
// straight back out of the sign-in they just completed. See
// lib/oauth-handoff.ts for why refresh_token/expires_in must not be dropped.
(() => {
  const handoff = parseOAuthHandoff(window.location.search);
  if (!handoff) return;

  applyOAuthHandoff(handoff);

  // Drop the token params from the URL. Preserve the deep `returnTo` path the
  // backend already redirected us to (now in window.location.pathname) when it
  // passes the shared safe-path / org-match guards, falling back to the org
  // root otherwise — and keep any non-token query params (e.g. an MCP OAuth
  // consent `returnTo` threaded through the SSO round-trip) riding along.
  const dest = resolveHandoffDestination(
    handoff.org,
    window.location.pathname,
    window.location.search,
    basepath,
  );
  window.history.replaceState(null, "", dest);
})();

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 1000 * 60, // 1 minute
      retry: (failureCount, error) => {
        // 429 (rate limited) is retryable despite being a 4xx: the server is
        // asking us to back off and try again, not rejecting the request
        // outright. Honoring it turns overload into backpressure instead of a
        // hard failure that the next live-invalidation tick re-fires blindly.
        if (error instanceof ApiError && error.status === 429) {
          return failureCount < 3;
        }
        // Never retry other 4xx errors (client errors)
        if (error instanceof ApiError && error.status && error.status < 500) {
          return false;
        }
        // Retry 5xx and network errors up to 3 times
        if (error instanceof ApiError || error instanceof NetworkError) {
          return failureCount < 3;
        }
        return false;
      },
      retryDelay: (attemptIndex, error) => {
        // Honor a server-provided Retry-After (seconds, parsed into
        // ApiError.retryAfter by client.ts) on 429, capped at 60s so a hostile
        // or absurd header can't wedge a query for minutes. Everything else
        // falls back to the existing exponential backoff.
        if (
          error instanceof ApiError &&
          error.status === 429 &&
          typeof error.retryAfter === "number" &&
          error.retryAfter > 0
        ) {
          return Math.min(error.retryAfter * 1000, 60_000);
        }
        return Math.min(1000 * Math.pow(2, attemptIndex), 10000);
      },
    },
  },
});

const router = createRouter({
  routeTree,
  context: {
    queryClient,
    auth: undefined!,
  },
  defaultPreload: "intent",
  defaultPreloadStaleTime: 0,
  // Contain render/loader errors at the route match that threw: parent
  // layouts (sidebar, header) stay mounted and usable instead of the
  // router's bare full-screen default ("Something went wrong! / Hide Error").
  defaultErrorComponent: RouteErrorFallback,
  basepath: basepath || "/",
});

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}

function InnerApp() {
  const auth = useAuth();
  return (
    <TooltipProvider delayDuration={300}>
      <RouterProvider router={router} context={{ queryClient, auth }} />
    </TooltipProvider>
  );
}

function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <AuthProvider>
        <InnerApp />
      </AuthProvider>
    </QueryClientProvider>
  );
}

// Reuse the React root across HMR reloads. Without this, every time Vite
// re-executes main.tsx (e.g. after editing imports here) a *new* createRoot
// is attached to #root while the old root and its component tree stay alive,
// producing duplicate listeners and duplicate dialogs (e.g. two CommandMenu
// instances responding to a single Cmd+K).
const container = document.getElementById("root")!;
type Root = ReturnType<typeof createRoot>;
const w = window as unknown as { __reactRoot__?: Root };
const root: Root = w.__reactRoot__ ?? createRoot(container);
w.__reactRoot__ = root;
root.render(
  <StrictMode>
    <ErrorBoundary>
      <App />
    </ErrorBoundary>
  </StrictMode>,
);

// Register the service worker for Web Push notifications.
if ("serviceWorker" in navigator) {
  navigator.serviceWorker.register("/dash0/sw.js").catch((err) => {
    console.warn("[solidping] SW registration failed", err);
  });
}
