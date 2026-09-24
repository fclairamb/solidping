import { test, expect, type Page } from "@playwright/test";
import { API_BASE as BASE, STATUS_BASE } from "./fixtures";

/**
 * Verifies the public status page renders the neutral "No data, last checked
 * HH:MM" badge for a stale component — never "operational" (spec
 * 2026-09-25-02, `status-page-view.tsx`'s `status === "stale"` branch).
 *
 * A check only goes stale after `max(3 × period, 5 min)` of silence, measured
 * by a minute sweep — far too slow for an end-to-end run (mirrors dash0's
 * `checks-stale.spec.ts`). So, following this file's siblings
 * (`overall-status-badge.spec.ts`, `response-time-chart.spec.ts`), this mocks
 * the public status page API response directly instead of depending on a
 * check actually going stale.
 */
const ORG = "e2e-stale-status";
const SLUG = "stale-status";

function isoMinutesAgo(minutes: number): string {
  return new Date(Date.now() - minutes * 60_000).toISOString();
}

function basePayload() {
  return {
    uid: "66666666-6666-6666-6666-666666666666",
    name: "Stale Status Test",
    slug: SLUG,
    visibility: "public",
    isDefault: false,
    enabled: true,
    showAvailability: false,
    showResponseTime: false,
    historyDays: 0,
    historyPeriod: "24h",
    availabilityThresholds: { thresholdUp: 99.9, thresholdDegraded: 99 },
    overallStatus: "unknown",
  };
}

async function mockStatusPage(page: Page, lastResultAt: string) {
  await page.route(`**/api/v1/status-pages/${ORG}/${SLUG}`, (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        ...basePayload(),
        sections: [
          {
            uid: "77777777-7777-7777-7777-777777777777",
            name: "Core",
            slug: "core",
            position: 0,
            resources: [
              {
                uid: "88888888-8888-8888-8888-888888888888",
                checkUid: "99999999-9999-9999-9999-999999999999",
                publicName: "Silent Component",
                position: 0,
                check: {
                  name: "Silent Component",
                  type: "http",
                  status: "stale",
                  inMaintenance: false,
                  lastResultAt,
                },
              },
            ],
          },
        ],
      }),
    }),
  );
}

test.describe("Public status page — stale (No data) status badge", () => {
  test("a stale component reads 'No data, last checked HH:MM', never a normal status label", async ({
    page,
  }) => {
    // 2 minutes, not 20: this is a mocked API response (no real staleness
    // sweep to satisfy), and a small offset keeps `lastResultAt` on today's
    // calendar date even when the test runs in the 00:00-00:20 window, which
    // otherwise makes `formatLastChecked` prepend a date and desync this
    // regex (a wall-clock landmine this repo has hit before).
    const lastResultAt = isoMinutesAgo(2);
    await mockStatusPage(page, lastResultAt);

    await page.goto(`${BASE}${STATUS_BASE}/${ORG}/${SLUG}`);
    await page.waitForLoadState("networkidle");

    const row = page.getByTestId("resource-row").filter({ hasText: "Silent Component" });
    await expect(row).toHaveCount(1);

    const badge = row.getByTestId("resource-status-badge");
    await expect(badge).toBeVisible();
    await expect(badge).toHaveAttribute("data-status", "stale");

    // The exact HH:MM rendering (24h vs AM/PM) depends on the browser's
    // detected locale, so this pins the sentence shape — "No data, last
    // checked" followed by a clock time — rather than the literal digits.
    await expect(badge).toHaveText(/^No data, last checked \d{1,2}:\d{2}(\s?[AP]M)?$/i);

    // Never renders as an ordinary status label or the raw wire token.
    await expect(badge).not.toHaveText(/^stale$/i);
    await expect(badge).not.toContainText(/operational|up\b/i);
  });
});
