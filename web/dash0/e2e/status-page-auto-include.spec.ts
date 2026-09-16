import { test, expect, API_BASE, disableHttpCache } from "./fixtures";
import type { Page } from "@playwright/test";

// Coverage for spec 2026-09-16-11 — the discoverability of auto-inclusion.
//
// Three facts, in the order they matter:
//
//  1. An "all checks" section really does adopt a check created afterwards,
//     with nobody touching the status page. This is the loop a user described
//     wanting while the feature already existed; if it is not provably true,
//     nothing else in the spec matters.
//  2. "Publish on a status page" reaches an EXISTING page. It used to navigate
//     to the create-a-page form, so the one button that sounds like the answer
//     offered a second page.
//  3. A check published only through its GROUP is reported as already
//     published. A status page resource targets a check XOR a check group, so
//     a naive checkUid scan sees nothing and happily publishes the check twice
//     — once folded into the roll-up, once beside it.

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
  expect(resp.status(), `POST ${path} -> ${await resp.text()}`).toBeLessThan(
    300,
  );
  return resp.json();
}

async function del(page: Page, token: string, path: string) {
  await page.request.delete(`${API_BASE}${path}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}

async function publicSection(
  page: Page,
  pageSlug: string,
  sectionSlug: string,
): Promise<{ check?: { name?: string } }[]> {
  const resp = await page.request.get(
    `${API_BASE}/api/v1/status-pages/test/${pageSlug}`,
  );
  expect(resp.status()).toBe(200);
  const body = await resp.json();
  const section = (body.sections ?? []).find(
    (candidate: { slug?: string }) => candidate.slug === sectionSlug,
  );
  return section?.resources ?? [];
}

test.describe("Status page auto-include discoverability", () => {
  test('an "all checks" section adopts a check created afterwards, with no further action', async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await disableHttpCache(page);

    const token = await getAuthToken(page);
    const suffix = Date.now().toString().slice(-9);
    const pageSlug = `e2e-all-page-${suffix}`.slice(0, 40);

    const statusPage = await api(page, token, "/api/v1/orgs/test/status-pages", {
      name: `E2E All Page ${suffix}`,
      slug: pageSlug,
      visibility: "public",
    });

    let checkUid: string | undefined;
    try {
      await api(
        page,
        token,
        `/api/v1/orgs/test/status-pages/${statusPage.uid}/sections`,
        { name: "Everything", slug: "everything", selector: { all: true } },
      );

      const checkName = `E2E All Check ${suffix}`;
      const check = await api(page, token, "/api/v1/orgs/test/checks", {
        type: "http",
        name: checkName,
        slug: `e2e-all-check-${suffix}`.slice(0, 40),
        config: { url: "https://httpbin.org/anything/all-rule" },
        period: "00:05:00",
      });
      checkUid = check.uid as string;

      // No status page is opened, edited or loaded between the check being
      // created and this read. That is the whole assertion.
      const resources = await publicSection(page, pageSlug, "everything");
      expect(
        resources.some((resource) => resource.check?.name === checkName),
      ).toBe(true);
    } finally {
      await del(
        page,
        token,
        `/api/v1/orgs/test/status-pages/${statusPage.uid}`,
      );
      if (checkUid) {
        await del(page, token, `/api/v1/orgs/test/checks/${checkUid}`);
      }
    }
  });

  test("the publish dialog adds the check to an EXISTING page's section", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const suffix = Date.now().toString().slice(-9);

    const statusPage = await api(page, token, "/api/v1/orgs/test/status-pages", {
      name: `E2E Existing Page ${suffix}`,
      slug: `e2e-existing-${suffix}`.slice(0, 40),
      visibility: "public",
    });
    const section = await api(
      page,
      token,
      `/api/v1/orgs/test/status-pages/${statusPage.uid}/sections`,
      { name: "Core", slug: "core" },
    );

    const checkName = `E2E Publish Check ${suffix}`;
    const check = await api(page, token, "/api/v1/orgs/test/checks", {
      type: "http",
      name: checkName,
      slug: `e2e-pub-check-${suffix}`.slice(0, 40),
      config: { url: "https://httpbin.org/anything/publish" },
      period: "00:05:00",
    });

    try {
      await page.goto(`orgs/test/checks/${check.uid}`);
      await page.waitForLoadState("networkidle");

      await page.getByTestId("publish-status-page-link").click();

      const addButton = page.getByTestId(
        `publish-add-to-section-${section.uid}`,
      );
      await expect(addButton).toBeVisible({ timeout: 15000 });
      await addButton.click();

      // Once it lands, the section reports the check as already here and the
      // add button is gone — no second row on the next click.
      await expect(
        page.getByTestId(`publish-section-published-${section.uid}`),
      ).toBeVisible({ timeout: 15000 });
      await expect(addButton).toHaveCount(0);

      const resp = await page.request.get(
        `${API_BASE}/api/v1/orgs/test/status-pages/${statusPage.uid}/sections/${section.uid}/resources`,
        { headers: { Authorization: `Bearer ${token}` } },
      );
      expect(resp.status()).toBe(200);
      const body = await resp.json();
      expect(body.data ?? []).toHaveLength(1);
      expect(body.data[0].checkUid).toBe(check.uid);
    } finally {
      await del(
        page,
        token,
        `/api/v1/orgs/test/status-pages/${statusPage.uid}`,
      );
      await del(page, token, `/api/v1/orgs/test/checks/${check.uid}`);
    }
  });

  test("a check published only through its group is reported, not offered a duplicate", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const suffix = Date.now().toString().slice(-9);

    const group = await api(page, token, "/api/v1/orgs/test/check-groups", {
      name: `E2E Group ${suffix}`,
      slug: `e2e-pub-grp-${suffix}`.slice(0, 40),
    });
    const check = await api(page, token, "/api/v1/orgs/test/checks", {
      type: "http",
      name: `E2E Grouped Check ${suffix}`,
      slug: `e2e-grp-check-${suffix}`.slice(0, 40),
      config: { url: "https://httpbin.org/anything/grouped" },
      period: "00:05:00",
      checkGroupUid: group.uid,
    });
    const statusPage = await api(page, token, "/api/v1/orgs/test/status-pages", {
      name: `E2E Group Page ${suffix}`,
      slug: `e2e-grp-page-${suffix}`.slice(0, 40),
      visibility: "public",
    });
    const section = await api(
      page,
      token,
      `/api/v1/orgs/test/status-pages/${statusPage.uid}/sections`,
      { name: "Platform", slug: "platform" },
    );
    // The group, not the check: nothing on this page names the check.
    await api(
      page,
      token,
      `/api/v1/orgs/test/status-pages/${statusPage.uid}/sections/${section.uid}/resources`,
      { checkGroupUid: group.uid },
    );

    try {
      await page.goto(`orgs/test/checks/${check.uid}`);
      await page.waitForLoadState("networkidle");

      await page.getByTestId("publish-status-page-link").click();

      await expect(page.getByTestId("publish-already-published")).toBeVisible({
        timeout: 15000,
      });
      await expect(page.getByTestId("publish-already-group")).toBeVisible();
      // The section holding the group offers no way to publish the check a
      // second time.
      await expect(
        page.getByTestId(`publish-add-to-section-${section.uid}`),
      ).toHaveCount(0);
    } finally {
      await del(
        page,
        token,
        `/api/v1/orgs/test/status-pages/${statusPage.uid}`,
      );
      await del(page, token, `/api/v1/orgs/test/checks/${check.uid}`);
      await del(page, token, `/api/v1/orgs/test/check-groups/${group.uid}`);
    }
  });
});
