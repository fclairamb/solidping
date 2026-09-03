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

  // "Checkout API — OUTAGE" says what broke, not whether it broke a minute ago
  // or overnight. The clock is frozen so the elapsed value is exact.
  test("names how long the outage has been going on", async ({ page }) => {
    await page.clock.install({
      time: new Date("2026-08-30T12:00:00Z").getTime(),
    });
    await mock(page, {
      overallStatus: "down",
      sections: section([
        resource("r1", "Checkout API", "down", {
          statusChangedAt: "2026-08-30T09:15:00Z",
        }),
      ]),
    });

    await page.goto(`${BASE}/status0/${ORG}/${SLUG}/tv`);

    const row = page.getByTestId("tv-failing-resource");
    await expect(row).toContainText("Checkout API");
    // Section and elapsed time on one line, 2h 45m after it went down.
    await expect(row).toContainText("Core · for 2h 45m");
  });

  // A group resource has no statusChangedAt: the row must still render.
  test("a resource with no timestamp still renders, just without a duration", async ({
    page,
  }) => {
    await mock(page, {
      overallStatus: "down",
      sections: section([resource("r1", "Aggregated group", "down")]),
    });

    await page.goto(`${BASE}/status0/${ORG}/${SLUG}/tv`);

    const row = page.getByTestId("tv-failing-resource");
    await expect(row).toContainText("Aggregated group");
    await expect(row).toContainText("Core");
    await expect(row).not.toContainText("for ");
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

/**
 * The resolved strip must say WHEN, not just how long.
 *
 * "resolved in 8m" alone tells the room how bad it was but not whether it
 * happened over lunch or last Tuesday — and a wallboard is read by someone who
 * was not watching when it happened.
 *
 * The clock is frozen so the relative "ago" is exact rather than drifting a
 * minute mid-assertion.
 */
test.describe("TV mode — the recently-resolved strip", () => {
  const NOW = new Date("2026-08-30T12:00:00Z").getTime();

  test("names when it started and how long it lasted", async ({ page }) => {
    await page.clock.install({ time: NOW });
    await mock(page, { overallStatus: "operational" }, [
      {
        uid: "r1",
        title: "1.1.1.1 is experiencing issues",
        state: "resolved",
        // Started 3h before the frozen now, resolved 8 minutes later.
        startedAt: "2026-08-30T09:00:00Z",
        resolvedAt: "2026-08-30T09:08:00Z",
      },
    ]);

    await page.goto(`${BASE}/status0/${ORG}/${SLUG}/tv`);

    const card = page.getByTestId("tv-resolved-incident");
    await expect(card).toHaveCount(1);
    // Both facts, in one line: when it began and how long it ran.
    await expect(card).toContainText("3h 0m ago");
    await expect(card).toContainText("lasted 8m");
  });

  test("a multi-day-old incident still reads in days", async ({ page }) => {
    await page.clock.install({ time: NOW });
    await mock(page, { overallStatus: "operational" }, [
      {
        uid: "r1",
        title: "Old outage",
        state: "resolved",
        startedAt: "2026-08-28T10:00:00Z",
        resolvedAt: "2026-08-28T12:30:00Z",
      },
    ]);

    await page.goto(`${BASE}/status0/${ORG}/${SLUG}/tv`);

    const card = page.getByTestId("tv-resolved-incident");
    await expect(card).toContainText("2d 2h ago");
    await expect(card).toContainText("lasted 2h 30m");
  });
});

/**
 * The headline's cause line (spec 2026-09-02-05).
 *
 * A board that reads "Some Systems Degraded" while every check is up is
 * correct — an operator published something the probes cannot see — but with
 * nothing saying so, the room reads it as "the monitoring is broken". The
 * reported case sat like that for ten days.
 */
test.describe("TV mode — attributing the amber", () => {
  test("names the open incident when the checks are all passing", async ({
    page,
  }) => {
    await mock(page, {
      overallStatus: "operational",
      activeIncidents: [
        {
          uid: "inc-1",
          title: "Some services are experiencing issues",
          state: "identified",
          severity: "minor",
          startedAt: "2026-08-30T10:00:00Z",
        },
      ],
      sections: section([resource("r1", "Checkout API", "up")]),
    });

    await page.goto(`${BASE}/status0/${ORG}/${SLUG}/tv`);

    await expect(page.getByTestId("tv-headline")).toContainText("Degraded");
    await expect(page.getByTestId("tv-headline-cause")).toContainText(
      "1 open incident",
    );
  });

  // An ALREADY-impaired rollup pushed further by a critical publication: the
  // attribution is still true, but "all monitored services are passing" would
  // not be — a wallboard must never assert that with a degraded rollup behind
  // it.
  test("drops the all-passing clause when the rollup is already impaired", async ({
    page,
  }) => {
    await mock(page, {
      overallStatus: "degraded",
      activeIncidents: [
        {
          uid: "inc-1",
          title: "Payments degraded",
          state: "investigating",
          severity: "critical",
          startedAt: "2026-08-30T10:00:00Z",
        },
      ],
      sections: section([resource("r1", "Checkout API", "degraded")]),
    });

    await page.goto(`${BASE}/status0/${ORG}/${SLUG}/tv`);

    const cause = page.getByTestId("tv-headline-cause");
    await expect(cause).toContainText("1 open incident");
    await expect(cause).not.toContainText("all monitored services are passing");
  });

  // The negative control: same amber board, but the checks account for it.
  // Attributing THAT to the publication would be a lie in the other direction.
  test("stays silent when the rollup itself is not green", async ({ page }) => {
    await mock(page, {
      overallStatus: "down",
      activeIncidents: [
        {
          uid: "inc-1",
          title: "Payments degraded",
          state: "investigating",
          severity: "minor",
          startedAt: "2026-08-30T10:00:00Z",
        },
      ],
      sections: section([resource("r1", "Checkout API", "down")]),
    });

    await page.goto(`${BASE}/status0/${ORG}/${SLUG}/tv`);

    await expect(page.getByTestId("tv-headline")).toBeVisible();
    await expect(page.getByTestId("tv-headline-cause")).toHaveCount(0);
  });
});
