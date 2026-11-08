import {
  test,
  expect,
  API_BASE,
  DASH_BASE,
  escapeRegExp,
  getAuthToken,
  type Page,
} from "./fixtures";

/**
 * Spec 2026-11-08-02: a permanent "Star on GitHub" sidebar link and a
 * one-time prompt on the dashboard home. The eligibility rule is unit-tested
 * (src/lib/github-star.test.ts); this file asserts the wiring: the stats the
 * dashboard really polls feed the card, the outcome is persisted server-side
 * (a second browser context, i.e. another device, must not see it again), and
 * the operator switch removes both surfaces.
 */

const REPO_URL = "https://github.com/fclairamb/solidping";
const DAY_MS = 24 * 60 * 60 * 1000;

async function stubStats(
  page: Page,
  opts: { ageDays: number; down?: number },
): Promise<void> {
  await page.route("**/api/v1/orgs/test/checks/stats", async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    await route.fulfill({
      response,
      json: {
        ...body,
        down: opts.down ?? 0,
        hardDown: opts.down ?? 0,
        oldestEnabledCheckCreatedAt: new Date(
          Date.now() - opts.ageDays * DAY_MS,
        ).toISOString(),
      },
    });
  });
}

async function stubStarConfig(page: Page, enabled: boolean): Promise<void> {
  await page.route("**/api/v1/config", async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    await route.fulfill({
      response,
      json: { ...body, githubStar: { enabled } },
    });
  });
}

async function openHome(page: Page, waitForState = true): Promise<void> {
  // Settle on the data the decision is made from, so an "absent" assertion
  // cannot pass before the page has even asked.
  const statsLoaded = page.waitForResponse((r) =>
    r.url().includes("/api/v1/orgs/test/checks/stats"),
  );
  const stateLoaded = waitForState
    ? page.waitForResponse((r) =>
        r.url().includes("/api/v1/me/ui-state/github-star"),
      )
    : Promise.resolve(null);
  await page.goto("orgs/test");
  await statsLoaded;
  await stateLoaded;
  await expect(page).toHaveURL(new RegExp(`${escapeRegExp(DASH_BASE)}/orgs/test`));
}

test.describe("GitHub star prompt", () => {
  // They all share the one test user's `github-star` entry.
  test.describe.configure({ mode: "serial" });

  test.beforeEach(async ({ authenticatedPage: page }) => {
    const token = await getAuthToken(page);
    const headers = { Authorization: `Bearer ${token}` };
    await page.request.delete(`${API_BASE}/api/v1/me/ui-state/github-star`, {
      headers,
    });
    // The prompt waits for the getting-started checklist to be out of the way.
    await page.request.put(`${API_BASE}/api/v1/me/ui-state/onboarding.test`, {
      headers,
      data: { dismissedAt: new Date().toISOString() },
    });
  });

  test("sidebar link points at the repository", async ({
    authenticatedPage: page,
  }) => {
    await openHome(page);
    const link = page.getByTestId("github-star-sidebar");
    await expect(link).toBeVisible();
    await expect(link).toHaveAttribute("href", REPO_URL);
    await expect(link).toHaveAttribute("target", "_blank");
  });

  test("Not now is final, on every device", async ({
    authenticatedPage: page,
    browser,
    authWorkerStorageState,
  }) => {
    await stubStats(page, { ageDays: 8 });
    await openHome(page);
    await expect(page.getByTestId("github-star-prompt")).toBeVisible();

    const saved = page.waitForResponse(
      (r) =>
        r.url().includes("/api/v1/me/ui-state/github-star") &&
        r.request().method() === "PUT" &&
        r.ok(),
    );
    await page.getByTestId("github-star-prompt-dismiss").click();
    await expect(page.getByTestId("github-star-prompt")).toHaveCount(0);
    await saved;

    await page.reload();
    await expect(page.getByTestId("github-star-sidebar")).toBeVisible();
    await expect(page.getByTestId("github-star-prompt")).toHaveCount(0);

    // Another device: a brand new context sharing only the account.
    const other = await browser.newContext({ storageState: authWorkerStorageState });
    const otherPage = await other.newPage();
    await stubStats(otherPage, { ageDays: 8 });
    await openHome(otherPage);
    await expect(otherPage.getByTestId("github-star-sidebar")).toBeVisible();
    await expect(otherPage.getByTestId("github-star-prompt")).toHaveCount(0);
    await other.close();
  });

  test("Star on GitHub opens the repo and the card never comes back", async ({
    authenticatedPage: page,
  }) => {
    await stubStats(page, { ageDays: 8 });
    await openHome(page);
    await expect(page.getByTestId("github-star-prompt")).toBeVisible();

    // Wait for the shownAt write, so the outcome PUT is not racing it.
    await expect
      .poll(async () => {
        const token = await getAuthToken(page);
        const r = await page.request.get(
          `${API_BASE}/api/v1/me/ui-state/github-star`,
          { headers: { Authorization: `Bearer ${token}` } },
        );
        return r.ok();
      })
      .toBe(true);

    const popupPromise = page.context().waitForEvent("page");
    await page.getByTestId("github-star-prompt-star").click();
    const popup = await popupPromise;
    expect(popup.url()).toContain("github.com/fclairamb/solidping");
    await popup.close();
    await expect(page.getByTestId("github-star-prompt")).toHaveCount(0);

    await expect
      .poll(async () => {
        const token = await getAuthToken(page);
        const r = await page.request.get(
          `${API_BASE}/api/v1/me/ui-state/github-star`,
          { headers: { Authorization: `Bearer ${token}` } },
        );
        return (await r.json()).value?.outcome;
      })
      .toBe("starred");

    await page.reload();
    await expect(page.getByTestId("github-star-prompt")).toHaveCount(0);
  });

  test("not eligible: check younger than 7 days", async ({
    authenticatedPage: page,
  }) => {
    await stubStats(page, { ageDays: 2 });
    await openHome(page);
    await expect(page.getByTestId("github-star-sidebar")).toBeVisible();
    await expect(page.getByTestId("github-star-prompt")).toHaveCount(0);
  });

  test("not eligible: something is down", async ({
    authenticatedPage: page,
  }) => {
    await stubStats(page, { ageDays: 8, down: 1 });
    await openHome(page);
    await expect(page.getByTestId("github-star-sidebar")).toBeVisible();
    await expect(page.getByTestId("github-star-prompt")).toHaveCount(0);
  });

  test("operator switch removes link and prompt", async ({
    authenticatedPage: page,
  }) => {
    await stubStarConfig(page, false);
    await stubStats(page, { ageDays: 8 });
    await openHome(page, false);
    await expect(page.getByTestId("user-menu-button")).toBeVisible();
    await expect(page.getByTestId("github-star-sidebar")).toHaveCount(0);
    await expect(page.getByTestId("github-star-prompt")).toHaveCount(0);
  });
});
