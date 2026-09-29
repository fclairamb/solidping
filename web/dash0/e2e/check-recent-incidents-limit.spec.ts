import { test, expect, DASH_BASE, mockSloCoverage } from "./fixtures";
import type { Page } from "@playwright/test";

const CHECK_UID = "00000000-0000-0000-0000-000000000012";

function makeIncidents(n: number) {
  // Deliberately oldest-first so the client-side sort is what picks "latest".
  return Array.from({ length: n }, (_, i) => ({
    uid: `10000000-0000-0000-0000-${String(i + 1).padStart(12, "0")}`,
    number: i + 1,
    checkUid: CHECK_UID,
    state: "resolved",
    failureCount: 3,
    startedAt: new Date(Date.UTC(2026, 0, 1 + i)).toISOString(),
    resolvedAt: new Date(Date.UTC(2026, 0, 1 + i, 1)).toISOString(),
  }));
}

async function mockIncidents(page: Page, n: number) {
  await page.route("**/api/v1/orgs/*/incidents?*", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: makeIncidents(n) }),
    }),
  );
}

test.beforeEach(async ({ authenticatedPage }) => {
  await mockSloCoverage(authenticatedPage);
});

test.describe("Check detail — Recent Incidents card limit", () => {
  test("15 incidents: shows the 10 latest and a View all link", async ({
    authenticatedPage: page,
  }) => {
    await mockIncidents(page, 15);
    await page.goto(`${DASH_BASE}/orgs/test/checks/${CHECK_UID}`);

    const card = page.getByTestId("recent-incidents-card");
    await expect(card).toBeVisible();
    await expect(card.locator('[data-testid^="incident-row-"]')).toHaveCount(
      10,
    );
    // Incidents 6..15 are the latest 10; 1..5 must be absent.
    for (let i = 6; i <= 15; i++) {
      await expect(
        card.getByTestId(
          `incident-row-10000000-0000-0000-0000-${String(i).padStart(12, "0")}`,
        ),
      ).toHaveCount(1);
    }
    await expect(
      card.getByTestId("incident-row-10000000-0000-0000-0000-000000000005"),
    ).toHaveCount(0);

    const viewAll = card.getByTestId("recent-incidents-view-all");
    await expect(viewAll).toBeVisible();
    await expect(viewAll).toHaveAttribute(
      "href",
      new RegExp(`checkUid=${CHECK_UID}`),
    );
    await expect(viewAll).toHaveAttribute("href", /state=all/);
  });

  test("3 incidents: 3 rows, no View all link, number links to the incident", async ({
    authenticatedPage: page,
  }) => {
    await mockIncidents(page, 3);
    await page.goto(`${DASH_BASE}/orgs/test/checks/${CHECK_UID}`);

    const card = page.getByTestId("recent-incidents-card");
    await expect(card.locator('[data-testid^="incident-row-"]')).toHaveCount(3);
    await expect(card.getByTestId("recent-incidents-view-all")).toHaveCount(0);

    const numbers = card.getByTestId("incident-number");
    await expect(numbers).toHaveCount(3);
    await expect(numbers.first()).toHaveText("#3");

    await numbers.first().click();
    await page.waitForURL(/\/incidents\/10000000-0000-0000-0000-000000000003/);
  });

  test("0 incidents: the card stays hidden", async ({
    authenticatedPage: page,
  }) => {
    await mockIncidents(page, 0);
    await page.goto(`${DASH_BASE}/orgs/test/checks/${CHECK_UID}`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("recent-incidents-card")).toHaveCount(0);
  });
});
