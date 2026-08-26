import { test, expect, type Page } from "./fixtures";

// Coverage for spec 2026-08-26-01: regions advertise a three-state browser
// capability (spec 2026-08-19-03), and the region picker in check-form.tsx
// surfaces it as a single icon next to the IPv6 badge — never to hide,
// disable or reject a region.
//
// The region list is mocked so all three states are present deterministically
// — "unknown" only appears in a real deployment when a worker is stale or
// old, not something an E2E run can arrange.

const REGIONS = [
  { slug: "no-browser", emoji: "🇩🇪", name: "No Browser Region", capabilities: { browser: "no" } },
  { slug: "silent", emoji: "🇯🇵", name: "Silent Region" }, // no capabilities map at all
  { slug: "has-browser", emoji: "🇫🇷", name: "Has Browser Region", capabilities: { browser: "yes" } },
];

async function mockRegions(page: Page): Promise<void> {
  await page.route("**/api/v1/orgs/*/regions", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: REGIONS, defaultRegions: ["no-browser"] }),
    }),
  );
}

test.describe("region picker browser capability", () => {
  test("marks all three states for a browser check, unknown never rendered as no", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await mockRegions(page);

    await page.goto("orgs/test/checks/new?checkType=browser");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    // A capable region is marked; a region that reports "no" is marked as
    // such; a region that reports nothing is NOT marked "no" — that
    // conflation is the exact lie this spec exists to stop telling.
    await expect(page.getByTestId("region-browser-has-browser")).toHaveAttribute(
      "data-browser",
      "yes",
    );
    await expect(page.getByTestId("region-browser-no-browser")).toHaveAttribute(
      "data-browser",
      "no",
    );
    await expect(page.getByTestId("region-option-silent")).toHaveAttribute(
      "data-browser",
      "unknown",
    );
    // For a browser check, unknown is meaningful — it renders, not hidden.
    await expect(page.getByTestId("region-browser-silent")).toBeVisible();

    // Nothing is hidden and nothing is disabled — the advertised value is a
    // hint, never a gate. A region advertising "no" must still be selectable,
    // because the run-time worker is the authority and may have gained a
    // browser since its last heartbeat.
    expect(await page.locator("[data-testid^='region-option-']").count()).toBe(3);

    const noBrowserCheckbox = page.getByTestId("region-option-no-browser").getByRole("checkbox");
    await expect(noBrowserCheckbox).toBeEnabled();
    await noBrowserCheckbox.click();
    await expect(noBrowserCheckbox).toHaveAttribute("data-state", "checked");
  });

  test("stays quiet on unknown for a non-browser check, still shows a definite no", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await mockRegions(page);

    await page.goto("orgs/test/checks/new?checkType=tcp");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    // A definite "no" still renders even for a check type that doesn't care
    // about browser capability — "no" always renders, per the model this
    // mirrors (ipv6-capability's hideUnknown contract).
    await expect(page.getByTestId("region-browser-no-browser")).toHaveAttribute(
      "data-browser",
      "no",
    );
    // "unknown" stays quiet here — a wall of neutral icons would be noise on
    // a check type this capability is irrelevant to.
    await expect(page.getByTestId("region-browser-silent")).toHaveCount(0);
  });
});
