// Regression coverage for spec 2026-09-25-14: signing out used to log 3 to 4
// "[auth] token refresh failed: no-refresh-token" console errors, because the
// dashboard's still-mounted React Query queries kept hitting the API with the
// now-revoked token while logout()'s POST /auth/logout was in flight, and
// each 401 raced the not-yet-cleared session into a spurious refresh attempt.
// Since spec 2026-09-25-13 turned on PostHog console-error autocapture, that
// noise also became a false-positive exception on every single logout.
//
// Each test drives its own fresh login (not the shared `authenticatedPage`
// fixture) so it can log out without invalidating the session other tests in
// this worker rely on.

import { test, expect } from "@playwright/test";

async function login(page: import("@playwright/test").Page): Promise<void> {
  await page.goto("orgs/test/login");
  await page.waitForLoadState("networkidle");
  await expect(page.getByTestId("login-title")).toBeVisible();
  await page.getByTestId("login-email").fill("test@test.com");
  await page.getByTestId("login-password").fill("test");
  await page.getByTestId("login-submit").click();
  await page.waitForURL((url) => !url.pathname.includes("/login"), {
    timeout: 10000,
  });
}

/** Collects console `error` messages matching the refresh-failure text from
 * now until the login page has been idle for 3s past the logout click —
 * matching the spec's Playwright test description. */
function trackRefreshErrors(page: import("@playwright/test").Page): string[] {
  const matches: string[] = [];
  page.on("console", (msg) => {
    if (msg.type() === "error" && msg.text().includes("token refresh failed")) {
      matches.push(msg.text());
    }
  });
  return matches;
}

test.describe("Logout does not log spurious token-refresh errors", () => {
  test("signing out from the sidebar at desktop width logs no refresh-failed errors", async ({
    page,
  }) => {
    await login(page);

    // Land on the dashboard (org root), which fires several concurrent
    // list queries (KPI tiles, needs-attention, active incidents, recent
    // activity). Wait only for the sidebar, not networkidle — the whole
    // point is to sign out while those queries may still be in flight,
    // reproducing the race the fix addresses.
    await expect(page.getByTestId("app-sidebar")).toBeVisible({ timeout: 10000 });

    const refreshErrors = trackRefreshErrors(page);

    await page.getByTestId("user-menu-button").click();
    await expect(page.getByTestId("logout-button")).toBeVisible();
    await page.getByTestId("logout-button").click();

    await page.waitForURL(/\/orgs\/test\/login/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    // Give any straggling in-flight request from before logout time to land
    // and (if the bug were still present) log its error.
    await page.waitForTimeout(3000);

    expect(refreshErrors).toEqual([]);
  });

  test("signing out from the sidebar at mobile width (420px) logs no refresh-failed errors", async ({
    page,
  }) => {
    // This is how the prod sessions in the spec's evidence signed out.
    await page.setViewportSize({ width: 420, height: 900 });

    await login(page);

    await expect(page.getByTestId("sidebar-trigger")).toBeVisible({ timeout: 10000 });

    const refreshErrors = trackRefreshErrors(page);

    // Mobile sidebar is a portaled sheet — open it before reaching the user
    // menu inside it.
    await page.getByTestId("sidebar-trigger").click();
    await expect(page.getByTestId("user-menu-button")).toBeVisible();
    await page.getByTestId("user-menu-button").click();
    await expect(page.getByTestId("logout-button")).toBeVisible();
    await page.getByTestId("logout-button").click();

    await page.waitForURL(/\/orgs\/test\/login/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    await page.waitForTimeout(3000);

    expect(refreshErrors).toEqual([]);
  });
});
