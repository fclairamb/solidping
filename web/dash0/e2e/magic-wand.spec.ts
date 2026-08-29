import { test, expect, API_BASE, type Page } from "./fixtures";

// Spec 2026-08-29-03: the Getting Started steps each land the user on an
// empty form. This exercises the one-click "magic wand" default on each of
// the three pages that offer one — DIRECT CREATE for alerts and the weekly
// report, PREFILL ONLY for the status page.
//
// Every test works from a throwaway org created through the real POST
// /api/v1/orgs by a freshly seeded zero-org user (mirrors
// onboarding-checklist.spec.ts), so nothing here can disturb the shared
// `test` fixture org used by other spec files. Such an org is NOT blank:
// `seedOrgDefaults` seeds a default email integration and a weekly report
// schedule for every self-created org, so the "wand is visible" precondition
// is established deliberately by deleting the seeded resource first, rather
// than assumed.
test.describe("Magic wand defaults", () => {
  async function seedBrowserSession(
    page: Page,
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

  /**
   * Creates a zero-org user, an org they own (named `orgName`), and
   * `checkCount` disabled checks (never actually pinged — the wand tests
   * only care that they exist and have names).
   */
  async function seedOrg(page: Page, checkCount: number) {
    const stamp = Date.now() + Math.floor(Math.random() * 1000);
    const email = `wand-${stamp}@unknown.example`;
    const password = "Strong-Pass-123!";
    const orgName = `Acme Wand ${stamp}`;

    const createUserResp = await page.request.post(
      `${API_BASE}/api/v1/test/users`,
      { data: { email, password, name: "Wand Owner" } },
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

    const slug = `wand-${stamp.toString(36)}`;
    const createOrgResp = await page.request.post(`${API_BASE}/api/v1/orgs`, {
      headers: { Authorization: `Bearer ${session.accessToken}` },
      data: { name: orgName, slug },
    });
    expect(createOrgResp.status()).toBe(201);

    const org = (await createOrgResp.json()) as {
      slug: string;
      accessToken: string;
      refreshToken?: string;
      expiresIn?: number;
    };
    const auth = { Authorization: `Bearer ${org.accessToken}` };

    const checkNames: string[] = [];
    for (let i = 0; i < checkCount; i++) {
      const name = `Acme site ${i + 1}`;
      checkNames.push(name);
      const checkResp = await page.request.post(
        `${API_BASE}/api/v1/orgs/${org.slug}/checks`,
        {
          headers: auth,
          data: {
            name,
            slug: `acme-site-${i + 1}`,
            type: "http",
            enabled: false,
            config: { url: "https://acme.com" },
          },
        },
      );
      expect(checkResp.status()).toBe(201);
    }

    await seedBrowserSession(page, { ...org, slug: org.slug });

    return { orgSlug: org.slug, auth, email, orgName, checkNames };
  }

  async function deleteAll(
    page: Page,
    orgSlug: string,
    auth: Record<string, string>,
    resource: "integrations" | "report-schedules",
  ) {
    const resp = await page.request.get(
      `${API_BASE}/api/v1/orgs/${orgSlug}/${resource}`,
      { headers: auth },
    );
    expect(resp.status()).toBe(200);
    const items = ((await resp.json()) as { data?: { uid: string }[] }).data ?? [];
    for (const item of items) {
      const del = await page.request.delete(
        `${API_BASE}/api/v1/orgs/${orgSlug}/${resource}/${item.uid}`,
        { headers: auth },
      );
      expect(del.status()).toBeLessThan(300);
    }
  }

  test("alerts wand: creates the org's email channel and flips the checklist step", async ({
    page,
  }) => {
    const { orgSlug, auth, email } = await seedOrg(page, 1);

    // Establish the precondition: a self-created org starts with a seeded
    // default email integration, so the wand would otherwise be absent from
    // the start rather than genuinely appearing because the step is open.
    await deleteAll(page, orgSlug, auth, "integrations");

    await page.goto(`orgs/${orgSlug}/integrations`);
    await page.waitForLoadState("networkidle");

    const wand = page.getByTestId("wand-create-email-alerts");
    await expect(wand).toBeVisible();

    await wand.click();
    await expect(page.getByText("Email alerts created")).toBeVisible();

    // The wand disappears once the step it offers is satisfied.
    await expect(wand).toHaveCount(0);

    // The created integration is addressed to the signed-in user, exactly
    // like seedOrgDefaults would have written.
    const resp = await page.request.get(
      `${API_BASE}/api/v1/orgs/${orgSlug}/integrations`,
      { headers: auth },
    );
    const items = ((await resp.json()) as {
      data?: {
        type: string;
        name: string;
        isDefault: boolean;
        settings?: { to?: string[] };
      }[];
    }).data ?? [];
    expect(items).toHaveLength(1);
    expect(items[0].type).toBe("email");
    expect(items[0].name).toBe("Email alerts");
    expect(items[0].isDefault).toBe(true);
    expect(items[0].settings?.to).toEqual([email]);

    // The Getting Started `alerts` step reads this same query key, so it
    // flips with no extra plumbing.
    await page.goto(`orgs/${orgSlug}`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("onboarding-step-alerts")).toHaveAttribute(
      "data-done",
      "true",
    );
  });

  test("alerts wand: absent once a notifiable integration already exists", async ({
    page,
  }) => {
    const { orgSlug } = await seedOrg(page, 1);
    // Deliberately NOT deleting the seeded integration this time — the org
    // starts 3/5 already, and this is the control for the test above.

    await page.goto(`orgs/${orgSlug}/integrations`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("wand-create-email-alerts")).toHaveCount(0);
  });

  test("report wand: creates a weekly org-wide report and flips the checklist step", async ({
    page,
  }) => {
    const { orgSlug, auth, email } = await seedOrg(page, 1);
    await deleteAll(page, orgSlug, auth, "report-schedules");

    await page.goto(`orgs/${orgSlug}/organization/report-schedules`);
    await page.waitForLoadState("networkidle");

    const wand = page.getByTestId("wand-create-weekly-report");
    await expect(wand).toBeVisible();

    await wand.click();
    await expect(page.getByText("Weekly uptime report created")).toBeVisible();
    await expect(wand).toHaveCount(0);

    const resp = await page.request.get(
      `${API_BASE}/api/v1/orgs/${orgSlug}/report-schedules`,
      { headers: auth },
    );
    const items = ((await resp.json()) as {
      data?: {
        name: string;
        frequency: string;
        recipients: string[];
        checkUids: string[];
        checkGroupUids: string[];
        includeSlos: boolean;
        enabled: boolean;
      }[];
    }).data ?? [];
    expect(items).toHaveLength(1);
    expect(items[0].name).toBe("Weekly uptime report");
    expect(items[0].frequency).toBe("weekly");
    expect(items[0].recipients).toEqual([email]);
    // Empty scopes = org-wide.
    expect(items[0].checkUids).toEqual([]);
    expect(items[0].checkGroupUids).toEqual([]);
    expect(items[0].includeSlos).toBe(true);
    expect(items[0].enabled).toBe(true);

    await page.goto(`orgs/${orgSlug}`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("onboarding-step-report")).toHaveAttribute(
      "data-done",
      "true",
    );
  });

  test("report wand: absent once an enabled schedule already exists", async ({
    page,
  }) => {
    const { orgSlug } = await seedOrg(page, 1);

    await page.goto(`orgs/${orgSlug}/organization/report-schedules`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("wand-create-weekly-report")).toHaveCount(0);
  });

  test("status-page wand: prefills the org name and every check, then creates one resource per check", async ({
    page,
  }) => {
    const { orgSlug, auth, orgName, checkNames } = await seedOrg(page, 2);

    await page.goto(`orgs/${orgSlug}/status-pages/new`);
    await page.waitForLoadState("networkidle");

    const wand = page.getByTestId("wand-prefill-status-page");
    await expect(wand).toBeVisible();
    await expect(wand).toBeEnabled({ timeout: 10000 });

    await wand.click();

    await expect(page.locator("#name")).toHaveValue(orgName);
    const badges = page.getByTestId("status-page-attached-check");
    await expect(badges).toHaveCount(checkNames.length);
    for (const name of checkNames) {
      await expect(badges.filter({ hasText: name })).toHaveCount(1);
    }

    // The wand only offers to fill a BLANK form — once it has run once, it
    // steps aside (the form now has a name and attached checks).
    await expect(wand).toHaveCount(0);

    const suffix = Date.now().toString().slice(-9);
    await page.locator("#slug").fill(`e2e-wand-${suffix}`.slice(0, 40));
    await page.getByRole("button", { name: "Create Status Page" }).click();

    await page.waitForURL(/\/status-pages\/(?!new)[^/]+$/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    const pageUid = page.url().split("/status-pages/")[1];

    const detailResp = await page.request.get(
      `${API_BASE}/api/v1/orgs/${orgSlug}/status-pages/${pageUid}`,
      { headers: auth },
    );
    expect(detailResp.status()).toBe(200);
    const detail = (await detailResp.json()) as {
      name: string;
      sections?: { resources?: { checkUid?: string }[] }[];
    };
    expect(detail.name).toBe(orgName);
    const resources = (detail.sections ?? []).flatMap(
      (section) => section.resources ?? [],
    );
    expect(resources).toHaveLength(checkNames.length);

    await page.request.delete(
      `${API_BASE}/api/v1/orgs/${orgSlug}/status-pages/${pageUid}`,
      { headers: auth },
    );
  });

  test("status-page wand: removing a badge drops it from what gets submitted", async ({
    page,
  }) => {
    const { orgSlug, auth, checkNames } = await seedOrg(page, 2);

    await page.goto(`orgs/${orgSlug}/status-pages/new`);
    await page.waitForLoadState("networkidle");

    const wand = page.getByTestId("wand-prefill-status-page");
    await expect(wand).toBeEnabled({ timeout: 10000 });
    await wand.click();

    const badges = page.getByTestId("status-page-attached-check");
    await expect(badges).toHaveCount(2);

    // Remove the first badge (the "x" button inside it).
    await badges.first().getByRole("button").click();
    await expect(badges).toHaveCount(1);
    await expect(badges).toHaveText(new RegExp(checkNames[1]));

    const suffix = Date.now().toString().slice(-9);
    await page.locator("#slug").fill(`e2e-wand-rm-${suffix}`.slice(0, 40));
    await page.getByRole("button", { name: "Create Status Page" }).click();

    await page.waitForURL(/\/status-pages\/(?!new)[^/]+$/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    const pageUid = page.url().split("/status-pages/")[1];

    const detailResp = await page.request.get(
      `${API_BASE}/api/v1/orgs/${orgSlug}/status-pages/${pageUid}`,
      { headers: auth },
    );
    const detail = (await detailResp.json()) as {
      sections?: { resources?: { checkUid?: string }[] }[];
    };
    const resources = (detail.sections ?? []).flatMap(
      (section) => section.resources ?? [],
    );
    expect(resources).toHaveLength(1);

    await page.request.delete(
      `${API_BASE}/api/v1/orgs/${orgSlug}/status-pages/${pageUid}`,
      { headers: auth },
    );
  });

  test("integrations page stays usable at mobile width with both wand and New buttons", async ({
    page,
  }) => {
    const { orgSlug, auth } = await seedOrg(page, 1);
    await deleteAll(page, orgSlug, auth, "integrations");

    await page.setViewportSize({ width: 375, height: 812 });
    await page.goto(`orgs/${orgSlug}/integrations`);
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("wand-create-email-alerts")).toBeVisible();

    const hasHorizontalOverflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth,
    );
    expect(hasHorizontalOverflow).toBe(false);
  });
});
