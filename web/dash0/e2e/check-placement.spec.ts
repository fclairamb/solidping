import { API_BASE, expect, getAuthToken, test, type Page } from "./fixtures";
import { choosePinnedRegions } from "./placement-helpers";

// Automatic region placement, spec 2026-09-25-06.
//
// The test server has a single cloud region ("default"), and the region picker
// only renders with two or more to offer. So the regions list is mocked with a
// second cloud region to exercise the form; everything written (the check,
// its placement, the bulk switch) goes through the live server, which only
// knows "default" and therefore places the check there.

async function mockTwoRegions(page: Page): Promise<void> {
  await page.route("**/api/v1/orgs/*/regions", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: [
          { slug: "default", emoji: "🇪🇺", name: "Default", status: "online" },
          { slug: "e2e-second", emoji: "🇫🇷", name: "E2E Second", status: "online" },
        ],
        defaultRegions: ["default"],
      }),
    }),
  );
}

async function getCheck(page: Page, token: string, uid: string): Promise<Record<string, unknown>> {
  const resp = await page.request.get(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  expect(resp.ok()).toBeTruthy();

  return (await resp.json()) as Record<string, unknown>;
}

test.describe("Region placement", () => {
  test("a new check is placed automatically, and 'Choose regions' switches to the pinned picker", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    await mockTwoRegions(page);

    await page.goto("orgs/test/checks/new?checkType=http");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    // The default: one line, no checkboxes.
    const picker = page.getByTestId("check-regions-picker");
    await expect(picker).toHaveAttribute("data-placement", "auto");
    await expect(page.getByTestId("check-placement-auto-summary")).toContainText("Automatic (2 regions)");
    await expect(page.locator("[data-testid^='region-option-']")).toHaveCount(0);

    // "Choose regions" opens the pinned picker, seeded with the org defaults…
    await choosePinnedRegions(page);
    await expect(page.getByTestId("region-option-default").getByRole("checkbox")).toBeChecked();
    // …and "Use automatic placement" goes back.
    await page.getByTestId("check-placement-use-auto").click();
    await expect(picker).toHaveAttribute("data-placement", "auto");

    const name = `E2E Auto Placement ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(name);
    await page.getByTestId("check-url-input").fill("https://example.com/auto-placement");
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });

    const uid = page.url().split("/checks/")[1].split(/[/?#]/)[0];
    const stored = await getCheck(page, token, uid);
    expect(stored.placement).toBe("auto");
    expect(stored.regions).toEqual(["default"]);

    // The detail page names the placement and the region it runs from.
    const placement = page.getByTestId("check-placement");
    await expect(placement).toHaveAttribute("data-placement", "auto");
    await expect(page.getByTestId("check-placement-summary")).toContainText("Automatic");
    await expect(page.getByTestId("check-placement-region")).toHaveCount(1);

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  });

  // TestUpdateToAutoRejectsPrivateRegion's frontend twin: a genuinely
  // single-region install (no mocking — the test server really only knows
  // "default") hides the region picker entirely (check-form.tsx's
  // `showRegions`). A new check must still come out `auto`, N=1 — not
  // `pinned` — so it is reassignable the moment a second region joins,
  // matching A3's "N = the number available" default.
  test("a new check on a genuinely single-region install is auto, not pinned", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    await page.goto("orgs/test/checks/new?checkType=http");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    // The picker never renders: there is nothing to choose between.
    await expect(page.getByTestId("check-regions-picker")).toHaveCount(0);

    const name = `E2E Single Region Auto ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(name);
    await page.getByTestId("check-url-input").fill("https://example.com/single-region-auto");
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });

    const uid = page.url().split("/checks/")[1].split(/[/?#]/)[0];
    const stored = await getCheck(page, token, uid);
    expect(stored.placement).toBe("auto");
    expect(stored.regionCount).toBe(1);
    expect(stored.regions).toEqual(["default"]);

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  });

  test("a check created with explicit regions stays pinned", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    await mockTwoRegions(page);

    await page.goto("orgs/test/checks/new?checkType=http");
    await expect(page.getByTestId("check-name-input")).toBeVisible();
    await choosePinnedRegions(page);

    const name = `E2E Pinned Placement ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(name);
    await page.getByTestId("check-url-input").fill("https://example.com/pinned-placement");
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });

    const uid = page.url().split("/checks/")[1].split(/[/?#]/)[0];
    const stored = await getCheck(page, token, uid);
    expect(stored.placement).toBe("pinned");
    expect(stored.regions).toEqual(["default"]);
    await expect(page.getByTestId("check-placement")).toHaveAttribute("data-placement", "pinned");

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  });

  test("the checks list switches pinned checks to automatic placement in bulk", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    const resp = await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
      headers: { Authorization: `Bearer ${token}` },
      data: {
        name: `E2E Bulk Placement ${Date.now()}`,
        type: "http",
        config: { url: "https://example.com/bulk-placement" },
        regions: ["default"],
      },
    });
    expect(resp.ok()).toBeTruthy();
    const created = (await resp.json()) as { uid: string; placement: string };
    expect(created.placement).toBe("pinned");

    await page.goto("orgs/test/checks");
    await page.getByTestId("auto-placement-button").click();
    await expect(page.getByTestId("auto-placement-dialog")).toBeVisible();
    await expect(page.getByTestId("auto-placement-description")).toContainText("automatic placement");
    await page.getByTestId("auto-placement-confirm").click();
    await expect(page.getByTestId("auto-placement-dialog")).toHaveCount(0);

    const switched = await getCheck(page, token, created.uid);
    expect(switched.placement).toBe("auto");
    expect(switched.regions).toEqual(["default"]);
    expect(switched.regionCount).toBe(1);

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${created.uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  });
});
