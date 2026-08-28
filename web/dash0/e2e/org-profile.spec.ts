import { test, expect, API_BASE } from "./fixtures";

// Spec 2026-08-08-12: an owner edits the organization's profile — name, URL
// slug and logo — from the settings page. Renaming lands the user
// authenticated on the new URL, and the previous slug keeps redirecting.
//
// Like org-owner-delete.spec.ts, each test works from a throwaway org created
// through the real POST /api/v1/orgs by a freshly seeded zero-org user, so a
// rename here cannot disturb the shared `test` fixture org.
test.describe("Organization profile", () => {
  async function seedOwnedOrg(page: import("@playwright/test").Page) {
    const stamp = Date.now() + Math.floor(Math.random() * 1000);
    const email = `profile-${stamp}@unknown.example`;
    const password = "Strong-Pass-123!";

    const createUserResp = await page.request.post(
      `${API_BASE}/api/v1/test/users`,
      { data: { email, password, name: "Profile Owner" } },
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

    const orgSlug = `prof-${stamp.toString(36)}`;
    const createOrgResp = await page.request.post(`${API_BASE}/api/v1/orgs`, {
      headers: { Authorization: `Bearer ${session.accessToken}` },
      data: { name: `Profile Co ${stamp}`, slug: orgSlug },
    });
    expect(createOrgResp.status()).toBe(201);

    const org = (await createOrgResp.json()) as {
      slug: string;
      accessToken: string;
      refreshToken?: string;
      expiresIn?: number;
    };

    await seedBrowserSession(page, org);

    return { email, password, orgSlug: org.slug, ownerToken: org.accessToken };
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

  test("an owner renames the org and stays authenticated on the new URL", async ({
    page,
  }) => {
    const { orgSlug } = await seedOwnedOrg(page);
    const renamed = `${orgSlug}-x`;

    await page.goto(`orgs/${orgSlug}/organization/settings`);
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("org-profile-card")).toBeVisible();

    // No warning until the slug actually changes.
    await expect(page.getByTestId("org-profile-rename-warning")).toHaveCount(0);

    await page.getByTestId("org-profile-slug").fill(renamed);
    await expect(page.getByTestId("org-profile-rename-warning")).toBeVisible();

    await page.getByTestId("org-profile-save").click();

    // The app lands on the new slug's settings URL, still signed in — the
    // re-minted session was adopted, otherwise every request would 403.
    await page.waitForURL((url) => url.pathname.includes(`/orgs/${renamed}/`), {
      timeout: 15000,
    });
    await expect(page.getByTestId("org-profile-card")).toBeVisible();
    await expect(page.getByTestId("org-profile-slug")).toHaveValue(renamed);

    // The previous slug is not dead: the API permanently redirects it to the
    // new one. The redirect is inspected, not followed.
    const redirected = await page.request.get(
      `${API_BASE}/api/v1/status-pages/${orgSlug}`,
      { maxRedirects: 0 },
    );
    expect(redirected.status()).toBe(301);
    expect(redirected.headers()["location"]).toBe(
      `/api/v1/status-pages/${renamed}`,
    );

    // Positive control: the current slug is served, not redirected.
    const direct = await page.request.get(
      `${API_BASE}/api/v1/status-pages/${renamed}`,
      { maxRedirects: 0 },
    );
    expect(direct.status()).not.toBe(301);
  });

  test("editing the name never touches the slug", async ({ page }) => {
    const { orgSlug } = await seedOwnedOrg(page);

    await page.goto(`orgs/${orgSlug}/organization/settings`);
    await page.waitForLoadState("networkidle");

    const nameField = page.getByTestId("org-profile-name");
    const slugField = page.getByTestId("org-profile-slug");

    await expect(slugField).toHaveValue(orgSlug);

    // A name that would slugify to something entirely different if the field
    // were still derived.
    await nameField.fill("Totally Different Name");
    await expect(slugField).toHaveValue(orgSlug);

    // Nothing moved, so the URL-change warning must stay away.
    await expect(page.getByTestId("org-profile-rename-warning")).toHaveCount(0);

    await page.getByTestId("org-profile-save").click();

    // The save is a pure rename of the display name: the app stays on the same
    // slug (a slug change would have navigated away) and the field still holds
    // the original slug after the reload of the org identity.
    await expect(page.getByTestId("org-profile-name")).toHaveValue(
      "Totally Different Name",
      { timeout: 15000 },
    );
    expect(page.url()).toContain(`/orgs/${orgSlug}/`);
    await expect(slugField).toHaveValue(orgSlug);

    // Positive control: the slug field itself is still editable, so the
    // assertions above prove decoupling, not a frozen input.
    await slugField.fill(`${orgSlug}-y`);
    await expect(slugField).toHaveValue(`${orgSlug}-y`);
    await expect(page.getByTestId("org-profile-rename-warning")).toBeVisible();
  });

  // A 1x1 transparent PNG, reused by every logo-upload test below.
  const PNG_LOGO = Buffer.from(
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==",
    "base64",
  );

  async function uploadLogo(page: import("@playwright/test").Page) {
    await page.getByTestId("org-profile-logo-file").setInputFiles({
      name: "logo.png",
      mimeType: "image/png",
      buffer: PNG_LOGO,
    });

    const preview = page.getByTestId("org-profile-logo-preview");
    await expect(preview).toBeVisible({ timeout: 15000 });
    await expect(preview).toHaveAttribute("src", /\/pub\/assets\//);
  }

  test("an owner uploads a logo and it shows in the sidebar", async ({
    page,
  }) => {
    const { orgSlug } = await seedOwnedOrg(page);

    await page.goto(`orgs/${orgSlug}/organization/settings`);
    await page.waitForLoadState("networkidle");

    // Control: no org logo yet, so the sidebar shows the product mark.
    await expect(page.getByTestId("sidebar-org-logo")).toHaveCount(0);

    await uploadLogo(page);

    // The org identity in the sidebar picks it up too.
    await expect(page.getByTestId("sidebar-org-logo")).toBeVisible({
      timeout: 15000,
    });

    // Spec 2026-08-28-13: the relative /pub/assets path must never land in
    // the type="url" input — it would trip native constraint validation and
    // silently block the whole form's submit. Instead the field shows an
    // "Uploaded file" badge, and the URL input isn't even in the DOM.
    await expect(page.getByTestId("org-profile-logo-badge")).toBeVisible();
    await expect(page.getByTestId("org-profile-logo-badge")).toHaveText(
      "Uploaded file",
    );
    await expect(page.getByTestId("org-profile-logo-url")).toHaveCount(0);

    // The uploaded state can be cleared, same as before.
    await page.getByTestId("org-profile-logo-clear").click();
    await expect(page.getByTestId("org-profile-logo-badge")).toHaveCount(0, {
      timeout: 15000,
    });
    await expect(page.getByTestId("org-profile-logo-preview")).toHaveCount(0);
  });

  test("saving a name change still succeeds after uploading a logo", async ({
    page,
  }) => {
    // This is the regression this spec exists for: before the fix, the
    // relative /pub/assets path from the upload landed in the type="url"
    // input, native constraint validation refused to submit the form at
    // all, and an unrelated name edit could never be saved again.
    const { orgSlug } = await seedOwnedOrg(page);

    await page.goto(`orgs/${orgSlug}/organization/settings`);
    await page.waitForLoadState("networkidle");

    await uploadLogo(page);

    await page.getByTestId("org-profile-name").fill("Renamed After Upload");
    await page.getByTestId("org-profile-save").click();

    await expect(page.getByTestId("org-profile-error")).toHaveCount(0);
    await expect(page.getByTestId("org-profile-name")).toHaveValue(
      "Renamed After Upload",
      { timeout: 15000 },
    );

    // The logo is untouched by the save — still the uploaded file, not
    // silently cleared by an omitted/mismatched logoUrl.
    await expect(page.getByTestId("org-profile-logo-badge")).toBeVisible();
    await expect(page.getByTestId("org-profile-logo-preview")).toHaveAttribute(
      "src",
      /\/pub\/assets\//,
    );
  });

  test("switching an uploaded logo to an external URL and saving works", async ({
    page,
  }) => {
    const { orgSlug } = await seedOwnedOrg(page);

    await page.goto(`orgs/${orgSlug}/organization/settings`);
    await page.waitForLoadState("networkidle");

    await uploadLogo(page);

    await page.getByTestId("org-profile-logo-use-url").click();
    const urlField = page.getByTestId("org-profile-logo-url");
    await expect(urlField).toBeVisible();
    await expect(urlField).toHaveValue("");
    await expect(page.getByTestId("org-profile-logo-badge")).toHaveCount(0);

    const externalUrl = "https://example.com/acme-logo.png";
    await urlField.fill(externalUrl);
    await page.getByTestId("org-profile-save").click();

    await expect(page.getByTestId("org-profile-error")).toHaveCount(0);
    await expect(urlField).toHaveValue(externalUrl, { timeout: 15000 });
    await expect(page.getByTestId("org-profile-logo-preview")).toHaveAttribute(
      "src",
      externalUrl,
    );
    await expect(page.getByTestId("org-profile-logo-badge")).toHaveCount(0);
  });

  test("a non-owner admin sees no profile card and is refused by the API", async ({
    page,
  }) => {
    const { orgSlug, ownerToken } = await seedOwnedOrg(page);

    const stamp = Date.now() + Math.floor(Math.random() * 1000);
    const adminEmail = `profile-admin-${stamp}@unknown.example`;
    const adminPassword = "Strong-Pass-123!";

    const createAdminResp = await page.request.post(
      `${API_BASE}/api/v1/test/users`,
      {
        data: {
          email: adminEmail,
          password: adminPassword,
          name: "Profile Admin",
        },
      },
    );
    if (createAdminResp.status() !== 201) {
      test.skip(true, "test user-seed endpoint unavailable");
    }

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
    const patchResp = await page.request.patch(
      `${API_BASE}/api/v1/orgs/${orgSlug}`,
      {
        headers: { Authorization: `Bearer ${adminSession.accessToken}` },
        data: { name: "Admin Was Here" },
      },
    );
    expect(patchResp.status()).toBe(403);

    await seedBrowserSession(page, adminSession);

    await page.goto(`orgs/${orgSlug}/organization/settings`);
    await page.waitForLoadState("networkidle");

    // Positive control: the page rendered for the admin, so the absent card
    // below is about the role, not a blank page.
    await expect(page.getByTestId("auto-join-pattern-save")).toBeVisible();
    await expect(page.getByTestId("org-profile-card")).toHaveCount(0);
  });
});
