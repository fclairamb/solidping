import { test, expect, API_BASE, getAuthToken } from "./fixtures";

// Spec 2026-11-25-02: a sign-in provider saved on Server Settings >
// Authentication is live without a restart. The local-login switch is locked
// while no SSO provider is live, and the login page shows the new button as
// soon as the provider is saved.
const KEYS = ["auth.gitlab.client_id", "auth.gitlab.client_secret", "auth.gitlab.enabled"];

test.describe("Server auth: SSO provider settings are live", () => {
  test("saving a provider unlocks local login and shows its login button", async ({
    authenticatedPage,
    browser,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const headers = { Authorization: `Bearer ${token}` };
    const cleanup = async () => {
      for (const key of KEYS) {
        await page.request.delete(`${API_BASE}/api/v1/system/parameters/${key}`, { headers });
      }
    };

    await cleanup();

    try {
      await page.goto("/d/orgs/test/server/auth");

      // The E2E server boots with no SSO provider.
      const localLogin = page.getByTestId("local-login-enabled");
      await expect(localLogin).toBeChecked();
      await expect(localLogin).toBeDisabled();
      await expect(page.getByTestId("local-login-no-provider-hint")).toBeVisible();

      // Unconfigured cards load collapsed (spec 2026-11-25-03).
      await page.getByTestId("provider-card-toggle-gitlab").click();
      await page.getByTestId("provider-field-auth.gitlab.client_id").fill("acme-gitlab-id");
      await page.getByTestId("provider-field-auth.gitlab.client_secret").fill("acme-gitlab-secret");
      await page.getByTestId("provider-enabled-gitlab").click();
      await page.getByTestId("provider-save-gitlab").click();
      await expect(page.getByTestId("settings-saved")).toBeVisible();

      // No restart: the lock is gone at once.
      await expect(localLogin).toBeEnabled();
      await expect(page.getByTestId("local-login-no-provider-hint")).toHaveCount(0);

      // Signed out, the login page offers the provider.
      const anon = await browser.newContext();
      try {
        const login = await anon.newPage();
        await login.goto("orgs/test/login");
        await expect(login.getByTestId("login-oauth-gitlab")).toBeVisible();

        // And its login route answers (a redirect to GitLab, not a 404).
        const resp = await login.request.get(
          `${API_BASE}/api/v1/auth/gitlab/login?org=test`,
          { maxRedirects: 0 },
        );
        expect(resp.status()).toBe(302);
        expect(resp.headers().location).toContain("client_id=acme-gitlab-id");
      } finally {
        await anon.close();
      }
    } finally {
      await cleanup();
    }

    // Deleted again: the route is gated back to 404.
    const gone = await page.request.get(`${API_BASE}/api/v1/auth/gitlab/login?org=test`, {
      maxRedirects: 0,
    });
    expect(gone.status()).toBe(404);
  });
});
