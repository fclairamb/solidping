import { test, expect, API_BASE } from "./fixtures";

// Spec 2026-08-08-11: the creator of an organization becomes its OWNER, only an
// owner sees the settings danger zone, and deleting the org through it lands the
// user outside the (now 404ing) org.
//
// The org is created through the real POST /api/v1/orgs by a freshly seeded
// zero-org user — the same technique create-org.spec.ts uses, and for the same
// reason (registration is not reachable in this environment). Working from a
// throwaway org rather than the shared `test` fixture org means the delete test
// can actually delete something without breaking every other spec.
test.describe("Organization owner and deletion", () => {
  // seedOwnedOrg creates a user, logs them in, creates an org through the API,
  // and seeds the browser with the returned org-scoped session.
  async function seedOwnedOrg(page: import("@playwright/test").Page) {
    const stamp = Date.now() + Math.floor(Math.random() * 1000);
    const email = `owner-${stamp}@unknown.example`;
    const password = "Strong-Pass-123!";

    const createUserResp = await page.request.post(
      `${API_BASE}/api/v1/test/users`,
      { data: { email, password, name: "Owner User" } },
    );
    if (createUserResp.status() !== 201) {
      test.skip(
        true,
        `test user-seed endpoint unavailable (server not in SP_RUNMODE=test?): ${createUserResp.status()}`,
      );
    }

    const loginResp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { email, password },
    });
    expect(loginResp.status()).toBe(200);

    const session = (await loginResp.json()) as { accessToken: string };

    const orgSlug = `own-${stamp.toString(36)}`;
    const createOrgResp = await page.request.post(`${API_BASE}/api/v1/orgs`, {
      headers: { Authorization: `Bearer ${session.accessToken}` },
      data: { name: `Owned Co ${stamp}`, slug: orgSlug },
    });
    expect(createOrgResp.status()).toBe(201);

    const org = (await createOrgResp.json()) as {
      slug: string;
      accessToken: string;
      refreshToken?: string;
      expiresIn?: number;
    };

    await page.addInitScript(
      ({ accessToken, refreshToken, expiresIn, slug }) => {
        localStorage.setItem("solidping_session_token", accessToken as string);
        if (refreshToken) {
          localStorage.setItem(
            "solidping_refresh_token",
            refreshToken as string,
          );
        }
        if (expiresIn) {
          localStorage.setItem(
            "solidping_expires_at",
            String(Date.now() + Number(expiresIn) * 1000),
          );
          localStorage.setItem("solidping_expires_in", String(expiresIn));
        }
        localStorage.setItem("solidping_org", slug as string);
      },
      {
        accessToken: org.accessToken,
        refreshToken: org.refreshToken ?? "",
        expiresIn: org.expiresIn ?? 0,
        slug: org.slug,
      },
    );

    return { email, orgSlug: org.slug, ownerToken: org.accessToken };
  }

  test("the creator is shown as Owner on the members page", async ({ page }) => {
    const { email, orgSlug } = await seedOwnedOrg(page);

    await page.goto(`orgs/${orgSlug}/organization/members`);
    await page.waitForLoadState("networkidle");

    // The creator's own row shows the Owner role (the label rendered by the
    // members.role.owner i18n key in the English locale is exactly "Owner").
    const roleSelect = page.getByTestId(`member-role-${email}`);
    await expect(roleSelect).toBeVisible();
    await expect(roleSelect).toHaveText("Owner");
  });

  test("an owner deletes the org from the danger zone and lands outside it", async ({
    page,
  }) => {
    const { orgSlug, ownerToken } = await seedOwnedOrg(page);

    await page.goto(`orgs/${orgSlug}/organization/settings`);
    await page.waitForLoadState("networkidle");

    // The danger zone is owner-only and visible here.
    await expect(page.getByTestId("org-danger-zone")).toBeVisible();

    await page.getByTestId("delete-org").click();

    const confirmButton = page.getByTestId("delete-org-confirm");
    await expect(confirmButton).toBeVisible();

    // The confirm action stays disabled until the slug is retyped exactly.
    await expect(confirmButton).toBeDisabled();

    await page.getByTestId("delete-org-input").fill(`${orgSlug}-wrong`);
    await expect(confirmButton).toBeDisabled();

    await page.getByTestId("delete-org-input").fill(orgSlug);
    await expect(confirmButton).toBeEnabled();

    await confirmButton.click();

    // The user is navigated out of the deleted org.
    await page.waitForURL((url) => !url.pathname.includes(`/orgs/${orgSlug}/`), {
      timeout: 15000,
    });

    // Server-side: the org is really gone — the owner's own token now 404s.
    const afterResp = await page.request.get(
      `${API_BASE}/api/v1/orgs/${orgSlug}/checks`,
      { headers: { Authorization: `Bearer ${ownerToken}` } },
    );
    expect(afterResp.status()).toBe(404);
  });

  test("a non-owner admin sees no danger zone and is refused by the API", async ({
    page,
  }) => {
    const { orgSlug, ownerToken } = await seedOwnedOrg(page);

    const stamp = Date.now() + Math.floor(Math.random() * 1000);
    const adminEmail = `admin-${stamp}@unknown.example`;
    const adminPassword = "Strong-Pass-123!";

    const createAdminResp = await page.request.post(
      `${API_BASE}/api/v1/test/users`,
      { data: { email: adminEmail, password: adminPassword, name: "Admin User" } },
    );
    if (createAdminResp.status() !== 201) {
      test.skip(true, "test user-seed endpoint unavailable");
    }

    // The owner adds them as a plain admin.
    const addResp = await page.request.post(
      `${API_BASE}/api/v1/orgs/${orgSlug}/members`,
      {
        headers: { Authorization: `Bearer ${ownerToken}` },
        data: { email: adminEmail, role: "admin" },
      },
    );
    expect(addResp.status()).toBe(201);

    const adminLogin = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { org: orgSlug, email: adminEmail, password: adminPassword },
    });
    expect(adminLogin.status()).toBe(200);

    const adminSession = (await adminLogin.json()) as {
      accessToken: string;
      refreshToken?: string;
      expiresIn?: number;
    };

    // Server-side proof first: the admin is refused even bypassing the UI.
    const deleteResp = await page.request.delete(
      `${API_BASE}/api/v1/orgs/${orgSlug}`,
      {
        headers: { Authorization: `Bearer ${adminSession.accessToken}` },
        data: { slug: orgSlug },
      },
    );
    expect(deleteResp.status()).toBe(403);

    // Swap the browser session to the admin and confirm the card is hidden.
    await page.addInitScript(
      ({ accessToken, refreshToken, expiresIn }) => {
        localStorage.setItem("solidping_session_token", accessToken as string);
        if (refreshToken) {
          localStorage.setItem(
            "solidping_refresh_token",
            refreshToken as string,
          );
        }
        if (expiresIn) {
          localStorage.setItem(
            "solidping_expires_at",
            String(Date.now() + Number(expiresIn) * 1000),
          );
          localStorage.setItem("solidping_expires_in", String(expiresIn));
        }
      },
      {
        accessToken: adminSession.accessToken,
        refreshToken: adminSession.refreshToken ?? "",
        expiresIn: adminSession.expiresIn ?? 0,
      },
    );

    await page.goto(`orgs/${orgSlug}/organization/settings`);
    await page.waitForLoadState("networkidle");

    // Positive control: the settings page itself rendered for the admin, so the
    // absent danger zone below is about the role, not a blank page.
    await expect(page.getByTestId("auto-join-pattern-save")).toBeVisible();
    await expect(page.getByTestId("org-danger-zone")).toHaveCount(0);
  });
});
