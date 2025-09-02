import { test as base, expect, type Page } from "@playwright/test";

/**
 * Test fixture that provides authenticated page context.
 * Uses the test credentials (test@test.com/test) for login.
 */
export const test = base.extend<{ authenticatedPage: Page }>({
  authenticatedPage: async ({ page }, use) => {
    // Navigate to org-specific login
    await page.goto("orgs/test/login");
    await page.waitForLoadState("networkidle");

    // Wait for login form to be visible using test IDs
    const loginTitle = page.getByTestId("login-title");
    await loginTitle.waitFor({ state: "visible", timeout: 10000 });

    // Fill in credentials (test user) using test IDs
    await page.getByTestId("login-email").fill("test@test.com");
    await page.getByTestId("login-password").fill("test");

    // Submit login form using test ID
    await page.getByTestId("login-submit").click();

    // Wait for navigation away from login to org dashboard
    await page.waitForURL((url) => !url.pathname.includes("login"), {
      timeout: 10000,
    });

    // Wait for authenticated page to be loaded
    await page.waitForLoadState("networkidle");

    // Use the authenticated page
    // eslint-disable-next-line react-hooks/rules-of-hooks
    await use(page);
  },
});

export { expect, type Page };
