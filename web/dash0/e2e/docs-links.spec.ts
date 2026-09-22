import { test, expect, API_BASE, mockSloCoverage, type Page } from "./fixtures";

// The DocsLink primitive (web/dash0/src/components/shared/docs-link.tsx) is
// wired into PageHeader (and a few standalone card headers) only where a
// genuinely relevant docs page exists — never a forced link to /docs/intro.
// This spec asserts both sides of that contract: representative mapped pages
// render the link with the expected href, and an unmapped page renders none.

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

async function createCheck(
  page: Page,
  token: string,
  type: string,
  name: string,
  config: Record<string, unknown>,
): Promise<{ uid: string }> {
  const resp = await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { type, name, config, period: "00:05:00" },
  });
  expect(resp.status()).toBe(201);
  return await resp.json();
}

test.describe("Docs links", () => {
  test("checks list renders a docs link to check-types", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
    await page.waitForURL(/\/checks/);
    await page.waitForLoadState("networkidle");

    const docsLink = page.getByTestId("docs-link");
    await expect(docsLink).toBeVisible();
    await expect(docsLink).toHaveAttribute("href", "/docs/features/check-types");
    await expect(docsLink).toHaveAttribute("target", "_blank");
    await expect(docsLink).toHaveAttribute("rel", "noopener");
  });

  test("status pages list renders a docs link to status-pages", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    await page.getByTestId("app-sidebar").getByRole("link", { name: "Status Pages" }).click();
    await page.waitForURL(/\/status-pages/);
    await page.waitForLoadState("networkidle");

    const docsLink = page.getByTestId("docs-link");
    await expect(docsLink).toBeVisible();
    await expect(docsLink).toHaveAttribute("href", "/docs/features/status-pages");
  });

  test("SLOs list renders a docs link to slos", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    await page.getByTestId("app-sidebar").getByRole("link", { name: "SLOs" }).click();
    await page.waitForURL(/\/slos/);
    await page.waitForLoadState("networkidle");

    const docsLink = page.getByTestId("docs-link");
    await expect(docsLink).toBeVisible();
    await expect(docsLink).toHaveAttribute("href", "/docs/features/slos");
  });

  test("badges page renders a docs link to status-badges", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    // The badge builder lives under the check it belongs to (spec
    // 2026-09-16-08), so it is reached from a check, not from the sidebar.
    const check = await createCheck(
      page,
      token,
      "http",
      `E2E Docs Badges ${Date.now()}`,
      { url: "https://example.com/docs-badges" },
    );
    await page.goto(`orgs/test/checks/${check.uid}`);
    await page.waitForLoadState("networkidle");
    await page.getByLabel("Badges").click();
    await page.waitForURL(/\/checks\/[^/]+\/badges/);
    // No networkidle: the badges page never reaches Playwright's network-idle
    // state after a client-side navigation (page rendered, network quiet, the
    // lifecycle event just never fires — deterministic on macOS headless,
    // intermittent on loaded CI runners). The docs-link assertion below is
    // the arrival gate.
    const docsLink = page.getByTestId("docs-link");
    await expect(docsLink).toBeVisible();
    await expect(docsLink).toHaveAttribute("href", "/docs/features/status-badges");
  });

  test("discovery list renders a docs link to discovery", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    // Discovery lives under the Organization tab row now (spec 2026-09-16-09),
    // admin-only end to end; the test user is an admin of the "test" org
    // (server/test/testdata/testdata.go), so it is reachable the way a user
    // reaches it: the Administration group's Organization entry in the sidebar
    // (spec 2026-09-16-07), then the Discovery tab.
    await page
      .getByTestId("app-sidebar")
      // `exact` matters: the sidebar header's accessible name carries the org
      // name, "Test Organization".
      .getByRole("link", { name: "Organization", exact: true })
      .click();
    const tabNav = page.getByTestId("tab-nav");
    await expect(tabNav).toBeVisible();
    await tabNav.getByRole("link", { name: /discovery/i }).click();
    await page.waitForURL(/\/organization\/discovery/);
    await page.waitForLoadState("networkidle");

    const docsLink = page.getByTestId("docs-link");
    await expect(docsLink).toBeVisible();
    await expect(docsLink).toHaveAttribute("href", "/docs/features/discovery");
  });

  test("events list renders a docs link to events", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    await page.getByTestId("app-sidebar").getByRole("link", { name: "Events" }).click();
    await page.waitForURL(/\/events/);
    await page.waitForLoadState("networkidle");

    const docsLink = page.getByTestId("docs-link");
    await expect(docsLink).toBeVisible();
    await expect(docsLink).toHaveAttribute("href", "/docs/features/events");
  });


  // The check detail page's docs link is per-type, not the generic
  // check-types page: whichever protocol a check monitors, its reference
  // section is one click away from the check itself.
  test("check detail links to the docs section for that check's type", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await mockSloCoverage(page);
    const token = await getAuthToken(page);

    const stamp = Date.now();
    const tcp = await createCheck(page, token, "tcp", `E2E Docs TCP ${stamp}`, {
      host: "example.com",
      port: 443,
    });
    const dns = await createCheck(page, token, "dns", `E2E Docs DNS ${stamp}`, {
      host: "example.com",
      record_type: "A",
    });

    await page.goto(`orgs/test/checks/${tcp.uid}`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-detail-header")).toBeVisible();

    const tcpDocsLink = page.getByTestId("docs-link");
    await expect(tcpDocsLink).toBeVisible();
    await expect(tcpDocsLink).toHaveAttribute(
      "href",
      "/docs/features/check-types#tcp",
    );
    await expect(tcpDocsLink).toHaveAttribute("target", "_blank");
    await expect(tcpDocsLink).toHaveAttribute("rel", "noopener");

    // A different type on the same page must move the anchor — otherwise the
    // link is the generic page wearing a per-type costume.
    await page.goto(`orgs/test/checks/${dns.uid}`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-detail-header")).toBeVisible();

    await expect(page.getByTestId("docs-link")).toHaveAttribute(
      "href",
      "/docs/features/check-types#dns",
    );
  });

  test("audit log renders a docs link to audit-log", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    // The audit page sits behind the admin-gated Organization layout; the
    // test user is an admin of the "test" org, so a direct deep-link works.
    await page.goto("orgs/test/organization/audit");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("audit-page")).toBeVisible();

    const docsLink = page.getByTestId("docs-link");
    await expect(docsLink).toBeVisible();
    await expect(docsLink).toHaveAttribute("href", "/docs/features/audit-log");
  });

  test("a page with no docs mapping renders no docs link", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    // Jobs has no matching docs page (spec: "Pages with no matching docs ...
    // simply don't pass docsHref") — its layout carries no docsHref prop, so
    // no DocsLink should render anywhere on the page.
    //
    // The sidebar entry is super-admin-only since spec 2026-09-16-07. The
    // test user (test@test.com) IS a super admin in test mode
    // (server/test/testdata/testdata.go), so the link is there for them.
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Jobs" }).click();
    await page.waitForURL(/\/jobs/);
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("docs-link")).toHaveCount(0);
  });
});
