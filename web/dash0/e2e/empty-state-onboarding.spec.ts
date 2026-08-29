import { test, expect, API_BASE, type Page } from "./fixtures";

// Covers the empty-state onboarding hero (EmptyStateOnboarding, rendered on
// /orgs/$org when the org has zero checks) and specifically the 2026-07-11
// "MCP / AI path" addition: alongside the HTTP/Ping/SSL quick-create chips
// and the full-editor link, the hero now offers a secondary "let AI set
// everything up" sub-card linking to the per-client MCP setup page under
// Account.
//
// The shared `test` org accumulates checks from other suites (and parallel
// workers), so instead of mutating shared state to empty it, both check
// endpoints the dashboard reads are stubbed empty: the list (`{"data":[]}`)
// and the aggregate counters (`total: 0`). `isEmptyOrg` in dashboard-page.tsx
// keys off the STATS endpoint — counting the list page would be wrong past
// 100 checks (GitHub issue #172) — so stubbing the list alone would no longer
// render the hero. The click-through to the MCP page then runs against the
// real backend (the MCP page needs no checks and no PAT — exactly the
// brand-new-org situation the spec calls out).
test.describe("Empty-state onboarding (zero-checks dashboard hero)", () => {
  async function gotoEmptyDashboard(page: import("./fixtures").Page) {
    // Needs its own pattern: a single `*` in a Playwright URL glob never
    // matches `/`, so the `checks*` route below cannot serve `/checks/stats`.
    await page.route("**/api/v1/orgs/test/checks/stats*", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          total: 0,
          enabled: 0,
          disabled: 0,
          byStatus: {},
          down: 0,
          hardDown: 0,
        }),
      }),
    );
    await page.route("**/api/v1/orgs/test/checks*", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ data: [] }),
      }),
    );
    await page.goto("orgs/test");
    await page.waitForLoadState("networkidle");
  }

  test("offers quick-create, the MCP / AI path, and the full editor", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await gotoEmptyDashboard(page);

    // Primary path: the three quick-start chips and the one-field form.
    await expect(page.getByTestId("quick-start-http")).toBeVisible();
    await expect(page.getByTestId("quick-start-icmp")).toBeVisible();
    await expect(page.getByTestId("quick-start-ssl")).toBeVisible();
    await expect(page.getByTestId("quick-start-input")).toBeVisible();
    await expect(page.getByTestId("quick-start-submit")).toBeVisible();

    // Secondary path: the MCP / AI sub-card links to the Account MCP page.
    const mcpLink = page.getByTestId("quick-start-mcp-link");
    await expect(mcpLink).toBeVisible();
    const href = await mcpLink.getAttribute("href");
    expect(href).toBeTruthy();
    expect(new URL(href!, page.url()).pathname).toBe(
      "/dash0/orgs/test/account/mcp",
    );

    // Tertiary path: the full check editor hint is still there.
    await expect(
      page.locator('a[href="/dash0/orgs/test/checks/new"]'),
    ).toBeVisible();
  });

  test("MCP CTA click-through lands on a working MCP setup page", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await gotoEmptyDashboard(page);

    await page.getByTestId("quick-start-mcp-link").click();
    await page.waitForURL(/\/orgs\/test\/account\/mcp/);

    // The MCP page renders fully for an org with zero checks and no PAT:
    // heading plus the instance MCP URL derived from the origin.
    await expect(
      page.getByRole("heading", { name: /ai assistants/i }),
    ).toBeVisible();
    const origin = await page.evaluate(() => window.location.origin);
    await expect(page.getByText(`${origin}/api/v1/mcp`).first()).toBeVisible();
  });

  test("hero stays usable at mobile width, quick-create first", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.setViewportSize({ width: 375, height: 812 });
    await gotoEmptyDashboard(page);

    await expect(page.getByTestId("quick-start-input")).toBeVisible();
    await expect(page.getByTestId("quick-start-mcp-link")).toBeVisible();

    // No fixed-width element may force the page wider than the viewport.
    const hasHorizontalOverflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth,
    );
    expect(hasHorizontalOverflow).toBe(false);

    // Hierarchy: the quick-create form renders above the MCP sub-card.
    const formBox = await page.getByTestId("quick-start-input").boundingBox();
    const mcpBox = await page.getByTestId("quick-start-mcp-link").boundingBox();
    expect(formBox).toBeTruthy();
    expect(mcpBox).toBeTruthy();
    expect(formBox!.y).toBeLessThan(mcpBox!.y);
  });

  // Spec 2026-08-29-07: submitting the quick-create form used to just clear
  // the input and let the dashboard re-render into the regular view once the
  // checks list refetched. It now navigates straight to the new check's own
  // page instead — the moment of highest engagement right after creating a
  // first check. This needs a genuinely empty org (not the shared `test` org
  // stubbed empty above): the earlier tests' route stubs intercept every
  // request to `**/api/v1/orgs/test/checks*`, method included, so a real
  // quick-create POST through them would be swallowed by the `{data: []}`
  // GET stub instead of returning the created check. A fresh org sidesteps
  // that and also gives a clean, unshared checks list to assert against.
  test.describe("quick-create redirect (needs a real empty org)", () => {
    /** Creates a fresh org with zero checks, authenticated as its owner. */
    async function seedEmptyOrg(page: Page): Promise<string> {
      const stamp = Date.now() + Math.floor(Math.random() * 1000);
      const email = `quickcreate-${stamp}@unknown.example`;
      const password = "Strong-Pass-123!";

      const createUserResp = await page.request.post(
        `${API_BASE}/api/v1/test/users`,
        { data: { email, password, name: "Quick Create Owner" } },
      );
      if (createUserResp.status() !== 201) {
        test.skip(
          true,
          `test user-seed endpoint unavailable (server not in SP_RUNMODE=test?): ${createUserResp.status()}`,
        );
      }

      const loginResp = await page.request.post(
        `${API_BASE}/api/v1/auth/login`,
        { data: { email, password } },
      );
      expect(loginResp.status()).toBe(200);
      const session = (await loginResp.json()) as { accessToken: string };

      const slug = `qc-${stamp.toString(36)}`;
      const createOrgResp = await page.request.post(`${API_BASE}/api/v1/orgs`, {
        headers: { Authorization: `Bearer ${session.accessToken}` },
        data: { name: `Acme Quick Create ${stamp}`, slug },
      });
      expect(createOrgResp.status()).toBe(201);
      const org = (await createOrgResp.json()) as {
        slug: string;
        accessToken: string;
        refreshToken?: string;
        expiresIn?: number;
      };

      await page.addInitScript(
        ({ accessToken, refreshToken, expiresIn, orgSlug }) => {
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
          localStorage.setItem("solidping_org", orgSlug as string);
        },
        {
          accessToken: org.accessToken,
          refreshToken: org.refreshToken ?? "",
          expiresIn: org.expiresIn ?? 0,
          orgSlug: org.slug,
        },
      );

      return org.slug;
    }

    test("navigates to the new check's page, and the hero is gone on return", async ({
      page,
    }) => {
      const orgSlug = await seedEmptyOrg(page);

      await page.goto(`orgs/${orgSlug}`);
      await page.waitForLoadState("networkidle");

      // Confirms the "dashboard switches out of the empty state" coverage
      // this test replaces: the hero is present before create.
      await expect(page.getByTestId("quick-start-input")).toBeVisible();

      await page.getByTestId("quick-start-input").fill("https://acme.com");
      await page.getByTestId("quick-start-submit").click();

      // Lands on the check detail route, not back on the dashboard.
      await page.waitForURL(
        new RegExp(`/orgs/${orgSlug}/checks/[^/]+/?$`),
      );
      await expect(
        page.locator('[data-testid="check-detail-header"] h1'),
      ).toContainText("HTTP — acme.com");

      // Navigating back to the dashboard: the org now has a check, so the
      // empty-state hero must be gone (replaced by the regular dashboard /
      // onboarding-checklist view) — the behavior the old assertion covered.
      await page.goto(`orgs/${orgSlug}`);
      await page.waitForLoadState("networkidle");
      await expect(page.getByTestId("quick-start-input")).not.toBeVisible();
      await expect(page.getByTestId("quick-start-submit")).not.toBeVisible();
    });
  });
});
