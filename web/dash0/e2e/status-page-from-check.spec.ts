import { test, expect, API_BASE } from "./fixtures";
import type { Page } from "@playwright/test";

// Coverage for spec 2026-08-28-16: publishing one check on a status page used
// to take three separate manual create flows (page, then section, then
// resource), with no entry point from the check itself. This exercises the
// collapsed flow end to end: from the check detail page, "Publish on a
// status page" pre-fills a create form that lands the check straight into
// the page's default section — verified both in the dashboard and on the
// PUBLIC page.

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  const body = await resp.json();
  return body.accessToken;
}

async function api(
  page: Page,
  token: string,
  path: string,
  data: Record<string, unknown>,
): Promise<Record<string, unknown>> {
  const resp = await page.request.post(`${API_BASE}${path}`, {
    headers: { Authorization: `Bearer ${token}` },
    data,
  });
  expect(resp.status(), `POST ${path} -> ${await resp.text()}`).toBeLessThan(300);
  return resp.json();
}

test.describe("Publish a check on a status page", () => {
  test("check detail -> prefilled create form -> the check appears on the page and publicly", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const suffix = Date.now().toString().slice(-9);

    const checkName = `e2e-publish-check-${suffix}`;
    const check = await api(page, token, "/api/v1/orgs/test/checks", {
      type: "http",
      name: checkName,
      config: { url: `https://httpbin.org/anything/${suffix}` },
      period: "00:05:00",
    });

    // --- Start from the check detail page ---
    await page.goto(`orgs/test/checks/${check.uid}`);
    await page.waitForLoadState("networkidle");

    const publishLink = page.getByTestId("publish-status-page-link");
    await expect(publishLink).toBeVisible();
    await publishLink.click();

    await page.waitForURL(/\/status-pages\/new\?checkUid=/);
    await page.waitForLoadState("networkidle");

    // The check is shown as pre-attached.
    const prefilledChip = page.getByTestId("status-page-prefilled-check");
    await expect(prefilledChip).toBeVisible();
    await expect(prefilledChip).toContainText(checkName);

    // Name is prefilled from the org; slug auto-derives. Make the slug
    // unique to this run so repeated test runs never collide.
    const slugField = page.locator("#slug");
    await slugField.fill(`e2e-publish-${suffix}`.slice(0, 40));

    await page.getByRole("button", { name: "Create Status Page" }).click();

    // Lands on the page detail route with the check already listed under the
    // default "Services" section — no "Add Section" / "Add Component" step.
    await page.waitForURL(/\/status-pages\/(?!new)[^/]+$/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    await expect(page.getByText("Services").first()).toBeVisible();
    const row = page.getByTestId("resource-row-name").filter({ hasText: checkName });
    await expect(row).toHaveCount(1);

    // --- The public page renders the check under the same section ---
    const statusPageUrl = page.url();
    const pageUid = statusPageUrl.split("/status-pages/")[1];

    // The public view endpoint is keyed by SLUG, not UID — resolve it via
    // the authenticated detail endpoint first.
    const adminResp = await page.request.get(
      `${API_BASE}/api/v1/orgs/test/status-pages/${pageUid}`,
      { headers: { Authorization: `Bearer ${token}` } },
    );
    expect(adminResp.status()).toBe(200);
    const adminJSON = await adminResp.json();
    const publicSlug = adminJSON.slug as string;

    const publicResp = await page.request.get(
      `${API_BASE}/api/v1/status-pages/test/${publicSlug}`,
    );
    expect(publicResp.status()).toBe(200);
    const publicJSON = await publicResp.json();
    const servicesSection = (publicJSON.sections ?? []).find(
      (section: { slug?: string }) => section.slug === "services",
    );
    expect(servicesSection?.resources ?? []).toHaveLength(1);
    expect(servicesSection.resources[0].checkUid).toBe(check.uid);

    await page.goto(`/status0/test/${publicSlug}`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByText(checkName).first()).toBeVisible();
  });
});
