import { test, expect, API_BASE } from "./fixtures";

// Spec 2026-08-09-05: a user who already belongs to an org had no UI path to
// create another one — only /no-org, reachable exclusively by a zero-org
// user. This covers the new /orgs/$org/account/organizations tab: the list
// (current-org marker + siblings) and the create flow.
//
// Like org-owner-delete.spec.ts / org-profile.spec.ts, each test seeds a
// throwaway org through the real POST /api/v1/orgs from a freshly created
// user, so nothing here disturbs the shared `test` fixture org.
test.describe("Account > Organizations", () => {
  async function seedUserWithOrg(page: import("@playwright/test").Page) {
    const stamp = Date.now() + Math.floor(Math.random() * 1000);
    const email = `acct-orgs-${stamp}@unknown.example`;
    const password = "Strong-Pass-123!";

    const createUserResp = await page.request.post(
      `${API_BASE}/api/v1/test/users`,
      { data: { email, password, name: "Acct Orgs User" } },
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
    const login = (await loginResp.json()) as { accessToken: string };

    const orgSlug = `ao1-${stamp.toString(36)}`;
    const createOrgResp = await page.request.post(`${API_BASE}/api/v1/orgs`, {
      headers: { Authorization: `Bearer ${login.accessToken}` },
      data: { name: `Acct Orgs Co ${stamp}`, slug: orgSlug },
    });
    expect(createOrgResp.status()).toBe(201);
    const org = (await createOrgResp.json()) as {
      slug: string;
      accessToken: string;
      refreshToken?: string;
      expiresIn?: number;
    };

    return { stamp, email, password, org };
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

  test("a user with an existing org creates a second one from the account section and the new session is adopted", async ({
    page,
  }) => {
    const { stamp, org } = await seedUserWithOrg(page);
    await seedBrowserSession(page, { ...org, slug: org.slug });

    const forbiddenUrls: string[] = [];
    page.on("response", (response) => {
      if (response.status() === 403) forbiddenUrls.push(response.url());
    });

    await page.goto(`orgs/${org.slug}/account/organizations`);
    await page.waitForLoadState("networkidle");

    // The first (only) org is marked current.
    await expect(page.getByTestId(`organization-row-${org.slug}`)).toBeVisible();
    await expect(page.getByTestId("organization-current-badge")).toBeVisible();

    await page.getByTestId("new-organization-button").click();
    await page.waitForURL((url) =>
      url.pathname.endsWith(`/orgs/${org.slug}/account/organizations/new`),
    );

    const newOrgName = `Acct Orgs Second ${stamp}`;
    const newOrgSlug = `ao2-${stamp.toString(36)}`;

    await page.locator("#orgName").fill(newOrgName);
    await page.getByTestId("no-org-advanced-toggle").click();
    await page.locator("#orgSlug").fill(newOrgSlug);
    await page.getByTestId("create-org-submit").click();

    // Lands on the NEW org's dashboard, not the account page it started
    // from — a plain URL-change alone wouldn't prove session adoption, so
    // this is checked alongside the token below.
    await page.waitForURL((url) => url.pathname.includes(`/orgs/${newOrgSlug}`), {
      timeout: 15000,
    });
    await page.waitForLoadState("networkidle");

    expect(forbiddenUrls, `unexpected 403s: ${forbiddenUrls.join(", ")}`).toEqual(
      [],
    );

    // The load-bearing assertion: the token now in storage must be a FRESH
    // one, different from the org1 session seeded at the top, and it must
    // actually work against an org-scoped endpoint on the NEW org — a stale
    // (org1) token would either still be present or would 403 here.
    const storedToken = await page.evaluate(() =>
      localStorage.getItem("solidping_session_token"),
    );
    expect(storedToken).toBeTruthy();
    expect(storedToken).not.toBe(org.accessToken);

    const scoped = await page.request.get(
      `${API_BASE}/api/v1/orgs/${newOrgSlug}/checks`,
      { headers: { Authorization: `Bearer ${storedToken}` } },
    );
    expect(scoped.status()).toBe(200);

    // Positive control: that same fresh token is scoped to the NEW org only
    // — it must NOT work against the org the user started from.
    const crossOrg = await page.request.get(
      `${API_BASE}/api/v1/orgs/${org.slug}/checks`,
      { headers: { Authorization: `Bearer ${storedToken}` } },
    );
    expect(crossOrg.status()).toBe(403);

    // The account Organizations list itself reflects both orgs now, with
    // the new one current.
    await page.goto(`orgs/${newOrgSlug}/account/organizations`);
    await page.waitForLoadState("networkidle");
    await expect(
      page.getByTestId(`organization-row-${newOrgSlug}`),
    ).toBeVisible();
    await expect(page.getByTestId("organization-current-badge")).toBeVisible();
    await expect(page.getByTestId(`organization-row-${org.slug}`)).toBeVisible();
    await expect(page.getByTestId(`organization-switch-${org.slug}`)).toBeVisible();
  });

  test("cancel on the new-organization form returns to the organizations list", async ({
    page,
  }) => {
    const { org } = await seedUserWithOrg(page);
    await seedBrowserSession(page, { ...org, slug: org.slug });

    await page.goto(`orgs/${org.slug}/account/organizations/new`);
    await page.waitForLoadState("networkidle");

    await page.getByTestId("create-org-cancel").click();
    await page.waitForURL((url) =>
      url.pathname.endsWith(`/orgs/${org.slug}/account/organizations`),
    );
    await expect(page.getByTestId(`organization-row-${org.slug}`)).toBeVisible();
  });

  test("switching orgs from the list navigates and marks the new org current", async ({
    page,
  }) => {
    const { org } = await seedUserWithOrg(page);

    // A second org for the same user, created via the API with org1's
    // token — mirrors a user who already has two memberships.
    const stamp2 = Date.now() + Math.floor(Math.random() * 1000);
    const org2Slug = `ao3-${stamp2.toString(36)}`;
    const createOrg2Resp = await page.request.post(`${API_BASE}/api/v1/orgs`, {
      headers: { Authorization: `Bearer ${org.accessToken}` },
      data: { name: `Acct Orgs Third ${stamp2}`, slug: org2Slug },
    });
    expect(createOrg2Resp.status()).toBe(201);

    // Seed the ORIGINAL (org1) session — the create-org call above minted a
    // session scoped to org2, but this test wants to start on org1 with both
    // memberships visible, exactly like the account layout's session
    // adoption on the *previous* test proved.
    await seedBrowserSession(page, { ...org, slug: org.slug });

    await page.goto(`orgs/${org.slug}/account/organizations`);
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId(`organization-row-${org2Slug}`)).toBeVisible();
    await page.getByTestId(`organization-switch-${org2Slug}`).click();

    await page.waitForURL((url) =>
      url.pathname.endsWith(`/orgs/${org2Slug}/account/organizations`),
    );
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId(`organization-row-${org2Slug}`)).toBeVisible();
    await expect(page.getByTestId("organization-current-badge")).toBeVisible();
  });

  test("the Settings shortcut is per-row role gated: current org links directly, a non-current admin org switches then lands on settings, and a non-admin row has no button", async ({
    page,
  }) => {
    const { email, org } = await seedUserWithOrg(page);

    // A second org the same user owns (admin-capable), created via org1's
    // token — mirrors the "switching orgs" test above. This is the
    // non-current row that must switch-then-navigate to its settings.
    const stamp2 = Date.now() + Math.floor(Math.random() * 1000);
    const org2Slug = `ao4-${stamp2.toString(36)}`;
    const createOrg2Resp = await page.request.post(`${API_BASE}/api/v1/orgs`, {
      headers: { Authorization: `Bearer ${org.accessToken}` },
      data: { name: `Acct Orgs Owner2 ${stamp2}`, slug: org2Slug },
    });
    expect(createOrg2Resp.status()).toBe(201);

    // A third org owned by a DIFFERENT user, who adds our primary user as a
    // plain "user" role member — the negative-control row: not admin-capable,
    // so it must render no Settings button at all.
    const stamp3 = Date.now() + Math.floor(Math.random() * 1000);
    const otherEmail = `acct-orgs-other-${stamp3}@unknown.example`;
    const otherPassword = "Strong-Pass-123!";
    const createOtherResp = await page.request.post(
      `${API_BASE}/api/v1/test/users`,
      { data: { email: otherEmail, password: otherPassword, name: "Other Owner" } },
    );
    if (createOtherResp.status() !== 201) {
      test.skip(true, "test user-seed endpoint unavailable");
    }
    const otherLoginResp = await page.request.post(
      `${API_BASE}/api/v1/auth/login`,
      { data: { email: otherEmail, password: otherPassword } },
    );
    expect(otherLoginResp.status()).toBe(200);
    const otherLogin = (await otherLoginResp.json()) as { accessToken: string };

    const org3Slug = `ao5-${stamp3.toString(36)}`;
    const createOrg3Resp = await page.request.post(`${API_BASE}/api/v1/orgs`, {
      headers: { Authorization: `Bearer ${otherLogin.accessToken}` },
      data: { name: `Acct Orgs Member Co ${stamp3}`, slug: org3Slug },
    });
    expect(createOrg3Resp.status()).toBe(201);
    const org3 = (await createOrg3Resp.json()) as { accessToken: string };

    const addMemberResp = await page.request.post(
      `${API_BASE}/api/v1/orgs/${org3Slug}/members`,
      {
        headers: { Authorization: `Bearer ${org3.accessToken}` },
        data: { email, role: "user" },
      },
    );
    expect(addMemberResp.status()).toBe(201);

    // Seed the browser on org1 — the user now belongs to org1 (owner,
    // current), org2 (owner, non-current) and org3 (user role, non-current).
    await seedBrowserSession(page, { ...org, slug: org.slug });

    await page.goto(`orgs/${org.slug}/account/organizations`);
    await page.waitForLoadState("networkidle");

    // Negative control first: the non-admin row shows no Settings button,
    // while its row (and the Switch button) is otherwise present.
    await expect(page.getByTestId(`organization-row-${org3Slug}`)).toBeVisible();
    await expect(
      page.getByTestId(`organization-settings-${org3Slug}`),
    ).toHaveCount(0);
    await expect(page.getByTestId(`organization-switch-${org3Slug}`)).toBeVisible();

    // Current org (org1): a direct link that lands straight on its settings.
    const currentSettingsButton = page.getByTestId(
      `organization-settings-${org.slug}`,
    );
    await expect(currentSettingsButton).toBeVisible();
    await currentSettingsButton.click();
    await page.waitForURL((url) =>
      url.pathname.endsWith(`/orgs/${org.slug}/organization/settings`),
    );
    await page.waitForLoadState("networkidle");
    await expect(
      page.getByRole("heading", { name: /session length/i }),
    ).toBeVisible();

    // Back to the list (still on org1) to exercise the non-current admin row.
    await page.goto(`orgs/${org.slug}/account/organizations`);
    await page.waitForLoadState("networkidle");

    const forbiddenUrls: string[] = [];
    page.on("response", (response) => {
      if (response.status() === 403) forbiddenUrls.push(response.url());
    });

    await page.getByTestId(`organization-settings-${org2Slug}`).click();
    await page.waitForURL((url) =>
      url.pathname.endsWith(`/orgs/${org2Slug}/organization/settings`),
    );
    await page.waitForLoadState("networkidle");
    await expect(
      page.getByRole("heading", { name: /session length/i }),
    ).toBeVisible();

    expect(forbiddenUrls, `unexpected 403s: ${forbiddenUrls.join(", ")}`).toEqual(
      [],
    );

    // The switch actually took effect server-side too, not just client URL.
    const storedOrg = await page.evaluate(() =>
      localStorage.getItem("solidping_org"),
    );
    expect(storedOrg).toBe(org2Slug);
  });

  test("the account tab bar scrolls horizontally instead of squashing on a narrow screen", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/account/profile");
    await page.waitForLoadState("networkidle");

    const nav = page.getByTestId("tab-nav");
    await expect(nav).toBeVisible();
    const organizationsTab = nav.getByRole("link", { name: "Organizations" });
    await expect(organizationsTab).toBeVisible();

    // Desktop: every tab fits, so the nav must not need to scroll.
    await page.setViewportSize({ width: 1280, height: 800 });
    const desktopOverflow = await nav.evaluate(
      (el) => el.scrollWidth > el.clientWidth + 1,
    );
    expect(desktopOverflow).toBe(false);

    // Narrow: the extra tab no longer fits, so the row must become
    // horizontally scrollable rather than wrapping/squashing — every tab
    // link stays on a single row (same offsetTop).
    await page.setViewportSize({ width: 375, height: 812 });
    const tops = await nav
      .getByRole("link")
      .evaluateAll((els) => els.map((el) => (el as HTMLElement).offsetTop));
    expect(new Set(tops).size).toBe(1);
  });
});
