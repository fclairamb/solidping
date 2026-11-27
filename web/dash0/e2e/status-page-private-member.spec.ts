import { test, expect, API_BASE, STATUS_BASE, uniqueStamp } from "./fixtures";
import type { Page } from "@playwright/test";

// Spec 2026-11-27-01: a `private` status page is visible to the signed-in
// members of its organization, and to nobody else. The authorization matrix
// (other org, invalid cookie, bearer, kiosk, password pages, cache headers)
// lives in the Go tests (internal/app/status_page_member_access_test.go);
// this proves the browser path: the dashboard's "View" button opens the page
// for a member, and the same URL is "not found" once signed out.

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });

  return (await resp.json()).accessToken;
}

async function createPage(
  page: Page,
  token: string,
  extra: Record<string, unknown> = {},
): Promise<{ uid: string; slug: string }> {
  const slug = `e2e-private-${uniqueStamp()}`.slice(0, 40);
  const resp = await page.request.post(
    `${API_BASE}/api/v1/orgs/test/status-pages`,
    {
      headers: { Authorization: `Bearer ${token}` },
      data: { name: `E2E Private ${slug}`, slug, ...extra },
    },
  );

  expect(
    resp.status(),
    `create status page -> ${await resp.text()}`,
  ).toBeLessThan(300);

  return { uid: (await resp.json()).uid, slug };
}

async function deletePage(page: Page, token: string, uid: string) {
  await page.request.delete(
    `${API_BASE}/api/v1/orgs/test/status-pages/${uid}`,
    { headers: { Authorization: `Bearer ${token}` } },
  );
}

test.describe("Private status page, member view", () => {
  test.describe.configure({ timeout: 90_000 });

  test("a signed-in member opens it from View; signed out it is not found", async ({
    authenticatedPage: page,
    browser,
  }) => {
    const token = await getAuthToken(page);
    const { uid, slug } = await createPage(page, token, {
      visibility: "private",
    });

    try {
      await page.goto(`orgs/test/status-pages/${uid}`);
      const view = page.getByTestId("status-page-view-link");
      await expect(view).toBeVisible({ timeout: 30_000 });

      const [statusTab] = await Promise.all([
        page.context().waitForEvent("page"),
        view.click(),
      ]);

      await expect(
        statusTab.getByTestId("status-page-private-banner"),
      ).toBeVisible({ timeout: 30_000 });
      await expect(statusTab.getByTestId("overall-status-banner")).toBeVisible();

      // Signed out: a fresh context holds no session cookie.
      const anonymous = await browser.newContext();
      try {
        const visitor = await anonymous.newPage();
        await visitor.goto(`${API_BASE}${STATUS_BASE}/test/${slug}`);
        await expect(visitor.getByText("Status Page Not Found")).toBeVisible({
          timeout: 30_000,
        });
        await expect(
          visitor.getByTestId("status-page-private-banner"),
        ).toHaveCount(0);
      } finally {
        await anonymous.close();
      }
    } finally {
      await deletePage(page, token, uid);
    }
  });

  test("a public page shows no private banner to a member", async ({
    authenticatedPage: page,
  }) => {
    const token = await getAuthToken(page);
    const { uid, slug } = await createPage(page, token);

    try {
      await page.goto(`${API_BASE}${STATUS_BASE}/test/${slug}`);
      await expect(page.getByTestId("overall-status-banner")).toBeVisible({
        timeout: 30_000,
      });
      await expect(page.getByTestId("status-page-private-banner")).toHaveCount(0);
    } finally {
      await deletePage(page, token, uid);
    }
  });

  test("the edit form's Save button is reachable without scrolling", async ({
    authenticatedPage: page,
  }) => {
    const token = await getAuthToken(page);
    const { uid } = await createPage(page, token);

    try {
      await page.goto(`orgs/test/status-pages/${uid}/edit`);
      const actions = page.getByTestId("status-page-form-actions");
      await expect(actions).toBeVisible({ timeout: 30_000 });

      // Sticky at the bottom of the viewport: in view on load, above the
      // cards (subscribers, custom domain, ...) that follow the form.
      await expect(page.getByTestId("status-page-form-submit")).toBeInViewport();
      await expect(page.getByTestId("status-page-form-cancel")).toBeInViewport();
    } finally {
      await deletePage(page, token, uid);
    }
  });
});
