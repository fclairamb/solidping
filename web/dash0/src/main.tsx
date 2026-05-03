import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createRouter } from "@tanstack/react-router";
import { routeTree } from "./routeTree.gen";
import { TooltipProvider } from "@/components/ui/tooltip";
import { AuthProvider, useAuth } from "@/contexts/AuthContext";
import { ErrorBoundary } from "@/components/shared/error-boundary";
import { ApiError, NetworkError } from "@/api/client";
import "./i18n";
import "./index.css";

// Get base URL from Vite config (empty string means root "/")
const basepath = import.meta.env.VITE_BASE_URL || "";

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
