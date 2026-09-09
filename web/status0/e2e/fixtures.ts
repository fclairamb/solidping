// Honor E2E_BASE_URL (side-car test server) like playwright.config.ts does;
// fall back to the CI default. Spec files use this for direct fetch/page.goto
// setup calls so they hit the same server as page navigation instead of
// silently falling back to :4000.
export const API_BASE = process.env.E2E_BASE_URL
  ? new URL(process.env.E2E_BASE_URL).origin
  : "http://localhost:4000";

/**
 * STATUS_BASE is the URL prefix the public status page app is mounted at.
 *
 * Kept here rather than written out in every `page.goto` so the prefix stays a
 * single edit: it moved once already (`/status0` → `/s`, spec 2026-09-09-01)
 * and it mirrors `config.StatusBasePath` (server/internal/config/config.go)
 * plus the Vite `base` in vite.config.ts.
 */
export const STATUS_BASE = "/s";
