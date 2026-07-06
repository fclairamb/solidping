import {
  test as base,
  expect,
  type Page,
  type BrowserContext,
} from "@playwright/test";

type AuthStorageState = Awaited<ReturnType<BrowserContext["storageState"]>>;

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

    // eslint-disable-next-line react-hooks/rules-of-hooks
    await use(page);

    await context.close();
  },
});

export { expect, type Page };
