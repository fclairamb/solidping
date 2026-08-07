// Honor E2E_BASE_URL (side-car test server) like playwright.config.ts does;
// fall back to the CI default. Spec files use this for direct fetch/page.goto
// setup calls so they hit the same server as page navigation instead of
// silently falling back to :4000.
export const API_BASE = process.env.E2E_BASE_URL
  ? new URL(process.env.E2E_BASE_URL).origin
  : "http://localhost:4000";
