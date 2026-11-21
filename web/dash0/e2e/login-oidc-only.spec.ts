import { test, expect, type Page } from "@playwright/test";

/**
 * OIDC-only mode (spec 2026-11-21-02).
 *
 * The E2E backend runs with local login on, so GET /api/v1/auth/providers is
 * answered with the real response, `localLogin` forced and a generic OIDC
 * provider advertised: what is under test here is the login page. The server
 * side (403 LOCAL_LOGIN_DISABLED, the lock-out guard, the IdP group sync) is
 * covered by the Go tests in server/internal/handlers/auth/local_login_test.go
 * and oidc_groups_test.go.
 */
async function advertise(page: Page, localLogin: boolean): Promise<void> {
  await page.route("**/api/v1/auth/providers", async (route) => {
    const response = await route.fetch();
    const body = (await response.json()) as {
      data?: { name: string; type: string }[];
      registrationEnabled?: boolean;
    };
    const data = (body.data ?? []).filter((p) => p.type !== "oidc");
    data.push({ name: "Acme SSO", type: "oidc" });
    await route.fulfill({
      response,
      json: {
        ...body,
        data,
        localLogin,
        registrationEnabled: localLogin ? body.registrationEnabled : false,
      },
    });
  });
}

test.describe("Login: OIDC-only mode", () => {
  test("hides the password form, forgot-password and sign-up when local login is off", async ({
    page,
  }) => {
    await advertise(page, false);
    await page.goto("orgs/test/login");

    await expect(page.getByTestId("login-oauth-oidc")).toBeVisible();
    await expect(page.getByTestId("login-oauth-oidc")).toHaveText("Acme SSO");

    await expect(page.getByTestId("login-form")).toHaveCount(0);
    await expect(page.getByTestId("login-email")).toHaveCount(0);
    await expect(page.getByTestId("login-password")).toHaveCount(0);
    await expect(page.getByTestId("login-submit")).toHaveCount(0);
    await expect(page.getByTestId("login-forgot-password")).toHaveCount(0);
    await expect(page.getByTestId("login-signup")).toHaveCount(0);
  });

  test("keeps the password form when local login is on", async ({ page }) => {
    await advertise(page, true);
    await page.goto("orgs/test/login");

    await expect(page.getByTestId("login-oauth-oidc")).toBeVisible();
    await expect(page.getByTestId("login-form")).toBeVisible();
    await expect(page.getByTestId("login-email")).toBeVisible();
    await expect(page.getByTestId("login-password")).toBeVisible();
    await expect(page.getByTestId("login-forgot-password")).toBeVisible();
  });
});
