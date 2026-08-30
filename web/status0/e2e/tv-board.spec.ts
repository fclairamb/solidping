import { test, expect, type Page } from "@playwright/test";
import { API_BASE as BASE } from "./fixtures";

/**
 * TV mode's middle panel (spec 2026-08-29-08).
 *
 * The board's colour and its explanation come from two sources that move at
 * different speeds: `overallStatus` is recomputed from live check data on every
 * poll, while `activeIncidents` are publications that only appear after the
 * page's `autoPublishDelaySeconds` — and never at all when auto-publish is off.
 * These tests pin the behaviour in that gap, which used to render a full red
 * screen whose only text was "N days since the last incident".
 *
 * Mocked rather than seeded: an outage window is not something the dev seed
 * produces on demand, and the whole point is a state that lasts 60 seconds.
 */
const ORG = "e2e-tv";
const SLUG = "wallboard";

function payload(overrides: Record<string, unknown> = {}) {
  return {
    uid: "22222222-2222-2222-2222-222222222222",
    name: "Wallboard Test",
    slug: SLUG,
    visibility: "public",
    isDefault: false,
    enabled: true,
    showAvailability: false,
    showResponseTime: false,
    historyDays: 90,
    historyPeriod: "90d",
    overallStatus: "operational",
    activeIncidents: [],
    sections: [],
    availabilityThresholds: { thresholdUp: 99.9, thresholdDegraded: 99 },
    ...overrides,
  };
}

function section(resources: Array<Record<string, unknown>>) {
  return [{ uid: "sec-1", name: "Core", slug: "core", position: 0, resources }];
}

function resource(
  uid: string,
  publicName: string,
  status: string,
  extra: Record<string, unknown> = {},
) {
  return {
    uid,
    position: 0,
    publicName,
    check: { type: "http", status, ...extra },
  };
}

async function mock(
  page: Page,
  overrides: Record<string, unknown>,
  incidents: unknown[] = [],
) {
  await page.route(`**/api/v1/status-pages/${ORG}/${SLUG}`, (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(payload(overrides)),
    }),
  );
  await page.route(
    `**/api/v1/status-pages/${ORG}/${SLUG}/incidents*`,
    (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ data: incidents }),
      }),
  );
}

const RESOLVED_LONG_AGO = [
  {
    uid: "old",
    title: "Old incident",
    state: "resolved",
    startedAt: "2026-01-01T00:00:00Z",
    resolvedAt: "2026-01-01T01:00:00Z",
  },
];

test.describe("TV mode — explaining a non-green board", () => {
  // The reported bug, verbatim: the screen goes red before anything says which
  // check failed.
  test("a red board with no publication yet names the failing check", async ({
    page,
  }) => {
    await mock(
      page,
      {
        overallStatus: "down",
        activeIncidents: [],
        sections: section([
          resource("r1", "Checkout API", "down"),
          resource("r2", "Marketing site", "up"),
        ]),
      },
      RESOLVED_LONG_AGO,
    );

    await page.goto(`${BASE}/status0/${ORG}/${SLUG}/tv`);
    await expect(page.getByTestId("tv-board")).toHaveAttribute(
      "data-tv-state",
      "down",
    );

    const failing = page.getByTestId("tv-failing-resource");
    await expect(failing).toHaveCount(1);
    await expect(failing.getByTestId("tv-failing-resource-name")).toHaveText(
      "Checkout API",
    );

    // The contradiction the bug produced must be gone.
    await expect(page.getByTestId("tv-days-since")).toHaveCount(0);
  });

  test("a healthy board is untouched — still the days-since panel", async ({
    page,
  }) => {
    await mock(
      page,
      {
        overallStatus: "operational",
        sections: section([resource("r1", "Checkout API", "up")]),
      },
      RESOLVED_LONG_AGO,
    );

    await page.goto(`${BASE}/status0/${ORG}/${SLUG}/tv`);
    await expect(page.getByTestId("tv-board")).toHaveAttribute(
      "data-tv-state",
      "operational",
    );
    await expect(page.getByTestId("tv-days-since")).toBeVisible();
    await expect(page.getByTestId("tv-failing-resources")).toHaveCount(0);
  });

  // A published incident is operator-authored and outranks a list of check
  // names, so it keeps the panel once it exists.
  test("an active publication still wins the panel", async ({ page }) => {
    await mock(
      page,
      {
        overallStatus: "down",
        activeIncidents: [
          {
            uid: "inc-1",
            title: "Payments degraded",
            state: "investigating",
            severity: "critical",
            startedAt: "2026-08-30T10:00:00Z",
          },
        ],
        sections: section([resource("r1", "Checkout API", "down")]),
      },
      RESOLVED_LONG_AGO,
    );

    await page.goto(`${BASE}/status0/${ORG}/${SLUG}/tv`);
    await expect(page.getByTestId("tv-active-incident")).toHaveCount(1);
    await expect(page.getByTestId("tv-failing-resources")).toHaveCount(0);
  });

  // Down on purpose is not down.
  test("a check in a maintenance window is not named as failing", async ({
    page,
  }) => {
    await mock(
      page,
      {
        overallStatus: "maintenance",
        sections: section([
          resource("r1", "Database", "down", { inMaintenance: true }),
        ]),
      },
      RESOLVED_LONG_AGO,
    );

    await page.goto(`${BASE}/status0/${ORG}/${SLUG}/tv`);
    await expect(page.getByTestId("tv-board")).toHaveAttribute(
      "data-tv-state",
      "maintenance",
    );
    await expect(page.getByTestId("tv-failing-resources")).toHaveCount(0);
  });

  test("worst first when several checks are failing", async ({ page }) => {
    await mock(
      page,
      {
        overallStatus: "down",
        sections: section([
          resource("r1", "Slow service", "degraded"),
          resource("r2", "Dead service", "down"),
        ]),
      },
      RESOLVED_LONG_AGO,
    );

    await page.goto(`${BASE}/status0/${ORG}/${SLUG}/tv`);
    await expect(
      page.getByTestId("tv-failing-resource-name"),
    ).toHaveText(["Dead service", "Slow service"]);
  });
});
