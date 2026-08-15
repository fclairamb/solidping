import { StrictMode, useEffect } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createRouter } from "@tanstack/react-router";
import { TooltipProvider } from "@/components/ui/tooltip";
import { routeTree } from "./routeTree.gen";
import { ApiError, NetworkError } from "@/api/client";
import i18n from "./i18n";
// Self-hosted variable fonts, same pair dash0 loads — no external font CDN, so
// a status page stays renderable (and privacy-clean) even when the visitor's
// network blocks third-party origins.
import "@fontsource-variable/inter/index.css";
import "@fontsource-variable/jetbrains-mono/index.css";
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
//
// Defence in depth against the "removeChild on Node" family of crashes. These
// are three DISTINCT mechanisms — do not read any one of them as the
// explanation for the others:
//
//   1. Document-level opt-out (pre-existing). index.html carries
//      `translate="no"` + <meta name="google" content="notranslate">, and the
//      <html lang> sync below removes the prompt that tempts a visitor to
//      translate manually. Both are HINTS: a visitor who explicitly picks
//      "Translate to…", or an extension that ignores them, still rewrites the
//      DOM.
//   2. Element-level opt-out (PRODUCTION hardening, spec 2026-08-01-05).
//      Every element whose text changes between renders — status labels,
//      availability percentages, incident kinds, relative timestamps, the
//      subscribe button — is marked translate="no" (see NO_TRANSLATE in
//      components/shared/status-page-view.tsx; the version line is opted out
//      for consistency even though `useVersion` never refetches).
//      Element-level opt-outs are honoured even by a forced translation — per
//      the HTML spec, though not verified here against a real Chrome
//      translation — so at exactly the sites where React rewrites or removes
//      text on every 30 s poll there is no foreign <font> wrapper to trip over.
//      HONESTY NOTE: no crash at these sites has been reproduced against a
//      production build — driving a full forced-translation simulation through
//      language switches and a background refetch did not throw. This is
//      hardening of a real, well-documented failure class, not a fix for a
//      demonstrated production repro.
//   3. Single React root (DEV-ONLY fix). See the createRoot call at the bottom
//      of this file. That one WAS a hard, 100%-reproducible crash — but only
//      under `vite dev`, which is the only thing that re-executes this module.
//      It cannot happen in the built bundle. It is fixed because engineers hit
//      it on every local page load, not because end users did.
//
// e2e/translate-resilience.spec.ts guards 1-3.
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
 * SCOPE: this is a DEV-SERVER fix. `vite dev` is the only thing that
 * re-executes this module; the built bundle has no HMR runtime and evaluates it
 * once, so the cache is a no-op in production. It is worth keeping because
 * without it every single local page load crashed (see below) — it is NOT an
 * explanation for any production report.
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
 * which the engineer sees as TanStack Router's "Something went wrong!" screen.
 * Reusing the cached root — what React's own warning tells you to do — turns a
 * hot update back into a plain re-render.
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
