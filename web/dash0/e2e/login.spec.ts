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

const LAST_AUTH_METHOD_KEY = "solidping_last_auth_method";

// Fetches /auth/providers so the config-dependent tests can decide whether
// the test backend actually has an OAuth provider / passkeys configured, and
// skip gracefully (covered by manual browser verification) when it doesn't.
async function fetchAuthCapabilities(
  baseURL: string | undefined,
): Promise<{ providers: { type: string; name: string }[]; passkeysEnabled: boolean }> {
  const root = (baseURL ?? "http://localhost:4000/dash0/").replace(
    /\/dash0\/?$/,
    "",
  );
  const res = await fetch(`${root}/api/v1/auth/providers`);
  const body = (await res.json()) as {
    data?: { type: string; name: string }[];
    passkeysEnabled?: boolean;
  };
  return {
    providers: body.data ?? [],
    passkeysEnabled: body.passkeysEnabled ?? false,
  };
}

test.describe("Login: remember last auth method", () => {
  test("promotes the password form (badge + email autofocus) when password was last used", async ({
    page,
  }) => {
    // Seed the last-used method before the app boots so the login page reads
    // it on mount.
    await page.addInitScript(
      ([key]) => {
        window.localStorage.setItem(key, "password");
      },
      [LAST_AUTH_METHOD_KEY],
    );

    await page.goto("orgs/test/login");
    await page.waitForLoadState("networkidle");

    // The "Last used" badge renders next to the Sign in button for password.
    await expect(page.getByTestId("login-last-used-badge")).toBeVisible();

    // The email field is autofocused for the returning password user.
    await expect(page.getByTestId("login-email")).toBeFocused();

    // No promoted top slot for the password case (the form is already primary).
    await expect(page.getByTestId("login-last-used")).toHaveCount(0);
  });

  test("renders the default layout with no badge when there is no memory", async ({
    page,
  }) => {
    // Ensure storage is clean before the app boots.
    await page.addInitScript(
      ([key]) => {
        window.localStorage.removeItem(key);
      },
      [LAST_AUTH_METHOD_KEY],
    );

    await page.goto("orgs/test/login");
    await page.waitForLoadState("networkidle");

    // Default layout: the form is visible but nothing is promoted/badged.
    await expect(page.getByTestId("login-submit")).toBeVisible();
    await expect(page.getByTestId("login-last-used")).toHaveCount(0);
    await expect(page.getByTestId("login-last-used-badge")).toHaveCount(0);
  });

  test("records 'password' after a successful password login", async ({
    page,
  }) => {
    // Start from a clean slate.
    await page.addInitScript(
      ([key]) => {
        window.localStorage.removeItem(key);
      },
      [LAST_AUTH_METHOD_KEY],
    );

    await page.goto("orgs/test/login");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("login-title")).toBeVisible();

    await page.getByTestId("login-email").fill("test@test.com");
    await page.getByTestId("login-password").fill("test");
    await page.getByTestId("login-submit").click();

    // Wait for redirect away from login to the authenticated area.
    await page.waitForURL((url) => !url.pathname.includes("/login"), {
      timeout: 10000,
    });

    const stored = await page.evaluate(
      (key) => window.localStorage.getItem(key),
      LAST_AUTH_METHOD_KEY,
    );
    expect(stored).toBe("password");
  });

  test("promotes the last-used OAuth provider and removes it from the grid", async ({
    page,
    baseURL,
  }) => {
    const { providers } = await fetchAuthCapabilities(baseURL);
    test.skip(
      providers.length === 0,
      "no OAuth provider configured on the test backend",
    );
    const provider = providers[0];

    await page.addInitScript(
      ([key, value]) => {
        window.localStorage.setItem(key, value);
      },
      [LAST_AUTH_METHOD_KEY, `oauth:${provider.type}`],
    );

    await page.goto("orgs/test/login");
    await page.waitForLoadState("networkidle");

    // Promoted top slot with the brand button + "Last used" badge.
    await expect(page.getByTestId("login-last-used")).toBeVisible();
    await expect(page.getByTestId("login-last-used-badge")).toBeVisible();
    await expect(
      page.getByTestId(`login-oauth-${provider.type}-promoted`),
    ).toBeVisible();

    // The provider is de-duplicated: the plain grid button is gone.
    await expect(page.getByTestId(`login-oauth-${provider.type}`)).toHaveCount(
      0,
    );
  });

  test("promotes the passkey button when passkey was last used", async ({
    page,
    baseURL,
  }) => {
    const { passkeysEnabled } = await fetchAuthCapabilities(baseURL);
    test.skip(
      !passkeysEnabled,
      "passkeys are not enabled on the test backend",
    );

    await page.addInitScript(
      ([key]) => {
        window.localStorage.setItem(key, "passkey");
      },
      [LAST_AUTH_METHOD_KEY],
    );

    await page.goto("orgs/test/login");
    await page.waitForLoadState("networkidle");

    // Promoted passkey button + badge at the top; the bottom duplicate hidden.
    await expect(page.getByTestId("login-last-used")).toBeVisible();
    await expect(page.getByTestId("login-last-used-badge")).toBeVisible();
    await expect(
      page.getByTestId("passkey-login-button-promoted"),
    ).toBeVisible();
    await expect(page.getByTestId("passkey-login-button")).toHaveCount(0);
  });
});

test.describe("Login: passkey error handling", () => {
  test("shows the domain-mismatch message (not the generic error) on an RP-ID mismatch", async ({
    page,
    baseURL,
  }) => {
    const { passkeysEnabled } = await fetchAuthCapabilities(baseURL);
    test.skip(
      !passkeysEnabled,
      "passkeys are not enabled on the test backend",
    );

    // Intercept the begin ceremony and rewrite the WebAuthn options' rpId to a
    // domain that is not valid for the page's origin. The browser then throws a
    // SecurityError during navigator.credentials.get(), which
    // @simplewebauthn/browser maps to ERROR_INVALID_RP_ID. No virtual
    // authenticator is needed — the RP-ID check precedes authenticator
    // interaction. This also covers the background conditional-UI ceremony,
    // whose failure must stay silent.
    await page.route(
      "**/api/v1/auth/passkeys/login/begin",
      async (route) => {
        const response = await route.fetch();
        const body = (await response.json()) as {
          options?: { publicKey?: { rpId?: string } };
          session?: string;
        };
        if (body.options?.publicKey) {
          body.options.publicKey.rpId = "example.com";
        }
        await route.fulfill({ response, json: body });
      },
    );

    await page.goto("orgs/test/login");
    await page.waitForLoadState("networkidle");

    // The explicit passkey button must be present (not promoted, since no
    // last-used memory is seeded).
    const passkeyButton = page.getByTestId("passkey-login-button");
    await expect(passkeyButton).toBeVisible();
    await passkeyButton.click();

    // The destructive Alert appears with the domain-mismatch copy — explicitly
    // NOT the generic unexpected-error text.
    const error = page.getByTestId("login-error");
    await expect(error).toBeVisible({ timeout: 5000 });
    await expect(error).toContainText("domain");
    await expect(error).not.toContainText("unexpected error");
  });
});
