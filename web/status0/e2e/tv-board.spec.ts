import { test, expect, type Page } from "@playwright/test";
import { API_BASE as BASE, STATUS_BASE } from "./fixtures";

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

/**
 * One summary response — `GET …/{org}/{slug}/summary`, the endpoint the uptime
 * tile now reads (spec 2026-09-22-08).
 *
 * `availabilityPct === undefined` models a summary that OMITS the number, which
 * is what the server sends for a page with nothing to average. It is a distinct
 * case from a zero, and the board must render no tile for it.
 */
function summaryBody(availabilityPct: number | undefined) {
  return {
    status: "operational",
    counts: { operational: 1, degraded: 0, down: 0, maintenance: 0, unknown: 0 },
    ...(availabilityPct === undefined
      ? {}
      : { overallAvailabilityPct: availabilityPct }),
    page: {
      name: "Wallboard Test",
      slug: SLUG,
      url: `http://localhost/s/${ORG}/${SLUG}`,
    },
    generatedAt: "2026-08-30T12:00:00Z",
  };
}

interface MockOptions {
  /** Hold the page response back, to prove the board paints without it. */
  pageDelayMs?: number;
  /** Hold the FIRST summary response back, to prove the tile lands last. */
  summaryDelayMs?: number;
  /**
   * What each successive summary answers with; the last entry repeats. An
   * `undefined` entry is a summary that omits the number entirely.
   */
  summaryAvailability?: Array<number | undefined>;
  /** Fail the incident history instead of answering it. */
  incidentsError?: { status: number; code: string };
}

const sleep = (ms: number) =>
  new Promise<void>((resolve) => setTimeout(resolve, ms));

async function mock(
  page: Page,
  overrides: Record<string, unknown>,
  incidents: unknown[] = [],
  options: MockOptions = {},
) {
  // The real history endpoint returns the OPEN publications alongside the
  // resolved ones: `ListPublicIncidents` applies no state filter when
  // `activeOnly` is false, so `?active=true` is a narrowing of one query rather
  // than a second, different one. Folding a test's `activeIncidents` into the
  // history body therefore models the server, and keeps every test that
  // declares an active incident on the page payload meaningful now that the
  // board reads the open list off the history endpoint (spec 2026-09-22-08).
  const active = (overrides.activeIncidents as unknown[] | undefined) ?? [];
  const history = [...active, ...incidents];

  // TV mode requests `?include=` (spec 2026-09-22-07) so the pattern must
  // tolerate the query string, not just the bare path.
  await page.route(`**/api/v1/status-pages/${ORG}/${SLUG}*`, async (route) => {
    if (options.pageDelayMs) await sleep(options.pageDelayMs);

    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(payload(overrides)),
    });
  });

  await page.route(
    `**/api/v1/status-pages/${ORG}/${SLUG}/incidents*`,
    (route) => {
      const failure = options.incidentsError;

      return failure
        ? route.fulfill({
            status: failure.status,
            contentType: "application/json",
            body: JSON.stringify({
              title: "Incident history unavailable",
              code: failure.code,
            }),
          })
        : route.fulfill({
            status: 200,
            contentType: "application/json",
            body: JSON.stringify({ data: history }),
          });
    },
  );

  const availabilities = options.summaryAvailability ?? [undefined];
  let summaryCalls = 0;

  await page.route(
    `**/api/v1/status-pages/${ORG}/${SLUG}/summary*`,
    async (route) => {
      const index = Math.min(summaryCalls, availabilities.length - 1);
      summaryCalls += 1;

      if (index === 0 && options.summaryDelayMs) {
        await sleep(options.summaryDelayMs);
      }

      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(summaryBody(availabilities[index])),
      });
    },
  );
}

/** Collects the URLs of every public status-page request the board makes. */
function recordRequests(page: Page): string[] {
  const seen: string[] = [];

  page.on("request", (request) => {
    if (request.url().includes(`/api/v1/status-pages/${ORG}/`)) {
      seen.push(request.url());
    }
  });

  return seen;
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

    await page.goto(`${BASE}${STATUS_BASE}/${ORG}/${SLUG}/tv`);
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

    await page.goto(`${BASE}${STATUS_BASE}/${ORG}/${SLUG}/tv`);
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

    await page.goto(`${BASE}${STATUS_BASE}/${ORG}/${SLUG}/tv`);
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

    await page.goto(`${BASE}${STATUS_BASE}/${ORG}/${SLUG}/tv`);
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

    await page.goto(`${BASE}${STATUS_BASE}/${ORG}/${SLUG}/tv`);

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

    await page.goto(`${BASE}${STATUS_BASE}/${ORG}/${SLUG}/tv`);

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

    await page.goto(`${BASE}${STATUS_BASE}/${ORG}/${SLUG}/tv`);
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

    await page.goto(`${BASE}${STATUS_BASE}/${ORG}/${SLUG}/tv`);

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

    await page.goto(`${BASE}${STATUS_BASE}/${ORG}/${SLUG}/tv`);

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

    await page.goto(`${BASE}${STATUS_BASE}/${ORG}/${SLUG}/tv`);

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

    await page.goto(`${BASE}${STATUS_BASE}/${ORG}/${SLUG}/tv`);

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

    await page.goto(`${BASE}${STATUS_BASE}/${ORG}/${SLUG}/tv`);

    await expect(page.getByTestId("tv-headline")).toBeVisible();
    await expect(page.getByTestId("tv-headline-cause")).toHaveCount(0);
  });
});

