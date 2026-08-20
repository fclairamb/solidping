import { test, expect, mockSloCoverage } from "./fixtures";
import type { Page } from "@playwright/test";

// Duty-cycle warning on the check detail page (spec 2026-07-01-04 D3): when
// the server-reported scheduling block says the check occupies >= 50% of a
// runner slot, a warning alert nudges the user toward a longer period; fast
// checks never see it. The scheduling block is injected via route
// interception so the test is deterministic (a real cost EWMA would need
// many timed runs to converge).

/**
 * Rewrites the check DETAIL response (GET /checks/{uid}) to carry the given
 * scheduling block; null removes it (a check that never ran). List and
 * sub-resource endpoints are untouched — the glob's `*` does not span `/`.
 */
async function mockScheduling(
  page: Page,
  scheduling: {
    costEwmaMs: number;
    delayEwmaMs: number;
    dutyCyclePct: number;
  } | null,
) {
  // The check-detail header's SLO coverage chip fires an unconditional
  // GET /slos?checkUid=… (spec 2026-08-20-01). Stub it so the networkidle
  // wait in the tests below has nothing real left to settle.
  await mockSloCoverage(page);

  await page.route("**/api/v1/orgs/*/checks/*", async (route) => {
    if (route.request().method() !== "GET") {
      await route.fallback();
      return;
    }
    const response = await route.fetch();
    const body = await response.json();
    if (scheduling) {
      body.scheduling = scheduling;
    } else {
      delete body.scheduling;
    }
    await route.fulfill({ response, body: JSON.stringify(body) });
  });
}

async function createCheckAndOpen(page: Page, name: string) {
  await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
  await page.waitForURL(/\/checks/);
  await page.waitForLoadState("networkidle");
  await page.getByTestId("new-check-button").click();
  await page.waitForURL(/\/checks\/new/);
  await page.waitForLoadState("networkidle");

  await page.getByTestId("check-name-input").fill(name);
  await page
    .getByTestId("check-url-input")
    .fill("https://example.com/duty-cycle-test");
  await page.getByTestId("check-submit-button").click();

  await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
  await page.waitForLoadState("networkidle");
}

test.describe("Check detail duty-cycle warning", () => {
  test("shows the warning for a high-duty check, also on mobile", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // A browser-style slot hog: ~10s cost on a 10s period → 100% duty.
    await mockScheduling(page, {
      costEwmaMs: 10036,
      delayEwmaMs: 13432,
      dutyCyclePct: 100,
    });

    await createCheckAndOpen(page, `E2E Duty High ${Date.now()}`);

    const warning = page.getByTestId("duty-cycle-warning");
    await expect(warning).toBeVisible({ timeout: 10000 });
    await expect(warning).toContainText("100%");
    await expect(warning).toContainText("Increase its period");

    // Mobile viewport: the alert stays visible and inside the viewport
    // (block-level layout, no fixed widths).
    await page.setViewportSize({ width: 375, height: 812 });
    await expect(warning).toBeVisible();
    const box = await warning.boundingBox();
    expect(box).not.toBeNull();
    expect(box!.width).toBeLessThanOrEqual(375);

    // All four locales render a translated warning (language detector reads
    // localStorage first; a distinctive fragment pins each translation).
    const localizedFragments: Record<string, string> = {
      fr: "créneau de supervision",
      de: "Monitoring-Slot",
      es: "slot de monitorización",
      en: "monitoring slot",
    };
    for (const [lng, fragment] of Object.entries(localizedFragments)) {
      await page.evaluate(
        (language) => localStorage.setItem("solidping_language", language),
        lng,
      );
      await page.reload();
      await page.waitForLoadState("networkidle");
      const localized = page.getByTestId("duty-cycle-warning");
      await expect(localized).toBeVisible({ timeout: 10000 });
      await expect(localized).toContainText(fragment);
      await expect(localized).toContainText("100");
    }
  });

  test("hides the warning for a fast check and for a never-run check", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Fast check: 200ms cost on a 60s period → 0% duty. Block present but
    // far below the 50% threshold.
    await mockScheduling(page, {
      costEwmaMs: 200,
      delayEwmaMs: 50,
      dutyCyclePct: 0,
    });

    await createCheckAndOpen(page, `E2E Duty Low ${Date.now()}`);

    await expect(page.getByTestId("check-detail-header")).toBeVisible();
    await expect(page.getByTestId("duty-cycle-warning")).toHaveCount(0);

    // Never-run check: no scheduling block at all → no warning either.
    await mockScheduling(page, null);
    await page.reload();
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-detail-header")).toBeVisible();
    await expect(page.getByTestId("duty-cycle-warning")).toHaveCount(0);
  });
});
