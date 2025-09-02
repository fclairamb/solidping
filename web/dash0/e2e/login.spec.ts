import { test, expect } from "@playwright/test";

test.describe("Login Flow", () => {
  test("should display login page", async ({ page }) => {
    // Navigate to org-specific login page
    await page.goto("orgs/test/login");
    await page.waitForLoadState("networkidle");

    // Take screenshot of login page
    await expect(page).toHaveTitle(/SolidPing|Login/i);
    await page.screenshot({ path: "test-results/screenshots/login-page.png" });

    // Check for login form elements using test IDs
    await expect(page.getByTestId("login-logo")).toBeVisible();
    await expect(page.getByTestId("login-title")).toBeVisible();
    await expect(page.getByTestId("login-email")).toBeVisible();
    await expect(page.getByTestId("login-password")).toBeVisible();
    await expect(page.getByTestId("login-submit")).toBeVisible();
  });

  test("should not display sidebar on login page", async ({ page }) => {
    // Navigate to org-specific login page
    await page.goto("orgs/test/login");
    await page.waitForLoadState("networkidle");

    // Wait for login form to be visible
    await expect(page.getByTestId("login-title")).toBeVisible();

    // Verify sidebar is NOT present on login page
    const sidebarTrigger = page.getByTestId("sidebar-trigger");
    await expect(sidebarTrigger).not.toBeVisible();

    // Also verify common sidebar elements are not present
    const dashboardLink = page.getByRole("link", { name: /dashboard/i });
    await expect(dashboardLink).not.toBeVisible();

    // Take screenshot to verify clean login page
    await page.screenshot({
      path: "test-results/screenshots/login-no-sidebar.png",
    });
  });

  test("should show error on invalid credentials", async ({ page }) => {
    // Navigate to org-specific login
    await page.goto("orgs/test/login");
    await page.waitForLoadState("networkidle");

    // Wait for form to be ready
    await expect(page.getByTestId("login-title")).toBeVisible();

    // Fill in invalid credentials using test IDs
    await page.getByTestId("login-email").fill("wrong@example.com");
    await page.getByTestId("login-password").fill("wrongpassword");

    // Submit form using test ID
    await page.getByTestId("login-submit").click();

    // Wait for error message to appear
    await expect(page.getByTestId("login-error")).toBeVisible({
      timeout: 5000,
    });

    // Verify we're still on the login page
    expect(page.url()).toContain("/login");

    // Take screenshot of error state
    await page.screenshot({
      path: "test-results/screenshots/login-error.png",
    });
  });

  test("should successfully login with valid credentials", async ({ page }) => {
    // Navigate to org-specific login
    await page.goto("orgs/test/login");
    await page.waitForLoadState("networkidle");

    // Wait for form to be ready
    await expect(page.getByTestId("login-title")).toBeVisible();

    // Fill in valid credentials (test user) using test IDs
    await page.getByTestId("login-email").fill("test@test.com");
    await page.getByTestId("login-password").fill("test");

    // Take screenshot before login
    await page.screenshot({
      path: "test-results/screenshots/login-filled.png",
    });

    // Submit form using test ID
    await page.getByTestId("login-submit").click();

    // Wait for redirect away from login to authenticated area
    await page.waitForURL((url) => !url.pathname.includes("/login"), {
      timeout: 10000,
    });

    // Wait for page to fully load
    await page.waitForLoadState("networkidle");

    // Take screenshot after successful login
    await page.screenshot({
      path: "test-results/screenshots/login-success.png",
      fullPage: true,
    });

    // Verify we're on the org dashboard (not login)
    const currentUrl = page.url();
    expect(currentUrl).not.toContain("/login");
    expect(currentUrl).toContain("orgs/test");

    // Verify we can see the dashboard or another authenticated page
    const pageContent = await page.textContent("body");
    expect(pageContent).toBeTruthy();
  });

  test("should redirect to login when accessing protected route without auth", async ({
    page,
  }) => {
    // Try to access org dashboard directly without auth
    await page.goto("orgs/test");
    await page.waitForLoadState("networkidle");

    // Should be redirected to org-specific login page
    await expect(page).toHaveURL(/\/orgs\/test\/login/);

    // Take screenshot
    await page.screenshot({
      path: "test-results/screenshots/auth-redirect.png",
      fullPage: true,
    });
  });

  test("should redirect to login after logout", async ({ page }) => {
    // First, login
    await page.goto("orgs/test/login");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("login-title")).toBeVisible();

    await page.getByTestId("login-email").fill("test@test.com");
    await page.getByTestId("login-password").fill("test");
    await page.getByTestId("login-submit").click();

    // Wait for redirect to dashboard
    await page.waitForURL((url) => !url.pathname.includes("/login"), {
      timeout: 10000,
    });
    await page.waitForLoadState("networkidle");

    // Verify we're logged in (sidebar should be visible)
    await expect(page.getByTestId("app-sidebar")).toBeVisible();

    // Take screenshot before logout
    await page.screenshot({
      path: "test-results/screenshots/before-logout.png",
      fullPage: true,
    });

    // Click on user menu to open dropdown
    await page.getByTestId("user-menu-button").click();

    // Wait for dropdown to appear and click logout
    await expect(page.getByTestId("logout-button")).toBeVisible();
    await page.getByTestId("logout-button").click();

    // Should be redirected to login page
    await page.waitForURL(/\/orgs\/test\/login/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    // Verify we're on the login page
    await expect(page.getByTestId("login-title")).toBeVisible();

    // Take screenshot after logout
    await page.screenshot({
      path: "test-results/screenshots/after-logout.png",
      fullPage: true,
    });
  });
});
