import { test, expect, API_BASE, uniqueStamp } from "./fixtures";

// Spec 2026-09-30-08: change a user's sign-in email, from their own profile
// page, or as a super admin from the system user directory.
//
// Every test seeds a throwaway user + org: changing the shared test@test.com
// address would break every other spec in the suite.
const PASSWORD = "Email-Pass-123!";

async function seedUserWithOrg(page: import("@playwright/test").Page) {
  const stamp = uniqueStamp();
  const email = `acct-email-${stamp}@unknown.example`;

  const createUserResp = await page.request.post(`${API_BASE}/api/v1/test/users`, {
    data: { email, password: PASSWORD, name: "Acct Email User" },
  });
  if (createUserResp.status() !== 201) {
    test.skip(
      true,
      `test user-seed endpoint unavailable (server not in SP_RUNMODE=test?): ${createUserResp.status()}`,
    );
  }

  const loginResp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { email, password: PASSWORD },
  });
  expect(loginResp.status()).toBe(200);
  const login = (await loginResp.json()) as { accessToken: string };

  const orgSlug = `ae1-${stamp}`;
  const createOrgResp = await page.request.post(`${API_BASE}/api/v1/orgs`, {
    headers: { Authorization: `Bearer ${login.accessToken}` },
    data: { name: `Acct Email Co ${stamp}`, slug: orgSlug },
  });
  expect(createOrgResp.status()).toBe(201);
  const org = (await createOrgResp.json()) as {
    slug: string;
    accessToken: string;
    refreshToken?: string;
    expiresIn?: number;
    user: { uid: string };
  };

  return { email, stamp, org };
}

async function seedBrowserSession(
  page: import("@playwright/test").Page,
  session: { accessToken: string; refreshToken?: string; expiresIn?: number; slug: string },
) {
  await page.addInitScript(
    ({ accessToken, refreshToken, expiresIn, slug }) => {
      // Only seed once: after an explicit logout the test must stay logged out.
      if (sessionStorage.getItem("e2e_seeded")) return;
      sessionStorage.setItem("e2e_seeded", "1");
      localStorage.setItem("solidping_session_token", accessToken);
      if (refreshToken) localStorage.setItem("solidping_refresh_token", refreshToken);
      if (expiresIn) {
        localStorage.setItem("solidping_expires_at", String(Date.now() + expiresIn * 1000));
        localStorage.setItem("solidping_expires_in", String(expiresIn));
      }
      localStorage.setItem("solidping_org", slug);
    },
    {
      accessToken: session.accessToken,
      refreshToken: session.refreshToken ?? "",
      expiresIn: session.expiresIn ?? 0,
      slug: session.slug,
    },
  );
}

test.describe("Account > Profile > Email", () => {
  test("changes the email, then logs out and back in with the new address", async ({ page }) => {
    const { email, stamp, org } = await seedUserWithOrg(page);
    await seedBrowserSession(page, org);

    await page.goto(`orgs/${org.slug}/account/profile`);
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("profile-current-email")).toHaveText(email);

    const newEmail = `acct-email-new-${stamp}@unknown.example`;

    // A wrong password is refused inline, and nothing changes.
    await page.getByTestId("profile-new-email").fill(newEmail);
    await page.getByTestId("profile-email-password").fill("wrong-password-1");
    await page.getByTestId("profile-change-email").click();
    await expect(page.getByTestId("profile-email-error")).toHaveText(
      "The current password is incorrect.",
    );
    // A 403 must never bounce to the login page.
    await expect(page).toHaveURL(/account\/profile/);
    await expect(page.getByTestId("profile-current-email")).toHaveText(email);

    // The right password changes it.
    await page.getByTestId("profile-email-password").fill(PASSWORD);
    await page.getByTestId("profile-change-email").click();
    await expect(page.getByTestId("profile-email-saved")).toBeVisible();
    await expect(page.getByTestId("profile-current-email")).toHaveText(newEmail);

    // Log out, then log in through the login page with the new address.
    await page.evaluate(() => localStorage.clear());
    await page.goto(`orgs/${org.slug}/login`);
    await page.waitForLoadState("networkidle");
    await page.getByTestId("login-email").fill(newEmail);
    await page.getByTestId("login-password").fill(PASSWORD);
    await page.getByTestId("login-submit").click();
    await page.waitForURL((url) => !url.pathname.includes("login"), { timeout: 10000 });

    // The old address no longer signs in.
    const oldLogin = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { email, password: PASSWORD },
    });
    expect(oldLogin.status()).not.toBe(200);
  });

  test("a duplicate address is refused inline", async ({ page }) => {
    const first = await seedUserWithOrg(page);
    const second = await seedUserWithOrg(page);
    await seedBrowserSession(page, second.org);

    await page.goto(`orgs/${second.org.slug}/account/profile`);
    await page.waitForLoadState("networkidle");

    await page.getByTestId("profile-new-email").fill(first.email.toUpperCase());
    await page.getByTestId("profile-email-password").fill(PASSWORD);
    await page.getByTestId("profile-change-email").click();
    await expect(page.getByTestId("profile-email-error")).toHaveText(
      "This email address is already used by another account.",
    );
  });
});

test.describe("Server > Users > Edit", () => {
  test("a super admin changes another user's email from the edit page", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const { email, stamp } = await seedUserWithOrg(page);

    await page.goto("orgs/test/server/users");
    await page.waitForLoadState("networkidle");
    await page.getByTestId("users-search").fill(email);
    await page.waitForLoadState("networkidle");

    const row = page.getByRole("row", { name: new RegExp(email.replace(/[\\.+]/g, "\\$&")) });
    await expect(row).toBeVisible();
    await row.locator('[data-testid^="users-edit-"]').click();
    await expect(page).toHaveURL(/server\/users\/[0-9a-f-]+$/);

    const newEmail = `acct-email-admin-${stamp}@unknown.example`;
    await expect(page.getByTestId("user-edit-email")).toHaveValue(email);
    await page.getByTestId("user-edit-email").fill(newEmail);
    await page.getByTestId("user-edit-save").click();
    await expect(page.getByTestId("user-edit-saved")).toBeVisible();

    const newLogin = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { email: newEmail, password: PASSWORD },
    });
    expect(newLogin.status()).toBe(200);
  });
});