/**
 * The load order (spec 2026-09-22-08).
 *
 * The board used to show `loading` until the whole page payload arrived — 3.2 MB
 * and about 7 seconds on a 200-resource page — and then render everything at
 * once, for the sake of one number: the page-level uptime mean. These tests pin
 * the three reads into the order the room actually needs them, and pin each
 * intermediate state as a state the board is allowed to be in.
 */
test.describe("TV mode — what loads when", () => {
  const ACTIVE_CRITICAL = {
    uid: "inc-1",
    title: "Payments are down",
    state: "investigating",
    severity: "critical",
    startedAt: "2026-08-30T10:00:00Z",
  };

  test("the page request asks for neither optional section", async ({ page }) => {
    const requests = recordRequests(page);
    await mock(page, { overallStatus: "operational" }, RESOLVED_LONG_AGO);

    await page.goto(`${BASE}${STATUS_BASE}/${ORG}/${SLUG}/tv`);
    await expect(page.getByTestId("tv-board")).toHaveAttribute(
      "data-tv-state",
      "operational",
    );

    const pageRequests = requests
      .map((url) => new URL(url))
      .filter((url) => url.pathname === `/api/v1/status-pages/${ORG}/${SLUG}`);

    expect(pageRequests.length).toBeGreaterThan(0);
    // `include=` with no value: neither the availability series nor the
    // response-time series, which is the whole 3 MB.
    expect(pageRequests.map((url) => url.search)).toContain("?include=");

    // A page body with no availability anywhere renders the board and no tile.
    await expect(page.getByTestId("tv-days-since")).toBeVisible();
    await expect(page.getByTestId("tv-availability")).toHaveCount(0);
  });

  // The headline of this spec: the room sees the incident about seven seconds
  // before the rest of the page exists.
  test("the board paints from the incidents while the page payload is still in flight", async ({
    page,
  }) => {
    await mock(
      page,
      { overallStatus: "operational", activeIncidents: [ACTIVE_CRITICAL] },
      RESOLVED_LONG_AGO,
      { pageDelayMs: 3000 },
    );

    await page.goto(`${BASE}${STATUS_BASE}/${ORG}/${SLUG}/tv`);

    const title = page.getByTestId("tv-active-incident-title");
    await expect(title).toHaveText("Payments are down", { timeout: 2000 });
    // The severity floor alone, with no rollup in hand at all.
    await expect(page.getByTestId("tv-board")).toHaveAttribute(
      "data-tv-state",
      "down",
    );
    // The proof that the page really has NOT landed yet: its name is not on
    // screen. Anything sourced from the page payload is still an empty slot.
    await expect(page.getByTestId("tv-page-name")).not.toContainText(
      "Wallboard Test",
    );
    // And no attribution line: that sentence compares the incident against the
    // server's rollup, and there is no rollup to compare against.
    await expect(page.getByTestId("tv-headline-cause")).toHaveCount(0);

    // Now the page lands and fills the rest in — around the SAME element, not a
    // fresh one. A remount here would flash the whole wall.
    const handle = await title.elementHandle();
    await expect(page.getByTestId("tv-page-name")).toHaveText("Wallboard Test", {
      timeout: 8000,
    });
    expect(await handle!.evaluate((node) => node.isConnected)).toBe(true);
    await expect(title).toHaveText("Payments are down");
  });

  test("the uptime tile lands after the board, and leaves when the summary drops the number", async ({
    page,
  }) => {
    await page.clock.install({
      time: new Date("2026-08-30T12:00:00Z").getTime(),
    });
    const requests = recordRequests(page);
    await mock(
      page,
      {
        overallStatus: "operational",
        showAvailability: true,
        historyPeriod: "7d",
      },
      RESOLVED_LONG_AGO,
      { summaryDelayMs: 3000, summaryAvailability: [99.95, undefined] },
    );

    await page.goto(`${BASE}${STATUS_BASE}/${ORG}/${SLUG}/tv`);

    const tile = page.getByTestId("tv-availability");

    // The whole board, complete except for the number.
    await expect(page.getByTestId("tv-page-name")).toHaveText("Wallboard Test");
    await expect(page.getByTestId("tv-days-since")).toBeVisible();
    await expect(tile).toHaveCount(0);

    // Then the number, from the summary — with the window label taken from the
    // PAGE, which is the only one of the two that knows the history period.
    await expect(tile).toContainText("99.95%", { timeout: 8000 });
    await expect(tile).toContainText("7-day uptime");

    // Five minutes on, the summary has no mean to report. The tile goes; the
    // board does not.
    await page.clock.fastForward("05:30");
    await expect(tile).toHaveCount(0, { timeout: 10000 });
    // The tile went because a SECOND summary answered without a number, not
    // because the board gave up: a stale board hides the tile too, and that
    // would make this assertion pass for the wrong reason.
    expect(
      requests.filter((url) => url.includes("/summary")).length,
    ).toBeGreaterThanOrEqual(2);
    await expect(page.getByTestId("tv-board")).toHaveAttribute(
      "data-tv-state",
      "operational",
    );
    await expect(page.getByTestId("tv-stale-notice")).toHaveCount(0);
  });

  test("no summary is requested for a page that hides availability", async ({
    page,
  }) => {
    const requests = recordRequests(page);
    await mock(
      page,
      { overallStatus: "operational", showAvailability: false },
      RESOLVED_LONG_AGO,
      // Deliberately ANSWERABLE with a number: if the board asked, it would get
      // one, so the absent tile below can only mean it never asked.
      { summaryAvailability: [99.99] },
    );

    await page.goto(`${BASE}${STATUS_BASE}/${ORG}/${SLUG}/tv`);
    await expect(page.getByTestId("tv-days-since")).toBeVisible();
    // Long enough for a request the board was going to make to have been made.
    await page.waitForTimeout(1500);

    expect(requests.filter((url) => url.includes("/summary"))).toEqual([]);
    await expect(page.getByTestId("tv-availability")).toHaveCount(0);
  });

  // The positive control for the test above: the same assertions, on a page
  // that DOES publish availability, must find the request and the tile.
  test("the summary IS requested for a page that publishes availability", async ({
    page,
  }) => {
    const requests = recordRequests(page);
    await mock(
      page,
      {
        overallStatus: "operational",
        showAvailability: true,
        historyPeriod: "30d",
      },
      RESOLVED_LONG_AGO,
      { summaryAvailability: [99.99] },
    );

    await page.goto(`${BASE}${STATUS_BASE}/${ORG}/${SLUG}/tv`);

    const tile = page.getByTestId("tv-availability");
    await expect(tile).toContainText("99.99%");
    await expect(tile).toContainText("30-day uptime");
    expect(requests.filter((url) => url.includes("/summary")).length).toBe(1);
  });

  // The two reads are behind ONE visibility gate on the server, so whichever of
  // them says "locked" is already the truth about the whole screen.
  test("a 401 on the incident history locks the whole screen", async ({
    page,
  }) => {
    await mock(page, { overallStatus: "operational" }, RESOLVED_LONG_AGO, {
      incidentsError: { status: 401, code: "STATUS_PAGE_LOCKED" },
    });

    await page.goto(`${BASE}${STATUS_BASE}/${ORG}/${SLUG}/tv`);

    await expect(page.getByTestId("tv-locked")).toBeVisible();
    await expect(page.getByTestId("tv-board")).toHaveCount(0);
  });

  test("a 404 on the incident history takes over the screen too", async ({
    page,
  }) => {
    await mock(page, { overallStatus: "operational" }, RESOLVED_LONG_AGO, {
      incidentsError: { status: 404, code: "STATUS_PAGE_NOT_FOUND" },
    });

    await page.goto(`${BASE}${STATUS_BASE}/${ORG}/${SLUG}/tv`);

    await expect(page.getByTestId("tv-not-found")).toBeVisible();
    await expect(page.getByTestId("tv-board")).toHaveCount(0);
  });

  // A 5xx is NOT a statement about the page, so it must not replace a board
  // that is otherwise rendering correctly.
  test("a 500 on the incident history leaves the board up", async ({ page }) => {
    await mock(page, { overallStatus: "operational" }, RESOLVED_LONG_AGO, {
      incidentsError: { status: 500, code: "INTERNAL_ERROR" },
    });

    await page.goto(`${BASE}${STATUS_BASE}/${ORG}/${SLUG}/tv`);

    await expect(page.getByTestId("tv-board")).toHaveAttribute(
      "data-tv-state",
      "operational",
    );
    await expect(page.getByTestId("tv-page-name")).toHaveText("Wallboard Test");
    await expect(page.getByTestId("tv-not-found")).toHaveCount(0);
  });
});
