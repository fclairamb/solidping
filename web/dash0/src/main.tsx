import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createRouter } from "@tanstack/react-router";
import { routeTree } from "./routeTree.gen";
import { TooltipProvider } from "@/components/ui/tooltip";
import { AuthProvider, useAuth } from "@/contexts/AuthContext";
import { ErrorBoundary } from "@/components/shared/error-boundary";
import { ApiError, NetworkError, setToken } from "@/api/client";
import { installErrorCollector } from "@/components/feedback/errorCollector";
import "./i18n";
import "./index.css";

installErrorCollector();

// Get base URL from Vite config (empty string means root "/")
const basepath = import.meta.env.VITE_BASE_URL || "";

// OAuth sign-in handoff. External providers (Slack, Google, …) redirect back to
// `/orgs/<slug>?access_token=…&org=<slug>`. We persist the token synchronously
// here — before the router or any React Query mounts. Doing it later (in a
// component effect) loses a race: org-scoped queries fire un-authenticated,
// 401, and apiFetch's global handler redirects to /login before the token is
// stored, bouncing the user straight back out of the sign-in they just
// completed.
(() => {
  const params = new URLSearchParams(window.location.search);
  const accessToken = params.get("access_token");
  if (!accessToken) return;

  setToken(accessToken);

  const orgSlug = params.get("org");
  if (orgSlug) {
    localStorage.setItem("solidping_org", orgSlug);
  }

  // Drop the token from the URL and land on the resolved org dashboard.
  const dest = orgSlug ? `${basepath}/orgs/${orgSlug}` : window.location.pathname;
  window.history.replaceState(null, "", dest);
})();

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 1000 * 60, // 1 minute
      retry: (failureCount, error) => {
        // Never retry 4xx errors (client errors)
        if (error instanceof ApiError && error.status && error.status < 500) {
          return false;
        }
        // Retry 5xx and network errors up to 3 times
        if (error instanceof ApiError || error instanceof NetworkError) {
          return failureCount < 3;
        }
        return false;
      },
      retryDelay: (attemptIndex) =>
        Math.min(1000 * Math.pow(2, attemptIndex), 10000),
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
