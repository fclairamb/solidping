import { test, expect, mockSloCoverage, API_BASE, getAuthToken, type Page } from "./fixtures";

// Spec 2026-09-25-04: a passive check (heartbeat, email) has no regions. It
// makes no outbound request and is evaluated by SolidPing itself on the jobs
// node, so a region only ever gave it a place to go silent (a dark region) or
// to fail (an agent). The form hides the region picker for these types, and
// the API accepts and drops a region list sent for one.

const REGIONS = [
  { slug: "e2e-eu", emoji: "🇪🇺", name: "E2E EU", status: "online" },
  { slug: "e2e-us", emoji: "🇺🇸", name: "E2E US", status: "online" },
];

// Two regions, so the picker has a choice to offer at all: with one region it
// is hidden for every type, which would make the passive assertions vacuous.
async function mockTwoRegions(page: Page): Promise<void> {
  await page.route("**/api/v1/orgs/*/regions", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: REGIONS, defaultRegions: ["e2e-eu"] }),
    }),
  );
}

test.describe("passive checks have no regions", () => {
  test.beforeEach(async ({ authenticatedPage }) => {
    await mockSloCoverage(authenticatedPage);
    await mockTwoRegions(authenticatedPage);
  });

  test("the check form hides the region picker for heartbeat and email only", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Positive control: an active type shows the picker.
    await page.goto("orgs/test/checks/new?checkType=tcp");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-regions-picker")).toBeVisible();

    for (const passiveType of ["heartbeat", "email"]) {
      await page.goto(`orgs/test/checks/new?checkType=${passiveType}`);
      await page.waitForLoadState("networkidle");
      await expect(page.getByTestId("check-name-input")).toBeVisible();
      await expect(page.getByTestId("check-regions-picker")).toHaveCount(0);
    }
  });

  test("the API drops a region list sent for a heartbeat", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    const created = await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
      headers: { Authorization: `Bearer ${token}` },
      data: {
        name: `E2E Passive Regions ${Date.now()}`,
        type: "heartbeat",
        config: { token: `e2e-passive-${Date.now()}` },
        regions: ["gravelines"],
      },
    });
    expect(created.status(), "an explicit list is accepted, not rejected").toBe(201);

    const body = await created.json();
    expect(body.regions ?? []).toEqual([]);

    const patched = await page.request.patch(`${API_BASE}/api/v1/orgs/test/checks/${body.uid}`, {
      headers: { Authorization: `Bearer ${token}` },
      data: { regions: ["gravelines", "roubaix"] },
    });
    expect(patched.status()).toBe(200);
    expect((await patched.json()).regions ?? []).toEqual([]);

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${body.uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  });
});
