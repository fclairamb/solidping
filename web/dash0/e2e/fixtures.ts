import {
  test as base,
  expect,
  type Page,
  type BrowserContext,
} from "@playwright/test";

type AuthStorageState = Awaited<ReturnType<BrowserContext["storageState"]>>;

// Honor E2E_BASE_URL (side-car test server) like playwright.config.ts does;
// fall back to the CI default. Spec files use this for direct page.request
// setup/cleanup calls so they hit the same server as page navigation instead
// of silently falling back to :4000.
export const API_BASE = process.env.E2E_BASE_URL
  ? new URL(process.env.E2E_BASE_URL).origin
  : "http://localhost:4000";

/**
 * Test fixture that provides authenticated page context.
 * Uses the test credentials (test@test.com/test) for login.
 *
 * Login happens at most once per Playwright *worker* process, not once per
 * test: `authWorkerStorageState` drives the real UI login form a single time
 * and caches the resulting `storageState()` (cookies + localStorage, which is
 * where the access/refresh token pair lives); the test-scoped
 * `authenticatedPage` then just spins up a fresh browser context seeded from
 * that cached state, so each test still gets its own isolated page/context
 * exactly like before, but without re-driving the login form and re-paying
 * its cost (argon2id verification + several serialized DB round trips).
 *
 * This caps real concurrent logins at `workers` instead of `tests-in-flight`,
 * which is what was overloading the backend and causing dashboard.spec.ts's
 * intermittent `waitForURL` timeouts. See spec 2026-07-06-02.
 *
 * The access token (1h) and refresh token (7d) comfortably outlive a single
 * worker's run of the full suite (server/internal/config/config.go), and the
 * dashboard already silently refreshes the access token in the background
 * (spec 2026-07-05-11), so a long-lived cached session behaves like a real
 * returning user for every test except the ones that specifically want a
 * fresh/unauthenticated page (those import `test`/`expect` directly from
 * "@playwright/test" instead of from this file, or use the plain `page`
 * fixture, and are unaffected by this change).
 */
export const test = base.extend<
  { authenticatedPage: Page },
  { authWorkerStorageState: AuthStorageState }
>({
  authWorkerStorageState: [
    async ({ browser }, use) => {
      const context = await browser.newContext();
      const page = await context.newPage();

      await page.goto("orgs/test/login");
      await page.waitForLoadState("networkidle");

      const loginTitle = page.getByTestId("login-title");
      await loginTitle.waitFor({ state: "visible", timeout: 10000 });

      await page.getByTestId("login-email").fill("test@test.com");
      await page.getByTestId("login-password").fill("test");

      await page.getByTestId("login-submit").click();

      await page.waitForURL((url) => !url.pathname.includes("login"), {
        timeout: 10000,
      });
      await page.waitForLoadState("networkidle");

      const storageState = await context.storageState();
      await context.close();

      await use(storageState);
    },
    { scope: "worker" },
  ],

  authenticatedPage: async ({ browser, authWorkerStorageState }, use) => {
    const context = await browser.newContext({
      storageState: authWorkerStorageState,
    });
    const page = await context.newPage();

    // The old fixture ended with the browser sitting on the post-login
    // landing page (/orgs/test, per spec #46) after driving the login form.
    // Consumers rely on that: several tests assert page.url() or read
    // dashboard content without navigating anywhere themselves first. A
    // fresh context here starts at about:blank, so reproduce the same
    // landing state explicitly instead of just restoring cookies/storage.
    await page.goto("orgs/test");
    await page.waitForLoadState("networkidle");

    // eslint-disable-next-line react-hooks/rules-of-hooks
    await use(page);

    await context.close();
  },
});

/**
 * Drives the real login form on `page` with the standard test credentials
 * and waits for the post-login landing state — the same flow
 * `authWorkerStorageState` performs once per worker, factored out for tests
 * that need a *dedicated*, disposable session instead of the shared
 * worker-cached one (see spec 2026-08-05-02: the shared session is minted
 * once and never refreshed, so `enforceSessionCap`
 * (server/internal/handlers/auth/service.go) — which prunes a user's
 * `refresh` sessions down to 10 on every fresh login elsewhere in the
 * suite — eventually evicts it as the least-recently-active row. Tests that
 * assert on the session's own `user_tokens` row surviving (the sessions
 * list, an explicit `/auth/refresh`) must not rely on that shared session).
 */
export async function freshLogin(page: Page): Promise<void> {
  await page.goto("orgs/test/login");
  await page.waitForLoadState("networkidle");
  await page.getByTestId("login-title").waitFor({ state: "visible", timeout: 10000 });
  await page.getByTestId("login-email").fill("test@test.com");
  await page.getByTestId("login-password").fill("test");
  await page.getByTestId("login-submit").click();
  await page.waitForURL((url) => !url.pathname.includes("login"), {
    timeout: 10000,
  });
  await page.waitForLoadState("networkidle");
}

