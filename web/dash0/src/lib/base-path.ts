/**
 * DASH_BASE is this app's own URL prefix, without a trailing slash (`/d`).
 *
 * It mirrors `config.DashboardBasePath` on the backend
 * (`server/internal/config/config.go`) and the Vite `base` in
 * `vite.config.ts`. Reading it from `VITE_BASE_URL` keeps a build with a
 * custom base consistent; the literal is only the fallback for environments
 * that do not define it — unit tests run under vitest, which has no Vite
 * `define` block.
 *
 * Use it instead of writing the prefix out by hand: an absolute `href` or a
 * `returnTo` that hard-codes the prefix silently breaks the day it moves,
 * and the prefix HAS moved once already (`/dash0` → `/d`).
 */
export const DASH_BASE = (import.meta.env.VITE_BASE_URL || "/d").replace(
  /\/+$/,
  "",
);

/**
 * STATUS_BASE is the PUBLIC STATUS PAGE app's URL prefix (`web/status0`,
 * mounted at `/s`).
 *
 * The dashboard links out to that app, so this cannot be derived from
 * `VITE_BASE_URL` — that is *this* app's base. It mirrors
 * `config.StatusBasePath` and `web/status0/vite.config.ts`.
 */
export const STATUS_BASE = "/s";
