import { test, expect, API_BASE, type Page } from "./fixtures";

// Spec 2026-08-28-17: after the first check exists, the dashboard shows a
// getting-started checklist. Two properties are what make it worth shipping,
// and both are asserted here rather than inferred:
//
//   - completion is DERIVED from real resources — creating an integration
//     flips the row with nothing stored anywhere, and
//   - the dismissal is SERVER-SIDE per user per org — it survives a full
//     reload (which a localStorage flag would too) *and* is undone from the
//     account page, which is what proves it is not localStorage.
//
// Every test works from a throwaway org created through the real POST
// /api/v1/orgs by a freshly seeded zero-org user, so nothing here can disturb
// the shared `test` fixture org. Note that such an org is NOT blank: spec
// 2026-08-28-15 seeds a default email integration and a weekly report
// schedule for every self-created org, so it starts at 3/5 once a check
// exists. The "item flips" test deletes the seeded integration first, so it
// observes a real false → true transition instead of passing by accident.
test.describe("Getting-started checklist", () => {
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

  /** Creates a zero-org user, an org they own, and one (disabled) check. */
  async function seedOrgWithCheck(page: Page) {
    const stamp = Date.now() + Math.floor(Math.random() * 1000);
    const email = `onboard-${stamp}@unknown.example`;
    const password = "Strong-Pass-123!";

    const createUserResp = await page.request.post(
      `${API_BASE}/api/v1/test/users`,
      { data: { email, password, name: "Onboarding Owner" } },
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

    const slug = `onb-${stamp.toString(36)}`;
    const createOrgResp = await page.request.post(`${API_BASE}/api/v1/orgs`, {
      headers: { Authorization: `Bearer ${session.accessToken}` },
      data: { name: `Acme Onboarding ${stamp}`, slug },
    });
    expect(createOrgResp.status()).toBe(201);

    const org = (await createOrgResp.json()) as {
      slug: string;
      accessToken: string;
      refreshToken?: string;
      expiresIn?: number;
    };
    const auth = { Authorization: `Bearer ${org.accessToken}` };

    // Disabled so the check never actually leaves the box — the checklist
    // only cares that the org HAS one, and an enabled HTTP check would make
    // this suite depend on outbound network.
    const checkResp = await page.request.post(
      `${API_BASE}/api/v1/orgs/${org.slug}/checks`,
      {
        headers: auth,
        data: {
          name: "Acme site",
          slug: "acme-site",
          type: "http",
          enabled: false,
          config: { url: "https://acme.com" },
        },
      },
    );
    expect(checkResp.status()).toBe(201);

    await seedBrowserSession(page, { ...org, slug: org.slug });

    return { orgSlug: org.slug, auth };
  }

  async function listIntegrations(
    page: Page,
    orgSlug: string,
    auth: Record<string, string>,
  ) {
    const resp = await page.request.get(
      `${API_BASE}/api/v1/orgs/${orgSlug}/integrations`,
      { headers: auth },
    );
    expect(resp.status()).toBe(200);
    return ((await resp.json()) as { data?: { uid: string }[] }).data ?? [];
  }

  async function openDashboard(page: Page, orgSlug: string) {
    await page.goto(`orgs/${orgSlug}`);
    await page.waitForLoadState("networkidle");
  }

  test("appears once a check exists, with the check step already ticked", async ({
    page,
  }) => {
    const { orgSlug } = await seedOrgWithCheck(page);

    await openDashboard(page, orgSlug);

    const card = page.getByTestId("onboarding-checklist");
    await expect(card).toBeVisible();

    // Step 1 opens completed on purpose — that is the "this thing works"
    // signal, not a bug.
    await expect(page.getByTestId("onboarding-step-check")).toHaveAttribute(
      "data-done",
      "true",
    );

    // Nothing has been published or shared yet, so these are genuinely open.
    // Their `false` is the control for the `true` above.
    await expect(page.getByTestId("onboarding-step-statusPage")).toHaveAttribute(
      "data-done",
      "false",
    );
    await expect(page.getByTestId("onboarding-step-team")).toHaveAttribute(
      "data-done",
      "false",
    );

    // Every step links somewhere; the status-page one carries the check to
    // pre-attach (spec 2026-08-28-16).
    await expect(
      page.getByTestId("onboarding-step-statusPage-cta"),
    ).toHaveAttribute("href", /\/status-pages\/new\?checkUid=/);
  });

  test("the alerting step is derived: it flips when a channel appears, with nothing stored", async ({
    page,
  }) => {
    const { orgSlug, auth } = await seedOrgWithCheck(page);

    // A self-created org is seeded with a default email channel, so start by
    // removing it — otherwise "creating an integration flips the step" would
    // pass without the step ever having been false.
    for (const integration of await listIntegrations(page, orgSlug, auth)) {
      const del = await page.request.delete(
        `${API_BASE}/api/v1/orgs/${orgSlug}/integrations/${integration.uid}`,
        { headers: auth },
      );
      expect(del.status()).toBeLessThan(300);
    }
    expect(await listIntegrations(page, orgSlug, auth)).toHaveLength(0);

    await openDashboard(page, orgSlug);
    await expect(page.getByTestId("onboarding-step-alerts")).toHaveAttribute(
      "data-done",
      "false",
    );
    // With no email channel there is nothing to send a test alert through.
    await expect(page.getByTestId("onboarding-test-alert")).toHaveCount(0);

    const created = await page.request.post(
      `${API_BASE}/api/v1/orgs/${orgSlug}/integrations`,
      {
        headers: auth,
        data: {
          type: "email",
          name: "Ops mailbox",
          enabled: true,
          isDefault: true,
          settings: { to: ["alice@acme.com"] },
        },
      },
    );
    expect(created.status()).toBe(201);

    await openDashboard(page, orgSlug);
    await expect(page.getByTestId("onboarding-step-alerts")).toHaveAttribute(
      "data-done",
      "true",
    );
    await expect(page.getByTestId("onboarding-test-alert")).toBeVisible();
  });

  test("the test-alert button reports what the server actually said", async ({
    page,
  }) => {
    const { orgSlug } = await seedOrgWithCheck(page);

    // The endpoint answers 200 either way, so both outcomes are stubbed and
    // both are asserted: a failure must render the server's message, never a
    // silent success.
    let succeed = true;
    await page.route("**/api/v1/orgs/*/integrations/*/test", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(
          succeed
            ? {
                success: true,
                statusCode: 0,
                durationMs: 12,
                detail: "Delivered to alice@acme.com",
              }
            : {
                success: false,
                statusCode: 0,
                durationMs: 3,
                error: "SMTP is not configured on this server",
              },
        ),
      }),
    );

    await openDashboard(page, orgSlug);

    await page.getByTestId("onboarding-test-alert").click();
    await expect(
      page.getByText("Delivered to alice@acme.com"),
    ).toBeVisible();

    succeed = false;
    await page.getByTestId("onboarding-test-alert").click();
    await expect(
      page.getByText("SMTP is not configured on this server"),
    ).toBeVisible();
  });

  test("dismissing survives a full reload and is undone from the account page", async ({
    page,
  }) => {
    const { orgSlug } = await seedOrgWithCheck(page);

    await openDashboard(page, orgSlug);
    await expect(page.getByTestId("onboarding-checklist")).toBeVisible();

    await page.getByTestId("onboarding-dismiss").click();
    await expect(page.getByTestId("onboarding-checklist")).toHaveCount(0);

    // A full reload rebuilds the page from the server's answer. The old
    // localStorage banner would also have survived this, which is why the
    // account round trip below is the part that actually proves the storage
    // moved server-side.
    await openDashboard(page, orgSlug);
    await expect(page.getByTestId("onboarding-checklist")).toHaveCount(0);

    await page.goto(`orgs/${orgSlug}/account/profile`);
    await page.waitForLoadState("networkidle");
    await page.getByTestId("restore-onboarding-checklist").click();
    await expect(page.getByText(/getting-started checklist is back/i)).toBeVisible();

    await openDashboard(page, orgSlug);
    await expect(page.getByTestId("onboarding-checklist")).toBeVisible();
  });

  test("the dismissal follows the user to another browser session", async ({
    page,
    browser,
  }) => {
    const stamp = Date.now() + Math.floor(Math.random() * 1000);
    const email = `onboard-x-${stamp}@unknown.example`;
    const password = "Strong-Pass-123!";

    const createUserResp = await page.request.post(
      `${API_BASE}/api/v1/test/users`,
      { data: { email, password, name: "Onboarding Owner" } },
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

    const slug = `onbx-${stamp.toString(36)}`;
    const createOrgResp = await page.request.post(`${API_BASE}/api/v1/orgs`, {
      headers: { Authorization: `Bearer ${session.accessToken}` },
      data: { name: `Acme Onboarding X ${stamp}`, slug },
    });
    expect(createOrgResp.status()).toBe(201);
    const org = (await createOrgResp.json()) as {
      slug: string;
      accessToken: string;
      refreshToken?: string;
      expiresIn?: number;
    };

    const checkResp = await page.request.post(
      `${API_BASE}/api/v1/orgs/${org.slug}/checks`,
      {
        headers: { Authorization: `Bearer ${org.accessToken}` },
        data: {
          name: "Acme site",
          slug: "acme-site",
          type: "http",
          enabled: false,
          config: { url: "https://acme.com" },
        },
      },
    );
    expect(checkResp.status()).toBe(201);

    await seedBrowserSession(page, { ...org, slug: org.slug });
    await openDashboard(page, org.slug);
    await page.getByTestId("onboarding-dismiss").click();
    await expect(page.getByTestId("onboarding-checklist")).toHaveCount(0);

    // A brand-new browser context: no localStorage carried over, only the
    // same user's session. The card must still be hidden — that is the whole
    // point of moving the dismissal off the device.
    const other = await browser.newContext();
    const otherPage = await other.newPage();
    await seedBrowserSession(otherPage, { ...org, slug: org.slug });
    await otherPage.goto(`orgs/${org.slug}`);
    await otherPage.waitForLoadState("networkidle");

    // Positive control: the dashboard really did render for this context.
    await expect(otherPage.getByTestId("kpi-tile-monitored")).toBeVisible();
    await expect(otherPage.getByTestId("onboarding-checklist")).toHaveCount(0);

    await other.close();
  });

  test("stays usable at mobile width", async ({ page }) => {
    const { orgSlug } = await seedOrgWithCheck(page);

    await page.setViewportSize({ width: 375, height: 812 });
    await openDashboard(page, orgSlug);

    await expect(page.getByTestId("onboarding-checklist")).toBeVisible();
    await expect(page.getByTestId("onboarding-dismiss")).toBeVisible();
    await expect(page.getByTestId("onboarding-step-check-cta")).toBeVisible();

    // No fixed-width element may force the page wider than the viewport.
    const hasHorizontalOverflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth,
    );
    expect(hasHorizontalOverflow).toBe(false);
  });
});
