/**
 * The organization's name and logo on the public status page and its TV view
 * (spec 2026-10-03-09).
 *
 * Mocked block: the precedence rules (page logo, then org logo, then the
 * SolidPing mark) and the TV testids. Real-server block: upload an org logo
 * through the owner API and prove it is served unauthenticated under the
 * status0 CSP (`img-src 'self'`) on both surfaces, with zero violations.
 */
import { test, expect, type Page } from "@playwright/test";
import { API_BASE as BASE, STATUS_BASE, resolveDefaultStatusPage } from "./fixtures";

const ORG = "e2e-org-identity";
const SLUG = "identity";

function payload(overrides: Record<string, unknown> = {}) {
  return {
    uid: "33333333-3333-3333-3333-333333333333",
    name: "Identity Page",
    slug: SLUG,
    visibility: "public",
    isDefault: false,
    enabled: true,
    showAvailability: false,
    showResponseTime: false,
    historyDays: 90,
    historyPeriod: "90d",
    overallStatus: "operational",
    activeIncidents: [],
    sections: [],
    availabilityThresholds: { thresholdUp: 99.9, thresholdDegraded: 99 },
    ...overrides,
  };
}

async function mock(page: Page, overrides: Record<string, unknown>) {
  const json = (body: unknown) => ({
    status: 200,
    contentType: "application/json",
    body: JSON.stringify(body),
  });

  await page.route(`**/api/v1/status-pages/${ORG}/${SLUG}*`, (route) =>
    route.fulfill(json(payload(overrides))),
  );
  await page.route(`**/api/v1/status-pages/${ORG}/${SLUG}/incidents*`, (route) =>
    route.fulfill(json({ data: [] })),
  );
  await page.route(`**/api/v1/status-pages/${ORG}/${SLUG}/summary*`, (route) =>
    route.fulfill(
      json({
        status: "operational",
        counts: { operational: 0, degraded: 0, down: 0, maintenance: 0, unknown: 0 },
        page: { name: "Identity Page", slug: SLUG, url: "" },
        generatedAt: "2026-10-03T12:00:00Z",
      }),
    ),
  );
}

const PAGE_URL = `${BASE}${STATUS_BASE}/${ORG}/${SLUG}`;

test.describe("Org identity (mocked payload)", () => {
  test("brand bar shows the org logo and name when the page has no logo", async ({
    page,
  }) => {
    await mock(page, { orgName: "Acme", orgLogoUrl: "/pub/assets/org-logo" });
    await page.goto(PAGE_URL);

    const logo = page.getByTestId("status-page-org-logo");
    await expect(logo).toHaveAttribute("src", "/pub/assets/org-logo");
    await expect(page.getByTestId("status-page-org-name")).toHaveText("Acme");
    await expect(page.getByTestId("status-page-logo")).toHaveCount(0);
  });

  test("the page logo wins over the org logo", async ({ page }) => {
    await mock(page, {
      orgName: "Acme",
      orgLogoUrl: "/pub/assets/org-logo",
      logoUrl: "/pub/status-page-assets/page-logo",
    });
    await page.goto(PAGE_URL);

    await expect(page.getByTestId("status-page-logo")).toHaveAttribute(
      "src",
      "/pub/status-page-assets/page-logo",
    );
    await expect(page.getByTestId("status-page-org-logo")).toHaveCount(0);
    await expect(page.getByTestId("status-page-org-name")).toHaveText("Acme");
  });

  test("falls back to the SolidPing mark when neither page nor org has a logo", async ({
    page,
  }) => {
    await mock(page, { orgName: "Acme" });
    await page.goto(PAGE_URL);

    await expect(page.getByTestId("status-page-org-name")).toHaveText("Acme");
    await expect(page.getByTestId("status-page-logo")).toHaveCount(0);
    await expect(page.getByTestId("status-page-org-logo")).toHaveCount(0);
    await expect(page.locator("header .sp-logo")).toBeVisible();
  });

  test("hideBranding does not hide the org identity", async ({ page }) => {
    await mock(page, {
      orgName: "Acme",
      orgLogoUrl: "/pub/assets/org-logo",
      hideBranding: true,
    });
    await page.goto(PAGE_URL);

    await expect(page.getByTestId("status-page-org-logo")).toBeVisible();
    await expect(page.getByTestId("status-page-org-name")).toHaveText("Acme");
  });

  test("TV board shows the org name and logo", async ({ page }) => {
    await mock(page, { orgName: "Acme", orgLogoUrl: "/pub/assets/org-logo" });
    await page.goto(`${PAGE_URL}/tv`);

    await expect(page.getByTestId("tv-org-name")).toHaveText("Acme");
    await expect(page.getByTestId("tv-org-logo")).toHaveAttribute(
      "src",
      "/pub/assets/org-logo",
    );
    await expect(page.getByTestId("tv-headline")).toBeVisible();
  });

  test("TV board has no org logo element when the org has none", async ({
    page,
  }) => {
    await mock(page, { orgName: "Acme" });
    await page.goto(`${PAGE_URL}/tv`);

    await expect(page.getByTestId("tv-org-name")).toHaveText("Acme");
    await expect(page.getByTestId("tv-org-logo")).toHaveCount(0);
  });
});

// 1x1 transparent PNG.
const PNG = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==",
  "base64",
);

test.describe.serial("Org identity (real server, CSP)", () => {
  let token = "";

  test.beforeAll(async () => {
    const res = await fetch(`${BASE}/api/v1/auth/login`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ org: "test", email: "test@test.com", password: "test" }),
    });
    token = ((await res.json()) as { accessToken: string }).accessToken;

    const form = new FormData();
    form.append("logo", new Blob([PNG], { type: "image/png" }), "logo.png");
    const up = await fetch(`${BASE}/api/v1/orgs/test/logo`, {
      method: "POST",
      headers: { Authorization: `Bearer ${token}` },
      body: form,
    });
    expect(up.ok).toBeTruthy();
  });

  test.afterAll(async () => {
    await fetch(`${BASE}/api/v1/orgs/test/logo`, {
      method: "DELETE",
      headers: { Authorization: `Bearer ${token}` },
    });
  });

  for (const suffix of ["", "/tv"]) {
    test(`org logo loads under the CSP on ${suffix || "the page"}`, async ({ page }) => {
      const target = await resolveDefaultStatusPage();
      await page.addInitScript(() => {
        const store: string[] = [];
        (window as unknown as { __csp: string[] }).__csp = store;
        document.addEventListener("securitypolicyviolation", (e) =>
          store.push(`${e.violatedDirective} ${e.blockedURI}`),
        );
      });

      await page.goto(`${BASE}${STATUS_BASE}/${target.org}/${target.slug}${suffix}`);
      const logo = page.getByTestId(suffix ? "tv-org-logo" : "status-page-org-logo");
      await expect(logo).toBeVisible();
      await expect
        .poll(() => logo.evaluate((img: HTMLImageElement) => img.naturalWidth))
        .toBeGreaterThan(0);

      const violations = await page.evaluate(
        () => (window as unknown as { __csp: string[] }).__csp,
      );
      expect(violations).toEqual([]);
    });
  }
});
