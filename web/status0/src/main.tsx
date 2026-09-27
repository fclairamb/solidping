import { StrictMode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { App } from "./app";
// Self-hosted variable fonts, same pair dash0 loads — no external font CDN, so
// a status page stays renderable (and privacy-clean) even when the visitor's
// network blocks third-party origins.
import "@fontsource-variable/inter/index.css";
import "@fontsource-variable/jetbrains-mono/index.css";
import "./index.css";

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
