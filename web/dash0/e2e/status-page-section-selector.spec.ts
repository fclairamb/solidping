import { test, expect, API_BASE, disableHttpCache } from "./fixtures";
import type { Page } from "@playwright/test";

// Coverage for spec 2026-08-29-11: a status page SECTION can carry a dynamic
// membership rule, and the system keeps its components in sync.
//
// The requirement worth testing end to end is the one that removes a silent
// failure: a check created AFTER the page was built, carrying the right label,
// must reach the public page with nobody touching that page. So the flow here
// deliberately never returns to the status page editor between creating the
// check and reading the public payload.

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

async function patch(
  page: Page,
  token: string,
  path: string,
  data: Record<string, unknown>,
): Promise<Record<string, unknown>> {
  const resp = await page.request.patch(`${API_BASE}${path}`, {
    headers: { Authorization: `Bearer ${token}` },
    data,
  });
  expect(resp.status(), `PATCH ${path} -> ${await resp.text()}`).toBeLessThan(
    300,
  );
  return resp.json();
}

/** Reads the public payload and returns the named section's resources. */
async function publicSectionResources(
  page: Page,
  pageSlug: string,
  sectionSlug: string,
): Promise<{ check?: { name?: string }; managedBySelector?: boolean }[]> {
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

test.describe("Status page section selectors", () => {
  test("a label selector adopts a check created later, and drops it when the label goes", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await disableHttpCache(page);

    const token = await getAuthToken(page);
    const suffix = Date.now().toString().slice(-9);
    const labelKey = `e2e-sel-${suffix}`.slice(0, 50);

    const pageSlug = `e2e-sel-page-${suffix}`.slice(0, 40);
    const statusPage = await api(page, token, "/api/v1/orgs/test/status-pages", {
      name: `E2E Selector Page ${suffix}`,
      slug: pageSlug,
      visibility: "public",
    });

    // The section exists BEFORE the check does — that is the whole point.
    await api(
      page,
      token,
      `/api/v1/orgs/test/status-pages/${statusPage.uid}/sections`,
      {
        name: "Auto",
        slug: "auto",
        selector: { labels: { [labelKey]: "true" } },
      },
    );

    // Nothing matches yet.
    expect(await publicSectionResources(page, pageSlug, "auto")).toHaveLength(0);

    // A brand-new labelled check. No status page is opened, edited or even
    // loaded between here and the assertion below.
    const checkName = `E2E Selector Check ${suffix}`;
    const check = await api(page, token, "/api/v1/orgs/test/checks", {
      type: "http",
      name: checkName,
      slug: `e2e-sel-check-${suffix}`.slice(0, 40),
      config: { url: "https://httpbin.org/anything/selector" },
      period: "00:05:00",
      labels: { [labelKey]: "true" },
    });

    const adopted = await publicSectionResources(page, pageSlug, "auto");
    expect(adopted).toHaveLength(1);
    expect(adopted[0].check?.name).toBe(checkName);
    // The public payload never spells out the rule that put it there, nor
    // that a rule was involved at all.
    expect(adopted[0].managedBySelector).toBeUndefined();

    // The dashboard marks the row as automatic and offers no delete control
    // for it — the rule owns it.
    await page.goto(`orgs/test/status-pages/${statusPage.uid}`);
    await page.waitForLoadState("networkidle");
    await expect(
      page.getByTestId("resource-row-name").filter({ hasText: checkName }),
    ).toHaveCount(1);
    await expect(page.getByTestId("resource-row-auto-badge")).toHaveCount(1);

    // Removing the label removes the component. `labels: {}` is a
    // replace-all update, exactly what the dashboard sends.
    await patch(page, token, `/api/v1/orgs/test/checks/${check.uid}`, {
      labels: {},
    });

    expect(await publicSectionResources(page, pageSlug, "auto")).toHaveLength(0);
  });

  test("enabling a rule on a public page warns about future checks", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const suffix = Date.now().toString().slice(-9);

    const statusPage = await api(page, token, "/api/v1/orgs/test/status-pages", {
      name: `E2E Warn Page ${suffix}`,
      slug: `e2e-warn-page-${suffix}`.slice(0, 40),
      visibility: "public",
    });

    await page.goto(`orgs/test/status-pages/${statusPage.uid}`);
    await page.waitForLoadState("networkidle");

    await page.getByRole("button", { name: "Add Section" }).first().click();

    const membership = page.getByTestId("section-membership");
    await expect(membership).toBeVisible();

    // Manual is the default, and it says nothing alarming because there is
    // nothing alarming to say.
    await expect(
      page.getByTestId("section-membership-public-warning"),
    ).toHaveCount(0);

    // "All checks" on a public page gets the strongest copy: FUTURE checks.
    await page.getByTestId("section-membership-all").click();
    const warning = page.getByTestId("section-membership-public-warning");
    await expect(warning).toBeVisible();
    await expect(warning).toContainText("future");

    // The label mode warns too — a rule is a rule — and recommends nothing
    // stronger than it needs to.
    await page.getByTestId("section-membership-labels").click();
    await expect(warning).toBeVisible();

    // Back to manual, warning gone.
    await page.getByTestId("section-membership-manual").click();
    await expect(
      page.getByTestId("section-membership-public-warning"),
    ).toHaveCount(0);
  });

  test("a private page enabling a rule gets no warning", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const suffix = Date.now().toString().slice(-9);

    const statusPage = await api(page, token, "/api/v1/orgs/test/status-pages", {
      name: `E2E Private Page ${suffix}`,
      slug: `e2e-priv-page-${suffix}`.slice(0, 40),
      visibility: "private",
    });

    await page.goto(`orgs/test/status-pages/${statusPage.uid}`);
    await page.waitForLoadState("networkidle");

    await page.getByRole("button", { name: "Add Section" }).first().click();
    await page.getByTestId("section-membership-all").click();

    // Nothing is disclosed by a private page, so nothing is warned about —
    // private + "all checks" + a kiosk token is the recommended wallboard.
    await expect(
      page.getByTestId("section-membership-public-warning"),
    ).toHaveCount(0);
  });
});
