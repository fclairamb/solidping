import { test, expect, type Page } from "@playwright/test";
import { API_BASE as BASE } from "./fixtures";

/**
 * Verifies the hero "overall-status-badge" renders the exact label for each
 * of the 5 server-computed overallStatus values (spec 2026-08-08-05):
 * operational | degraded | down | maintenance | unknown.
 *
 * Unlike maintenance-badge.spec.ts (which depends on live seed data and
 * skips when absent), this mocks the public status page API response
 * directly so every state — including maintenance and unknown, which the
 * stock dev seed never produces — is deterministically exercised.
 */
const ORG = "e2e-overall-status";
const SLUG = "overall-status";

function basePayload() {
  return {
    uid: "11111111-1111-1111-1111-111111111111",
    name: "Overall Status Test",
    slug: SLUG,
    visibility: "public",
    isDefault: false,
    enabled: true,
    showAvailability: false,
    showResponseTime: false,
    historyDays: 0,
    historyPeriod: "24h",
    sections: [],
    availabilityThresholds: { thresholdUp: 99.9, thresholdDegraded: 99 },
  };
}

async function mockOverallStatus(page: Page, overallStatus: string) {
  await page.route(`**/api/v1/status-pages/${ORG}/${SLUG}`, (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ ...basePayload(), overallStatus }),
    }),
  );
}

test.describe("Public status page — overall status badge", () => {
  const cases: Array<{ overallStatus: string; expectedText: RegExp }> = [
    { overallStatus: "operational", expectedText: /All Systems Operational/i },
    { overallStatus: "degraded", expectedText: /Some Systems Degraded/i },
    { overallStatus: "down", expectedText: /System Outage/i },
    { overallStatus: "maintenance", expectedText: /Under Maintenance/i },
    { overallStatus: "unknown", expectedText: /Status Unknown/i },
  ];

  for (const { overallStatus, expectedText } of cases) {
    test(`overallStatus="${overallStatus}" renders the matching hero label`, async ({
      page,
    }) => {
      await mockOverallStatus(page, overallStatus);
      await page.goto(`${BASE}/status0/${ORG}/${SLUG}`);
      await page.waitForLoadState("networkidle");

      const badge = page.getByTestId("overall-status-badge");
      await expect(badge).toBeVisible();
      await expect(badge).toHaveText(expectedText);
    });
  }

  test("missing overallStatus (older/cached response) falls back to Status Unknown, not a crash", async ({
    page,
  }) => {
    await page.route(`**/api/v1/status-pages/${ORG}/${SLUG}`, (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(basePayload()),
      }),
    );
    await page.goto(`${BASE}/status0/${ORG}/${SLUG}`);
    await page.waitForLoadState("networkidle");

    const badge = page.getByTestId("overall-status-badge");
    await expect(badge).toBeVisible();
    await expect(badge).toHaveText(/Status Unknown/i);
  });
});
