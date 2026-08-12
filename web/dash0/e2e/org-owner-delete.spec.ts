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
  type Page = import("@playwright/test").Page;

  interface CreatedOrg {
    slug: string;
    accessToken: string;
    refreshToken?: string;
    expiresIn?: number;
  }

  // seedUser creates a throwaway user through the test-only endpoint and logs
  // them in, returning their (org-less) session.
  async function seedUser(page: Page, stamp: number) {
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

    return { email, password, accessToken: session.accessToken };
  }

  // createOrg creates an organization owned by the bearer of `token` and
  // returns the org-scoped session the API mints for it.
  async function createOrg(
    page: Page,
    token: string,
    slug: string,
  ): Promise<CreatedOrg> {
    const resp = await page.request.post(`${API_BASE}/api/v1/orgs`, {
      headers: { Authorization: `Bearer ${token}` },
      data: { name: `Owned Co ${slug}`, slug },
    });
    expect(resp.status()).toBe(201);

    return (await resp.json()) as CreatedOrg;
  }

  // seedBrowserSession puts an API-minted session into the browser, the way a
  // real login would have.
  async function seedBrowserSession(page: Page, org: CreatedOrg) {
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
  }

  // seedOwnedOrg creates a user, logs them in, creates an org through the API,
  // and seeds the browser with the returned org-scoped session.
  async function seedOwnedOrg(page: Page) {
    const stamp = Date.now() + Math.floor(Math.random() * 1000);
    const { email, accessToken } = await seedUser(page, stamp);
    const org = await createOrg(page, accessToken, `own-${stamp.toString(36)}`);

    await seedBrowserSession(page, org);

    return { email, orgSlug: org.slug, ownerToken: org.accessToken };
  }

  // deleteOrgThroughDangerZone walks the danger-zone confirmation exactly as a
  // user would, and returns once the confirm button has been clicked.
  async function deleteOrgThroughDangerZone(page: Page, orgSlug: string) {
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
  }

  // expectStillSignedIn is the assertion the old test was missing: the URL
  // leaving the deleted org passed for BOTH the intended flow and the buggy
  // logout-and-bounce-to-/login. This one only passes when the session is
  // genuinely still in the browser and nothing raised "session expired".
  async function expectStillSignedIn(page: Page) {
    const url = new URL(page.url());
    expect(url.searchParams.get("session_expired")).toBeNull();
    expect(url.pathname).not.toContain("/login");

    const token = await page.evaluate(() =>
      localStorage.getItem("solidping_session_token"),
    );
    expect(token, "the deleter must still hold an access token").toBeTruthy();

    await expect(page.getByTestId("login-title")).toHaveCount(0);
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

  // Spec 2026-08-10-03 / issue #206: deleting your LAST org must leave you
  // signed in on the /no-org empty state — not bounced to a login page for a
  // slug that no longer exists.
  test("deleting the last org lands the still-signed-in owner on /no-org", async ({
    page,
  }) => {
    const { orgSlug, ownerToken } = await seedOwnedOrg(page);

    await deleteOrgThroughDangerZone(page, orgSlug);

    await page.waitForURL((url) => url.pathname.endsWith("/no-org"), {
      timeout: 15000,
    });

    await expectStillSignedIn(page);

    // The empty state really rendered (not a blank authenticated shell).
    await expect(page.getByTestId("create-org-submit")).toBeVisible();

    // Nothing keeps pointing at the dead slug, or the next reload / expired
    // redirect would send the user back to its 404ing login page.
    const storedOrg = await page.evaluate(() =>
      localStorage.getItem("solidping_org"),
    );
    expect(storedOrg).toBeFalsy();

    // Server-side control: the org is really gone. The token used here is the
    // ORIGINAL one, still scoped to the deleted org, so the org-scoped
    // middleware lets it through on slug match and the lookup is what 404s (a
    // token scoped elsewhere would 403 on the mismatch and prove nothing).
    const afterResp = await page.request.get(
      `${API_BASE}/api/v1/orgs/${orgSlug}/checks`,
      { headers: { Authorization: `Bearer ${ownerToken}` } },
    );
    expect(afterResp.status()).toBe(404);

    // And that same stale token still authenticates: /auth/me degrades to an
    // org-less 200 instead of the 401 that used to log a reloading tab out.
    const meResp = await page.request.get(`${API_BASE}/api/v1/auth/me`, {
      headers: { Authorization: `Bearer ${ownerToken}` },
    });
    expect(meResp.status()).toBe(200);
    expect(
      ((await meResp.json()) as { organization?: unknown }).organization,
    ).toBeFalsy();
  });

  // The multi-org half of the same spec: with another org left, the owner is
  // switched into it rather than dropped anywhere.
  test("deleting one of two orgs switches the owner into the surviving one", async ({
    page,
  }) => {
    const stamp = Date.now() + Math.floor(Math.random() * 1000);
    const { accessToken } = await seedUser(page, stamp);

    const keeper = await createOrg(page, accessToken, `keep-${stamp.toString(36)}`);
    const doomed = await createOrg(
      page,
      keeper.accessToken,
      `gone-${stamp.toString(36)}`,
    );

    // The browser is sitting in the org that is about to be deleted.
    await seedBrowserSession(page, doomed);

    await deleteOrgThroughDangerZone(page, doomed.slug);

    await page.waitForURL(
      (url) => url.pathname.includes(`/orgs/${keeper.slug}`),
      { timeout: 15000 },
    );

    await expectStillSignedIn(page);

    // The surviving org's dashboard really rendered for this session.
    await expect(page.getByTestId("app-sidebar")).toBeVisible();

    const storedOrg = await page.evaluate(() =>
      localStorage.getItem("solidping_org"),
    );
    expect(storedOrg).toBe(keeper.slug);

    // And the adopted session is scoped to the survivor: the token now in the
    // browser is accepted by the keeper org's API.
    const token = await page.evaluate(() =>
      localStorage.getItem("solidping_session_token"),
    );
    const keeperResp = await page.request.get(
      `${API_BASE}/api/v1/orgs/${keeper.slug}/checks`,
      { headers: { Authorization: `Bearer ${token}` } },
    );
    expect(keeperResp.status()).toBe(200);

    // Control: the deleted org is genuinely gone. Probed with the ORIGINAL
    // doomed-scoped token — the org-scoped middleware only reaches the lookup
    // (and its 404) when the token's org matches the path; the adopted
    // keeper-scoped token would 403 on the mismatch and prove nothing.
    const goneResp = await page.request.get(
      `${API_BASE}/api/v1/orgs/${doomed.slug}/checks`,
      { headers: { Authorization: `Bearer ${doomed.accessToken}` } },
    );
    expect(goneResp.status()).toBe(404);
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