/**
 * Stubs the SLO coverage chip's request on the check detail page.
 *
 * `checks.$checkUid.index.tsx` renders `<SloCoverageChip>` unconditionally, and
 * that chip fires `GET /api/v1/orgs/:org/slos?checkUid=…` on every load (spec
 * 2026-08-20-01). A spec that mocks the rest of the check-detail traffic to be
 * hermetic and then waits on `waitForLoadState("networkidle")` would otherwise
 * still take this one real request — one more in-flight XHR that has to settle
 * on the shared single-connection sqlite path, which is enough to make those
 * suites flaky under parallel workers.
 *
 * Call it wherever the other check-detail routes are mocked. Deliberately a
 * helper rather than a global fixture route: a spec that WANTS the real
 * endpoint (slos.spec.ts) must keep getting it.
 */
export async function mockSloCoverage(page: Page): Promise<void> {
  await page.route("**/api/v1/orgs/*/slos*", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: [] }),
    }),
  );
}

/**
 * Turns the browser HTTP cache OFF for `page`. Call it before the first
 * navigation.
 *
 * The public status-page endpoints answer `Cache-Control: public, max-age=60`
 * for a `public` page — deliberate, per spec
 * `2026-08-22-06-public-status-page-view-sends-no-cache-control.md`, whose
 * acceptance is explicitly "status changes still surface within 60 s".
 *
 * That directive is invisible to a human pressing reload but fatal to a spec
 * that saves branding/appearance in the dashboard and then re-reads the public
 * page: `page.reload()` revalidates the *document*, while the SPA's own
 * `fetch` for `/api/v1/status-pages/:org/:slug` is served straight from the
 * still-fresh cache entry. A poll loop shorter than 60 s can then only ever
 * observe the pre-save body — which is what silently broke
 * status-page-appearance.spec.ts and status-page-branding.spec.ts once the
 * cache directive landed.
 *
 * These specs assert what the server *publishes*, not how long a browser is
 * entitled to hold it, so they read through with the cache disabled. Nothing
 * is loosened: the assertions are unchanged and still fail if the save never
 * reaches the public payload. (Playwright disables the HTTP cache for any page
 * with routing enabled — the pass-through handler is what buys that.)
 */
export async function disableHttpCache(page: Page): Promise<void> {
  await page.route("**/*", (route) => route.continue());
}

/**
 * Logs in through the API (not the UI) and returns the access token, for
 * specs that need to set fixtures up over REST before driving the browser.
 */
export async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  const body = await resp.json();
  return body.accessToken;
}

export interface HeartbeatCheck {
  uid: string;
  name: string;
  /** Server-generated slug (e.g. "heartbeat-heartbeat-2") — the incidents
   * list renders the incident title ("<slug> is down") and the check slug,
   * never the check *name*, so incident-row assertions must match on this. */
  slug: string;
  hbToken: string;
}

/**
 * Creates a heartbeat check over the API and returns its uid/slug plus the
 * heartbeat token needed to ping it.
 *
 * Shared by live-updates.spec.ts and check-heartbeat-evaluation-rows.spec.ts:
 * a heartbeat is the only check type whose results a spec can create on
 * demand without a real probe, so it is the fixture of choice for anything
 * that needs genuine result rows.
 *
 * `period` (e.g. "01:00:00") is worth setting deliberately. A heartbeat check
 * is scheduled like any other: its passive job writes a `No heartbeat
 * received` → down result whenever the last signal is older than the period,
 * and one scheduler-evaluation row per period regardless. The default (60 s)
 * is fine for tests that only care about the heartbeats they send themselves;
 * a test that counts rows, or that must not see a status transition it did
 * not cause, passes a long period so no second evaluation can run inside the
 * test's lifetime.
 */
export async function createHeartbeatCheck(
  page: Page,
  token: string,
  name: string,
  checkGroupUid?: string,
  period?: string,
): Promise<HeartbeatCheck> {
  const hbToken = `e2e-live-${Date.now()}-${Math.floor(Math.random() * 1e6)}`;
  const resp = await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
    headers: { Authorization: `Bearer ${token}` },
    data: {
      name,
      type: "heartbeat",
      config: { token: hbToken },
      // Open/resolve incidents on the first failing/passing heartbeat so the
      // live-update latency is the only delay under test.
      confirmationPeriodSeconds: 0,
      recoveryPeriodSeconds: 0,
      ...(checkGroupUid ? { checkGroupUid } : {}),
      ...(period ? { period } : {}),
    },
  });
  expect(resp.status()).toBe(201);
  const body = await resp.json();
  return { uid: body.uid, name, slug: body.slug, hbToken };
}

export { expect, type Page };
