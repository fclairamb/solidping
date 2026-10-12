import { test, expect } from "./fixtures";
import { test as base } from "@playwright/test";

// Spec 2026-10-12-01: a first-time visitor (no cookies, no localStorage) must
// not be told their session expired, while a real expired session still is.

base.describe("First visit", () => {
  base("fresh browser on the dashboard root sees no expired banner", async ({ page }) => {
    await page.goto("");
    await expect(page.getByTestId("login-title")).toBeVisible({ timeout: 15000 });
    expect(page.url()).toContain("/login");
    expect(page.url()).not.toContain("session_expired=true");
    await expect(page.getByText(/Your session has expired/i)).toHaveCount(0);
  });
});

test.describe("Invalidated session", () => {
  test("still shows the expired banner and keeps returnTo", async ({ authenticatedPage: page }) => {
    await page.evaluate(() => {
      localStorage.setItem("solidping_session_token", "invalid");
      localStorage.setItem("solidping_refresh_token", "invalid");
    });
    await page.goto("orgs/test/checks");
    await expect(page.getByTestId("login-title")).toBeVisible({ timeout: 15000 });
    expect(page.url()).toContain("session_expired=true");
    expect(page.url()).toContain("returnTo=");
    await expect(page.getByText(/Your session has expired/i)).toBeVisible();
  });
});
