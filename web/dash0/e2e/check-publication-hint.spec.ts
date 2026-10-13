import { test, expect, API_BASE } from "./fixtures";
import type { Page } from "@playwright/test";

// Spec 2026-10-12-05: the post-create publication line is an inline hint on
// the check detail page, not a toast covering the header actions.

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

async function post(
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

async function del(page: Page, token: string, path: string) {
  await page.request.delete(`${API_BASE}${path}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}

async function createViaForm(page: Page, name: string) {
  await page.goto("orgs/test/checks/new");
  await page.getByTestId("check-name-input").fill(name);
  await page.getByTestId("check-url-input").fill("https://example.com/hint");
  await page.getByTestId("check-submit-button").click();
  await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
  await expect(page.getByRole("heading", { name })).toBeVisible();
}

test.describe("Post-create publication hint", () => {
  test("shows an inline hint with a working Publish action and never covers Edit", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await createViaForm(page, `E2E Hint ${Date.now()}`);

    const hint = page.getByTestId("check-publication-hint");
    await expect(hint).toBeVisible();
    await expect(hint).toContainText("Not on any status page yet");

    // The hint sits in the body, below the header.
    const headerBox = await page.getByTestId("check-detail-header").boundingBox();
    const hintBox = await hint.boundingBox();
    expect(hintBox!.y).toBeGreaterThanOrEqual(headerBox!.y + headerBox!.height - 1);

    // No toast overlaps the header action buttons.
    const header = page.getByTestId("check-detail-header");
    await expect(page.locator("[data-sonner-toast]").first()).toBeVisible();
    const actions = [
      await header.getByRole("link", { name: "Edit" }).boundingBox(),
      await page.getByTestId("check-more-actions").boundingBox(),
    ];
    for (const toast of await page.locator("[data-sonner-toast]").all()) {
      const box = await toast.boundingBox();
      if (!box) continue;
      for (const a of actions) {
        const overlaps =
          box.x < a!.x + a!.width &&
          box.x + box.width > a!.x &&
          box.y < a!.y + a!.height &&
          box.y + box.height > a!.y;
        expect(overlaps).toBe(false);
      }
    }

    // Edit is reachable with the pointer resting at the top-right corner.
    const viewport = page.viewportSize()!;
    await page.mouse.move(viewport.width - 40, 60);
    await header.getByRole("link", { name: "Edit" }).click({ timeout: 5000 });
    await page.waitForURL(/\/edit/);

    await page.goBack();
    await page.getByTestId("check-publication-hint-action").click();
    await expect(page.getByTestId("publish-create-status-page")).toBeVisible();
  });

  test("names the page and section when the check is already published, without the 'none' hint", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const suffix = Date.now().toString().slice(-9);
    const sp = await post(page, token, "/api/v1/orgs/test/status-pages", {
      name: `E2E Hint Page ${suffix}`,
      slug: `e2e-hint-${suffix}`,
      visibility: "public",
    });
    const section = await post(
      page,
      token,
      `/api/v1/orgs/test/status-pages/${sp.uid}/sections`,
      { name: `Core ${suffix}`, slug: `core-${suffix}` },
    );
    const check = await post(page, token, "/api/v1/orgs/test/checks", {
      type: "http",
      name: `e2e-hint-check-${suffix}`,
      config: { url: `https://httpbin.org/anything/${suffix}` },
      period: "00:05:00",
    });
    try {
      await post(
        page,
        token,
        `/api/v1/orgs/test/status-pages/${sp.uid}/sections/${section.uid}/resources`,
        { checkUid: check.uid },
      );
      await page.goto(`orgs/test/checks/${check.uid}?created=true`);
      const hint = page.getByTestId("check-publication-hint");
      await expect(hint).toContainText(`E2E Hint Page ${suffix}`);
      await expect(hint).toContainText(`Core ${suffix}`);
      await expect(hint).not.toContainText("Not on any status page");
      await expect(page.getByTestId("check-publication-hint-action")).toHaveCount(0);

      // Without ?created the hint is absent (positive control above).
      await page.goto(`orgs/test/checks/${check.uid}`);
      await expect(page.getByTestId("check-detail-header")).toBeVisible();
      await expect(page.getByTestId("check-publication-hint")).toHaveCount(0);
    } finally {
      await del(page, token, `/api/v1/orgs/test/checks/${check.uid}`);
      await del(page, token, `/api/v1/orgs/test/status-pages/${sp.uid}`);
    }
  });

  test("wraps on a 375px viewport without horizontal overflow", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.setViewportSize({ width: 375, height: 812 });
    await createViaForm(page, `E2E Hint Mobile ${Date.now()}`);
    await expect(page.getByTestId("check-publication-hint")).toBeVisible();
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
    );
    expect(overflow).toBeLessThanOrEqual(0);
  });
});
