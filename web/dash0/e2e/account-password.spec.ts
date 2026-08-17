import { test, expect, API_BASE } from "./fixtures";

// Spec 2026-08-16-04: a logged-in user had no way to change their password
// from the dashboard — only the logged-out forgotten-password email flow.
//
// Every test here seeds a throwaway user + org (the account-organizations
// pattern) rather than using the shared `test` fixture: rotating the shared
// test@test.com password would break every other spec in the suite.
test.describe("Account > Security > Password", () => {
  const OLD_PASSWORD = "Old-Pass-123!";

  async function seedUserWithOrg(page: import("@playwright/test").Page) {
    const stamp = Date.now() + Math.floor(Math.random() * 1000);
    const email = `acct-pwd-${stamp}@unknown.example`;

    const createUserResp = await page.request.post(
      `${API_BASE}/api/v1/test/users`,
      { data: { email, password: OLD_PASSWORD, name: "Acct Pwd User" } },
    );
    if (createUserResp.status() !== 201) {
      test.skip(
        true,
        `test user-seed endpoint unavailable (server not in SP_RUNMODE=test?): ${createUserResp.status()}`,
      );
    }

    const loginResp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { email, password: OLD_PASSWORD },
    });
    expect(loginResp.status()).toBe(200);
    const login = (await loginResp.json()) as { accessToken: string };

    const orgSlug = `ap1-${stamp.toString(36)}`;
    const createOrgResp = await page.request.post(`${API_BASE}/api/v1/orgs`, {
      headers: { Authorization: `Bearer ${login.accessToken}` },
      data: { name: `Acct Pwd Co ${stamp}`, slug: orgSlug },
    });
    expect(createOrgResp.status()).toBe(201);
    const org = (await createOrgResp.json()) as {
      slug: string;
      accessToken: string;
      refreshToken?: string;
      expiresIn?: number;
    };

    return { email, org };
  }

  async function seedBrowserSession(
    page: import("@playwright/test").Page,
    session: {
      accessToken: string;
      refreshToken?: string;
      expiresIn?: number;
      slug?: string;
    },
  ) {
    await page.addInitScript(
      ({ accessToken, refreshToken, expiresIn, slug }) => {
        localStorage.setItem("solidping_session_token", accessToken as string);
        if (refreshToken) {
          localStorage.setItem("solidping_refresh_token", refreshToken as string);
        }
        if (expiresIn) {
          localStorage.setItem(
            "solidping_expires_at",
            String(Date.now() + Number(expiresIn) * 1000),
          );
          localStorage.setItem("solidping_expires_in", String(expiresIn));
        }
        if (slug) {
          localStorage.setItem("solidping_org", slug as string);
        }
      },
      {
        accessToken: session.accessToken,
        refreshToken: session.refreshToken ?? "",
        expiresIn: session.expiresIn ?? 0,
        slug: session.slug ?? "",
      },
    );
  }

  test("changes the password and the new one is the one that logs in", async ({
    page,
  }) => {
    const { email, org } = await seedUserWithOrg(page);
    await seedBrowserSession(page, { ...org, slug: org.slug });

    await page.goto(`orgs/${org.slug}/account/security`);
    await page.waitForLoadState("networkidle");

    // The card renders the three-field "change" variant for an account that
    // already has a password.
    await expect(page.getByTestId("password-current-input")).toBeVisible();
    await expect(page.getByTestId("password-new-input")).toBeVisible();
    await expect(page.getByTestId("password-confirm-input")).toBeVisible();

    const newPassword = "New-Pass-456!";
    await page.getByTestId("password-current-input").fill(OLD_PASSWORD);
    await page.getByTestId("password-new-input").fill(newPassword);
    await page.getByTestId("password-confirm-input").fill(newPassword);
    await page.getByTestId("password-submit-button").click();

    await expect(page.getByText("Password updated.")).toBeVisible();

    // The caller's session survived — we're still on the security page, not
    // bounced to login.
    await expect(page).toHaveURL(/account\/security/);

    // Logging in again works with the new password and not the old one.
    const newLogin = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { email, password: newPassword },
    });
    expect(newLogin.status()).toBe(200);

    const oldLogin = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { email, password: OLD_PASSWORD },
    });
    expect(oldLogin.status()).not.toBe(200);
  });

  test("a wrong current password surfaces inline and does not log the user out", async ({
    page,
  }) => {
    const { org } = await seedUserWithOrg(page);
    await seedBrowserSession(page, { ...org, slug: org.slug });

    await page.goto(`orgs/${org.slug}/account/security`);
    await page.waitForLoadState("networkidle");

    await page.getByTestId("password-current-input").fill("Definitely-Wrong-1!");
    await page.getByTestId("password-new-input").fill("New-Pass-456!");
    await page.getByTestId("password-confirm-input").fill("New-Pass-456!");
    await page.getByTestId("password-submit-button").click();

    // Inline, on the current-password field — not a page-level alert.
    await expect(page.getByTestId("password-current-error")).toBeVisible();

    // The 401 must not be mistaken for an expired session: still on the page.
    await expect(page).toHaveURL(/account\/security/);
    await expect(page.getByTestId("password-submit-button")).toBeVisible();
  });

  test("mismatched confirmation is caught client-side before any request", async ({
    page,
  }) => {
    const { org } = await seedUserWithOrg(page);
    await seedBrowserSession(page, { ...org, slug: org.slug });

    let changeRequests = 0;
    page.on("request", (req) => {
      if (req.url().includes("/auth/change-password")) changeRequests++;
    });

    await page.goto(`orgs/${org.slug}/account/security`);
    await page.waitForLoadState("networkidle");

    await page.getByTestId("password-current-input").fill(OLD_PASSWORD);
    await page.getByTestId("password-new-input").fill("New-Pass-456!");
    await page.getByTestId("password-confirm-input").fill("New-Pass-789!");
    await page.getByTestId("password-submit-button").click();

    await expect(page.getByTestId("password-form-error")).toBeVisible();
    expect(changeRequests).toBe(0);
  });

  test("a too-short new password is caught client-side", async ({ page }) => {
    const { org } = await seedUserWithOrg(page);
    await seedBrowserSession(page, { ...org, slug: org.slug });

    let changeRequests = 0;
    page.on("request", (req) => {
      if (req.url().includes("/auth/change-password")) changeRequests++;
    });

    await page.goto(`orgs/${org.slug}/account/security`);
    await page.waitForLoadState("networkidle");

    await page.getByTestId("password-current-input").fill(OLD_PASSWORD);
    await page.getByTestId("password-new-input").fill("short");
    await page.getByTestId("password-confirm-input").fill("short");
    await page.getByTestId("password-submit-button").click();

    await expect(page.getByTestId("password-form-error")).toBeVisible();
    expect(changeRequests).toBe(0);
  });
});
