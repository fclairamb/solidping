import { test, expect } from "@playwright/test";
import { API_BASE } from "./fixtures";

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

test.describe("Register: public route stays public", () => {
  // Regression e2e for spec 2026-08-29-12: OrgLayout ($org.tsx) computed
  // isOrgPublicRoute but never used it to gate useFeatures(), so an
  // unauthenticated visitor to /orgs/:org/register triggered a 401 on
  // GET /api/v1/features and was silently bounced to
  // /login?session_expired=true before ever seeing the sign-up form.
  // redirectToExpiredLogin's own no-op guard only covered "/login", not
  // "/register", so the redirect was fully user-visible there. Must prove
  // the negative: settle on /register (not /login), no session_expired
  // param, and the form itself rendered — not just "navigation didn't
  // happen yet".
  test("an unauthenticated visitor stays on /register and sees the sign-up form", async ({
    page,
  }) => {
    await page.goto("orgs/test/register");
    await page.waitForLoadState("networkidle");

    // Give the buggy background call a chance to fire and redirect before
    // asserting the negative.
    await page.waitForTimeout(1000);

    const url = new URL(page.url());
    expect(url.pathname).toContain("/orgs/test/register");
    expect(url.pathname).not.toContain("/login");
    expect(url.searchParams.get("session_expired")).toBeNull();

    await expect(page.locator("#email")).toBeVisible();
    await expect(page.locator("#password")).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Create account" }),
    ).toBeVisible();
  });
});

test.describe("Login: deep-link returnTo", () => {
  test("returns the user to the original deep link (with query string) after login", async ({
    page,
  }) => {
    // Visit a protected deep link while logged out. `?status=down` is a search
    // param the checks route preserves, so it must survive the round-trip.
    await page.goto("orgs/test/checks?status=down");
    await page.waitForLoadState("networkidle");

    // The route guard redirects to the org login page, carrying the original
    // URL (path + query) as the returnTo search param.
    await expect(page).toHaveURL(/\/orgs\/test\/login/);
    const loginUrl = new URL(page.url());
    const returnTo = loginUrl.searchParams.get("returnTo");
    expect(returnTo).toContain("/orgs/test/checks");
    expect(returnTo).toContain("status=down");

    // Log in with the test password credentials.
    await expect(page.getByTestId("login-title")).toBeVisible();
    await page.getByTestId("login-email").fill("test@test.com");
    await page.getByTestId("login-password").fill("test");
    await page.getByTestId("login-submit").click();

    // Land back on the original deep link — NOT the org root — including the
    // query string. This is the regression this spec fixes.
    await page.waitForURL(/\/orgs\/test\/checks/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    const finalUrl = new URL(page.url());
    expect(finalUrl.pathname).toContain("/orgs/test/checks");
    // Guard against the pre-fix behavior of landing on the org root.
    expect(finalUrl.pathname).not.toMatch(/\/orgs\/test\/?$/);
    expect(finalUrl.searchParams.get("status")).toBe("down");
  });
});

const LAST_AUTH_METHOD_KEY = "solidping_last_auth_method";

// Fetches /auth/providers so the config-dependent tests can decide whether
// the test backend actually has an OAuth provider / passkeys configured, and
// skip gracefully (covered by manual browser verification) when it doesn't.
async function fetchAuthCapabilities(
  baseURL: string | undefined,
): Promise<{ providers: { type: string; name: string }[]; passkeysEnabled: boolean }> {
  const root = baseURL ? new URL(baseURL).origin : API_BASE;
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

test.describe("Login: forgot-password link placement", () => {
  test("renders the forgot-password link on the password label row", async ({
    page,
  }) => {
    await page.goto("orgs/test/login");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("login-title")).toBeVisible();

    // The link is inline with the Password label, above the password input.
    const forgotLink = page.getByRole("link", { name: /forgot/i });
    await expect(forgotLink).toBeVisible();

    // It points at /forgot-password.
    await expect(forgotLink).toHaveAttribute("href", /\/forgot-password/);

    // It sits above the password field (label row), not after the form.
    const linkBox = await forgotLink.boundingBox();
    const passwordBox = await page.getByTestId("login-password").boundingBox();
    expect(linkBox).not.toBeNull();
    expect(passwordBox).not.toBeNull();
    expect(linkBox!.y).toBeLessThan(passwordBox!.y);
  });

  test("carries the typed email as the email search param", async ({
    page,
  }) => {
    await page.goto("orgs/test/login");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("login-title")).toBeVisible();

    // Type an email, then follow the forgot-password link.
    await page.getByTestId("login-email").fill("someone@example.com");

    const forgotLink = page.getByRole("link", { name: /forgot/i });
    await forgotLink.click();

    await page.waitForURL(/\/forgot-password/, { timeout: 10000 });

    const url = new URL(page.url());
    expect(url.pathname).toContain("/forgot-password");
    expect(url.searchParams.get("email")).toBe("someone@example.com");
  });
});

