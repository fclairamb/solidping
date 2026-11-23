// Spec 2026-11-23-03 (solidping-business#107): on iOS Safari some users land
// back on the `default` org's login page, signed out, shortly after signing in.
// WebKit + iPhone emulation did not reproduce it (see the spec), so this file
// pins the scenario the users went through — sign in, reload, come back from
// the marketing site's "Sign in" URL, back/forward — and the diagnostics
// shipped to tell the causes apart in production: clearToken leaves its reason
// in localStorage, and a new session drops it.
//
// Each test drives its own fresh login (not the shared `authenticatedPage`
// fixture) so it can sign out without invalidating other tests' session.

import { test, expect, type Page } from "@playwright/test";

const SESSION_CLEARED_KEY = "solidping_session_cleared";

async function login(page: Page): Promise<void> {
  await page.goto("orgs/test/login");
  await expect(page.getByTestId("login-title")).toBeVisible();
  await page.getByTestId("login-email").fill("test@test.com");
  await page.getByTestId("login-password").fill("test");
  await page.getByTestId("login-submit").click();
  await page.waitForURL((url) => !url.pathname.includes("/login"), { timeout: 10000 });
}

function storedKeys(page: Page): Promise<Record<string, boolean>> {
  return page.evaluate(() => ({
    token: Boolean(localStorage.getItem("solidping_session_token")),
    refresh: Boolean(localStorage.getItem("solidping_refresh_token")),
    org: Boolean(localStorage.getItem("solidping_org")),
  }));
}

test.describe("session persistence after sign-in", () => {
  test("stays signed in across reload, the marketing sign-in URL and back/forward", async ({
    page,
  }) => {
    await login(page);
    const all = { token: true, refresh: true, org: true };
    expect(await storedKeys(page)).toEqual(all);

    await page.goto("orgs/test/account/notifications");
    await expect(page.getByTestId("app-sidebar")).toBeVisible({ timeout: 10000 });

    await page.reload();
    await expect(page.getByTestId("app-sidebar")).toBeVisible({ timeout: 10000 });
    expect(page.url()).toContain("/orgs/test/account/notifications");

    // The marketing site's "Sign in" links: the dashboard root and /login.
    for (const path of ["", "login"]) {
      await page.goto(path);
      await page.waitForURL(/\/d\/orgs\/test(\/)?$/, { timeout: 10000 });
      expect(await storedKeys(page)).toEqual(all);
    }

    await page.goBack();
    await page.goForward();
    await expect(page.getByTestId("app-sidebar")).toBeVisible({ timeout: 10000 });
    expect(page.url()).not.toContain("/login");
    expect(await storedKeys(page)).toEqual(all);
    expect(await page.evaluate((k) => localStorage.getItem(k), SESSION_CLEARED_KEY)).toBeNull();
  });

  test("signing out records the reason, and the next sign-in drops it", async ({ page }) => {
    await login(page);
    await expect(page.getByTestId("app-sidebar")).toBeVisible({ timeout: 10000 });

    await page.getByTestId("user-menu-button").click();
    await page.getByTestId("logout-button").click();
    await page.waitForURL(/\/orgs\/test\/login/, { timeout: 10000 });

    const marker = await page.evaluate((k) => localStorage.getItem(k), SESSION_CLEARED_KEY);
    expect(marker).not.toBeNull();
    const parsed = JSON.parse(marker!) as { reason: string; at: number };
    expect(parsed.reason).toBe("logout");
    expect(typeof parsed.at).toBe("number");
    // Booleans only leave the browser, and the marker holds no token either.
    expect(marker).not.toMatch(/eyJ/);
    expect(await storedKeys(page)).toMatchObject({ token: false, refresh: false });

    await login(page);
    expect(await page.evaluate((k) => localStorage.getItem(k), SESSION_CLEARED_KEY)).toBeNull();
  });
});
