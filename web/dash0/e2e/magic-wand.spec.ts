import { test, expect, API_BASE, type Page } from "./fixtures";

// Spec 2026-08-29-03: the Getting Started steps each land the user on an
// empty form. This exercises the one-click "magic wand" default on each of
// the pages that offer one — DIRECT CREATE for alerts, the weekly report,
// and (spec 2026-08-30-10) the status-pages LIST; PREFILL ONLY for the
// status-page CREATE FORM itself.
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
    // like seedOrgDefaults would have written. The LIST endpoint deliberately
    // omits `settings` (server internal/handlers/integrations/service.go's
    // toResponse(conn, includeSettings) — only the single-item GET includes
    // it), so the recipient check needs the individual GET.
    const resp = await page.request.get(
      `${API_BASE}/api/v1/orgs/${orgSlug}/integrations`,
      { headers: auth },
    );
    const items = ((await resp.json()) as {
      data?: {
        uid: string;
        type: string;
        name: string;
        isDefault: boolean;
      }[];
    }).data ?? [];
    expect(items).toHaveLength(1);
    expect(items[0].type).toBe("email");
    expect(items[0].name).toBe("Email alerts");
    expect(items[0].isDefault).toBe(true);

    const detailResp = await page.request.get(
      `${API_BASE}/api/v1/orgs/${orgSlug}/integrations/${items[0].uid}`,
      { headers: auth },
    );
    const detail = (await detailResp.json()) as {
      settings?: { to?: string[] };
    };
    expect(detail.settings?.to).toEqual([email]);

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

    // Sections/resources are only embedded with ?with=sections (server
    // internal/handlers/statuspages/handler.go's GetStatusPageOptions) —
    // the from-check e2e spec sidesteps this by reading the PUBLIC endpoint
    // instead, which always includes them.
    const detailResp = await page.request.get(
      `${API_BASE}/api/v1/orgs/${orgSlug}/status-pages/${pageUid}?with=sections`,
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

    // Remove the first badge (the "x" button inside it). The check list
    // does not come back in creation order (default sort is created_at
    // DESC), so capture which one this actually was rather than assuming
    // index 0 is checkNames[0].
    const removedText = (await badges.first().textContent())?.trim() ?? "";
    expect(checkNames).toContain(removedText);
    await badges.first().getByRole("button").click();
    await expect(badges).toHaveCount(1);
    const remainingText = (await badges.first().textContent())?.trim() ?? "";
    expect(remainingText).not.toBe(removedText);
    expect(checkNames).toContain(remainingText);

    const suffix = Date.now().toString().slice(-9);
    await page.locator("#slug").fill(`e2e-wand-rm-${suffix}`.slice(0, 40));
    await page.getByRole("button", { name: "Create Status Page" }).click();

    await page.waitForURL(/\/status-pages\/(?!new)[^/]+$/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    const pageUid = page.url().split("/status-pages/")[1];

    const detailResp = await page.request.get(
      `${API_BASE}/api/v1/orgs/${orgSlug}/status-pages/${pageUid}?with=sections`,
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

  test("status pages LIST wand: creates a page outright with every check and the create-form defaults", async ({
    page,
  }) => {
    const { orgSlug, auth, orgName, checkNames } = await seedOrg(page, 2);

    await page.goto(`orgs/${orgSlug}/status-pages`);
    await page.waitForLoadState("networkidle");

    const wand = page.getByTestId("wand-create-status-page");
    await expect(wand).toBeVisible();
    await expect(wand).toBeEnabled({ timeout: 10000 });

    await wand.click();
    await expect(page.getByText("Status page created successfully")).toBeVisible();

    // Unlike the form's prefill-only wand, this one creates outright and
    // navigates straight to the detail route — never the create form.
    await page.waitForURL(/\/status-pages\/(?!new)[^/]+$/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    const pageUid = page.url().split("/status-pages/")[1];

    const detailResp = await page.request.get(
      `${API_BASE}/api/v1/orgs/${orgSlug}/status-pages/${pageUid}?with=sections`,
      { headers: auth },
    );
    expect(detailResp.status()).toBe(200);
    const detail = (await detailResp.json()) as {
      name: string;
      visibility: string;
      autoPublish: boolean;
      autoPublishDelaySeconds: number;
      autoResolve: string;
      historyPeriod: string;
      showAvailability: boolean;
      showResponseTime: boolean;
      hideBranding: boolean;
      isDefault: boolean;
      sections?: { resources?: { checkUid?: string }[] }[];
    };
    // Name and every check mirror what the form's "Prefill for me" wand +
    // submit-untouched would have produced.
    expect(detail.name).toBe(orgName);
    const resources = (detail.sections ?? []).flatMap(
      (section) => section.resources ?? [],
    );
    expect(resources).toHaveLength(checkNames.length);
    // Every other field is the create form's own default — see
    // status-page-form.tsx's initial state / buildStatusPageWandAutoCreatePayload.
    expect(detail.visibility).toBe("public");
    // isDefault is the ONE field the client does not get to decide: the server
    // promotes an org's FIRST status page to default whatever the payload says
    // (statuspages/service.go, "Check if this should be default (first page or
    // explicitly set)"). seedOrg gives us a fresh org, so the wand's page is
    // that first page. The create form goes through the same path and lands on
    // the same value — which is exactly the parity this test is pinning, so
    // asserting `false` here would assert a behaviour neither surface has.
    expect(detail.isDefault).toBe(true);
    expect(detail.showAvailability).toBe(true);
    expect(detail.showResponseTime).toBe(true);
    expect(detail.hideBranding).toBe(false);
    expect(detail.historyPeriod).toBe("90d");
    expect(detail.autoPublish).toBe(true);
    expect(detail.autoPublishDelaySeconds).toBe(60);
    expect(detail.autoResolve).toBe("if_untouched");

    await page.request.delete(
      `${API_BASE}/api/v1/orgs/${orgSlug}/status-pages/${pageUid}`,
      { headers: auth },
    );
  });

  test("status pages LIST wand: attaches every check even when the org has more than 100", async ({
    page,
  }) => {
    // Guards the auto-page-through fetch in status-pages.index.tsx: an org
    // with more checks than the list endpoint's single-page cap must not be
    // silently under-attached. 105 checks created via the real API (not
    // mocked) so this exercises the actual useInfiniteChecks pagination the
    // wand relies on, not just the pure payload builder (see
    // onboarding-wand.test.ts for that unit-level guard).
    const CHECK_COUNT = 105;
    const { orgSlug, auth } = await seedOrg(page, 0);

    await Promise.all(
      Array.from({ length: CHECK_COUNT }, (_, i) =>
        page.request.post(`${API_BASE}/api/v1/orgs/${orgSlug}/checks`, {
          headers: auth,
          data: {
            name: `Acme bulk site ${i + 1}`,
            slug: `acme-bulk-site-${i + 1}`,
            type: "http",
            enabled: false,
            config: { url: "https://acme.com" },
          },
        }),
      ),
    ).then((responses) => {
      for (const resp of responses) expect(resp.status()).toBe(201);
    });

    await page.goto(`orgs/${orgSlug}/status-pages`);
    await page.waitForLoadState("networkidle");

    const wand = page.getByTestId("wand-create-status-page");
    await expect(wand).toBeVisible();
    // Disabled while useInfiniteChecks is still paging through — 105 checks
    // means at least 2 pages at the wand's limit:100 request size.
    await expect(wand).toBeEnabled({ timeout: 20000 });

    await wand.click();
    await page.waitForURL(/\/status-pages\/(?!new)[^/]+$/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    const pageUid = page.url().split("/status-pages/")[1];

    const detailResp = await page.request.get(
      `${API_BASE}/api/v1/orgs/${orgSlug}/status-pages/${pageUid}?with=sections`,
      { headers: auth },
    );
    expect(detailResp.status()).toBe(200);
    const detail = (await detailResp.json()) as {
      sections?: { resources?: { checkUid?: string }[] }[];
    };
    const resources = (detail.sections ?? []).flatMap(
      (section) => section.resources ?? [],
    );
    // The whole point: every one of the 105 checks got attached, not just
    // the first page's worth.
    expect(resources).toHaveLength(CHECK_COUNT);

    await page.request.delete(
      `${API_BASE}/api/v1/orgs/${orgSlug}/status-pages/${pageUid}`,
      { headers: auth },
    );
  });

  test("status pages LIST wand: a colliding slug surfaces an error instead of inventing a suffix", async ({
    page,
  }) => {
    // Mirrors web/dash0/src/lib/utils.ts's slugify — the wand derives the new
    // page's slug from the org name the same way the create form does.
    const slugify = (name: string) =>
      name
        .toLowerCase()
        .replace(/[^a-z0-9-]/g, "-")
        .replace(/-+/g, "-")
        .replace(/^-|-$/g, "")
        .slice(0, 100);

    const { orgSlug, auth, orgName } = await seedOrg(page, 1);
    const collidingSlug = slugify(orgName);

    // Pre-create a status page occupying the exact slug the wand would try
    // to use, so its POST hits a real CONFLICT from the API.
    const preCreateResp = await page.request.post(
      `${API_BASE}/api/v1/orgs/${orgSlug}/status-pages`,
      {
        headers: auth,
        data: { name: "Existing page", slug: collidingSlug },
      },
    );
    expect(preCreateResp.status()).toBe(201);

    await page.goto(`orgs/${orgSlug}/status-pages`);
    await page.waitForLoadState("networkidle");

    const wand = page.getByTestId("wand-create-status-page");
    await expect(wand).toBeEnabled({ timeout: 10000 });
    await wand.click();

    // Error toast, and the wand does NOT retry with an invented suffix — it
    // stays on the list with no second page created.
    await expect(page.getByText(/already|conflict|slug/i)).toBeVisible({
      timeout: 10000,
    });
    await expect(page).toHaveURL(/\/status-pages$/);

    const listResp = await page.request.get(
      `${API_BASE}/api/v1/orgs/${orgSlug}/status-pages`,
      { headers: auth },
    );
    const items = ((await listResp.json()) as { data?: { slug: string }[] })
      .data ?? [];
    expect(items.filter((p) => p.slug === collidingSlug)).toHaveLength(1);
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
