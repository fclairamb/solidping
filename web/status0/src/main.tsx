import { StrictMode, useEffect } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createRouter } from "@tanstack/react-router";
import { TooltipProvider } from "@/components/ui/tooltip";
import { routeTree } from "./routeTree.gen";
import { ApiError, NetworkError } from "@/api/client";
import i18n from "./i18n";
import "./index.css";

const basepath = import.meta.env.VITE_BASE_URL || "";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 1000 * 30, // 30 seconds
      retry: (failureCount, error) => {
        if (error instanceof ApiError && error.status && error.status < 500) {
          return false;
        }
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

// Keep <html lang> in sync with the active i18n language. Without this, Chrome
// sees an `lang="en"` page rendered in another language and offers to translate
// it — auto-translate wraps text nodes in <font> tags, which clashes with React
// reconciliation and throws "removeChild on Node" on the next re-render.
//
// Defence in depth against the same "removeChild on Node" family of crashes:
//   * index.html carries `translate="no"` + <meta name="google" content="notranslate">
//     so Chrome never rewrites this DOM in the first place, and the
//     <html lang> sync below removes the prompt that would tempt a visitor to
//     translate manually;
//   * the version line (status-page-view.tsx) is additionally marked
//     translate="no" — it is rewritten on every 30 s poll, which is the render
//     most likely to remove a re-parented text node; and
//   * the entry mounts a SINGLE React root even when this module is
//     re-executed (see the createRoot call at the bottom of this file), which
//     is what actually reproduced the crash on every dev page load.
// e2e/translate-resilience.spec.ts is the regression guard for all three.
function useSyncHtmlLang() {
  useEffect(() => {
    const sync = () => {
      document.documentElement.lang =
        i18n.resolvedLanguage || i18n.language || "en";
    };
    sync();
    i18n.on("languageChanged", sync);
    return () => {
      i18n.off("languageChanged", sync);
    };
  }, []);
}

function App() {
  useSyncHtmlLang();
  return (
    <QueryClientProvider client={queryClient}>
      <TooltipProvider delayDuration={300}>
        <RouterProvider router={router} context={{ queryClient }} />
      </TooltipProvider>
    </QueryClientProvider>
  );
}

/**
 * Mount exactly one React root per document.
 *
 * Vite re-executes this entry module whenever something below it hot-updates
 * and no intermediate module accepts the update — the generated
 * `routeTree.gen.ts` does exactly that on the first request after the router
 * plugin regenerates it. A second `createRoot()` on the same container does not
 * replace the first tree, it mounts a SECOND one into #root: React warns
 * ("createRoot() on a container that has already been passed to createRoot()"),
 * and the original tree then dies on its next commit with
 *
 *   NotFoundError: Failed to execute 'removeChild' on 'Node'
 *
 * which the visitor sees as TanStack Router's "Something went wrong!" screen.
 * Reusing the cached root — what React's own warning tells you to do — turns a
 * hot update back into a plain re-render. In production this module is
 * evaluated once, so the cache is a no-op.
 */
type RootHost = Window & { __spStatus0Root?: Root };
const host = window as RootHost;
const container = document.getElementById("root")!;
host.__spStatus0Root ??= createRoot(container);
host.__spStatus0Root.render(
  <StrictMode>
    <App />
  </StrictMode>
);
