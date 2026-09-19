import { test, expect, DASH_BASE, mockSloCoverage } from "./fixtures";

// Deterministically seeded in test mode (server/test/testdata/testdata.go,
// createTestIncidentNotification): a down check with exactly one active
// incident. Gives this spec a stable, non-zero incidents count to click
// through from.
const CHECK_WITH_INCIDENT_UID = "00000000-0000-0000-0000-000000000012";
const INCIDENT_UID = "00000000-0000-0000-0000-000000000013";

test.beforeEach(async ({ authenticatedPage }) => {
  await mockSloCoverage(authenticatedPage);
});

test.describe("Check detail — Incidents card click-through", () => {
  test("clicking the Incidents card opens the incidents list filtered to this check", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto(`${DASH_BASE}/orgs/test/checks/${CHECK_WITH_INCIDENT_UID}`);
    await page.waitForLoadState("networkidle");

    const card = page.getByTestId("incidents-card");
    await expect(card).toBeVisible();
    await expect(card).toContainText("1");

    // The card must actually be the interactive element (an <a>), not a div
    // with an onClick — keyboard focus, middle-click and copy-link depend on
    // it being a real link.
    const anchor = card.locator("xpath=ancestor-or-self::a[1]");
    await expect(anchor).toHaveAttribute("href", new RegExp(`/orgs/test/incidents\\?.*checkUid=${CHECK_WITH_INCIDENT_UID}`));

    await card.click();

    await page.waitForURL(
      new RegExp(`/orgs/test/incidents\\?.*checkUid=${CHECK_WITH_INCIDENT_UID}`)
    );
    const url = new URL(page.url());
    expect(url.searchParams.get("checkUid")).toBe(CHECK_WITH_INCIDENT_UID);
    expect(url.searchParams.get("state")).toBe("all");

    await page.waitForLoadState("networkidle");

    // Rendered rows are scoped to this check's incident only.
    const rows = page.getByTestId("incident-row");
    await expect(rows).toHaveCount(1);
    await expect(rows.first()).toHaveAttribute("data-incident-uid", INCIDENT_UID);
  });

  test("a check with zero incidents renders a non-interactive card", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Create a fresh check via the UI — brand new, so it has zero incidents.
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
    await page.waitForURL(/\/checks/);
    await page.waitForLoadState("networkidle");
    await page.getByTestId("new-check-button").click();
    await page.waitForURL(/\/checks\/new/);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    const checkName = `E2E Zero Incidents ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page.getByTestId("check-url-input").fill("https://example.com/zero-incidents");
    await page.getByTestId("check-submit-button").click();

    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    const card = page.getByTestId("incidents-card");
    await expect(card).toBeVisible();
    await expect(card).toContainText("0");

    // Positive control: assert the card is NOT wrapped in an anchor, so a
    // regression that always links (even with zero incidents) can't pass.
    const closestAnchorTag = await card.evaluate(
      (el) => el.closest("a")?.tagName ?? null
    );
    expect(closestAnchorTag).toBeNull();
  });
});
