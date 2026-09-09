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

/** The shape the public status-page endpoint returns for a page's identity. */
export interface ResolvedStatusPage {
  /** Org slug the page lives under. */
  org: string;
  uid: string;
  /** The page's own slug — `/s/<org>/<slug>` addresses it directly. */
  slug: string;
  /** Display name, as rendered in the page header. */
  name: string;
}

/**
 * Resolves the org and default status page this run should exercise.
 *
 * Which org exists depends on how the server was seeded: `make dev` seeds
 * `default` and its `status-0` page, while `SP_RUNMODE=test` (what CI runs)
 * seeds `test` / `test-status-page` instead. Hardcoding either one makes a spec
 * pass on one server and fail on the other for a reason that has nothing to do
 * with what it is testing — which is precisely why several specs sat out of CI.
 *
 * `E2E_ORG` pins the choice when the caller knows; otherwise both are probed,
 * `default` first. The identity comes back from the server, so assertions on
 * the page NAME stay true on either.
 */
export async function resolveDefaultStatusPage(): Promise<ResolvedStatusPage> {
  const orgs = process.env.E2E_ORG ? [process.env.E2E_ORG] : ["default", "test"];

  for (const org of orgs) {
    const res = await fetch(`${API_BASE}/api/v1/status-pages/${org}`, {
      cache: "no-store",
    });
    if (!res.ok) continue;

    const page = (await res.json()) as {
      uid: string;
      slug: string;
      name: string;
    };

    return { org, uid: page.uid, slug: page.slug, name: page.name };
  }

  throw new Error(
    `No public status page found on ${API_BASE} for orgs ${orgs.join(", ")}`,
  );
}