test.describe("Login: passkey sign-in link", () => {
  test("the passkey control is a clickable text link that triggers the flow", async ({
    page,
    baseURL,
  }) => {
    const { passkeysEnabled } = await fetchAuthCapabilities(baseURL);
    test.skip(!passkeysEnabled, "passkeys are not enabled on the test backend");

    // Count the begin-ceremony requests. A background conditional-UI ceremony
    // may also fire one on mount, so we assert the click *adds* a request
    // rather than waiting for the first one.
    let beginCount = 0;
    page.on("request", (req) => {
      if (req.url().includes("/api/v1/auth/passkeys/login/begin")) beginCount++;
    });

    await page.goto("orgs/test/login");
    await page.waitForLoadState("networkidle");

    // Not promoted (no last-used memory) → the text-link control is rendered.
    const passkeyButton = page.getByTestId("passkey-login-button");
    await expect(passkeyButton).toBeVisible();

    const before = beginCount;
    await passkeyButton.click();

    // Clicking it kicks off a (new) passkey ceremony.
    await expect(() => expect(beginCount).toBeGreaterThan(before)).toPass({
      timeout: 10000,
    });
  });
});

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

  test("the footer brand and version are links to the marketing site and changelog", async ({
    page,
  }) => {
    await page.goto("orgs/test/login");
    await page.waitForLoadState("networkidle");

    const brandLink = page.getByTestId("login-brand-link");
    const versionLink = page.getByTestId("login-version");
    await expect(brandLink).toBeVisible();
    await expect(versionLink).toBeVisible();

    // "SolidPing v0.24.0" as one line of text (brand, a space, then the
    // version link) — spec 2026-09-06-01.
    //
    // The version is deliberately NOT asserted as a semver. The binary is
    // stamped from `git describe --tags --always` (Makefile), and CI checks out
    // without tags, so the server there reports a bare commit sha — the shape
    // under test is "brand, space, v-prefixed version", which is what the footer
    // promises; how the build happens to be versioned is not this test's
    // business.
    const footerText = `${await brandLink.textContent()} ${await versionLink.textContent()}`;
    expect(footerText).toMatch(/^SolidPing v\S+/);

    // The brand link opens the marketing site in a new tab, tagged with the
    // self-hosted campaign (the test server runs self-hosted — no
    // SP_DEPLOYMENT_MODE override).
    await expect(brandLink).toHaveAttribute("target", "_blank");
    const brandHref = await brandLink.getAttribute("href");
    expect(brandHref).toBeTruthy();
    const brandUrl = new URL(brandHref!);
    expect(brandUrl.origin).toBe("https://www.solidping.io");
    expect(brandUrl.searchParams.get("utm_campaign")).toBe("self-hosted");

    // The version link goes straight to the unadorned docs changelog page.
    await expect(versionLink).toHaveAttribute("target", "_blank");
    await expect(versionLink).toHaveAttribute(
      "href",
      "https://solidping.io/docs/changelog",
    );

    // The run-mode badge is untouched by this change.
    await expect(page.getByTestId("login-runmode")).toHaveText("test");
  });
});
