import { test, expect, mockSloCoverage, type Page } from "./fixtures";

// Coverage for spec 2026-09-22-03: degraded detection's three dashboard
// surfaces — the dry-run banner on the check page (which IS the rollout: the
// feature ships off for every pre-existing check), the `wouldHaveFired` filter
// on the checks list, and the amber band the chart draws over a degraded episode.
//
// Mocked at the API boundary like checks-index-status-type-filters.spec.ts: a
// real degraded episode needs 60 probes and a minute-resolution evaluator sweep,
// which is not something an e2e run can produce deterministically. What is under
// test here is the UI's reading of the API's answer.

interface MockCheck {
  uid: string;
  name: string;
  degradedEnabled: boolean;
  degradedWouldFireAt?: string;
}

const ORG = "test";

/**
 * The episode is anchored to NOW, not to a fixed calendar date: recharts draws a
 * ReferenceArea against the chart's live time domain (the default view is the
 * last 24 h), and a band outside that domain is legitimately not drawn. A fixed
 * 2026-09-22 window silently tested nothing the day after.
 */
const EPISODE_END_MS = Date.now() - 30 * 60_000;
const EPISODE_START_MS = EPISODE_END_MS - 53 * 60_000;
const EPISODE_START = new Date(EPISODE_START_MS).toISOString();
const EPISODE_END = new Date(EPISODE_END_MS).toISOString();

/**
 * The chart renders no plot at all — and therefore no band — until it has
 * results, so a spec that asserts on the band has to supply them. 24 h of
 * successful probes at 12-minute spacing, the same shape check-chart-zoom.spec.ts
 * uses.
 */
function buildResults() {
  const now = Date.now();
  const count = 120;
  const intervalMs = 12 * 60_000;

  return Array.from({ length: count }, (_, index) => ({
    uid: `degraded-raw-${index}`,
    durationMs: 40 + (index % 5) * 8,
    status: "up",
    periodStart: new Date(now - (count - index) * intervalMs).toISOString(),
    periodType: "raw",
  }));
}

/** Mocks the results endpoint the chart plots, plus the SLO coverage chip. */
async function mockChartData(page: Page): Promise<void> {
  await mockSloCoverage(page);

  const points = buildResults();

  await page.route("**/api/v1/orgs/*/results*", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: points,
        pagination: { total: points.length, size: points.length },
      }),
    }),
  );
}

async function mockChecksList(page: Page, checks: MockCheck[]): Promise<void> {
  await page.route("**/api/v1/orgs/*/check-groups*", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: [] }),
    }),
  );

  await page.route("**/api/v1/orgs/*/check-types*", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: [
          { type: "http", description: "http", labels: [], enabled: true },
        ],
      }),
    }),
  );

  await page.route("**/api/v1/orgs/*/escalation-policies*", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: [] }),
    }),
  );

  await page.route("**/api/v1/orgs/*/checks*", (route) => {
    const url = route.request().url();
    if (
      !url.includes("/checks") ||
      url.includes("/check-groups") ||
      url.includes("/check-types")
    ) {
      return route.continue();
    }

    // Mirrors the server: `?wouldHaveFired=true` keeps only the stamped rows,
    // anything else applies no filter.
    const onlyFlagged =
      new URL(url).searchParams.get("wouldHaveFired") === "true";
    const filtered = onlyFlagged
      ? checks.filter((check) => Boolean(check.degradedWouldFireAt))
      : checks;

    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: filtered.map((check) => ({
          uid: check.uid,
          name: check.name,
          slug: check.uid,
          type: "http",
          enabled: true,
          status: "up",
          period: "00:01:00",
          config: { url: `https://example.com/${check.uid}` },
          degradedFailures: 5,
          degradedFailuresWindow: 60,
          degradedSlow: 3,
          degradedSlowWindow: 6,
          slowThresholdMs: 0,
          degradedEnabled: check.degradedEnabled,
          degradedWouldFireAt: check.degradedWouldFireAt ?? null,
        })),
        pagination: { total: filtered.length },
      }),
    });
  });
}

/** Mocks one check's detail page, its incidents and its (empty) results. */
async function mockCheckDetail(
  page: Page,
  check: MockCheck,
  opts: { degradedIncident: boolean; startedAt?: string; resolvedAt?: string },
): Promise<void> {
  await page.route(`**/api/v1/orgs/*/checks/${check.uid}*`, (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        uid: check.uid,
        name: check.name,
        slug: check.uid,
        type: "http",
        enabled: true,
        status: "up",
        period: "00:01:00",
        regions: [],
        config: { url: `https://example.com/${check.uid}` },
        degradedFailures: 5,
        degradedFailuresWindow: 60,
        degradedSlow: 3,
        degradedSlowWindow: 6,
        slowThresholdMs: 0,
        degradedEnabled: check.degradedEnabled,
        degradedWouldFireAt: check.degradedWouldFireAt ?? null,
      }),
    }),
  );

  await page.route("**/api/v1/orgs/*/incidents*", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: opts.degradedIncident
          ? [
              {
                uid: "e2e-degraded-incident",
                number: 41,
                checkUid: check.uid,
                kind: "degraded",
                state: "resolved",
                startedAt: opts.startedAt ?? EPISODE_START,
                resolvedAt: opts.resolvedAt ?? EPISODE_END,
                title: `${check.name} is degraded: 7 failures in the last 60 probes (93.8%)`,
                failureCount: 7,
                relapseCount: 0,
              },
            ]
          : [],
        pagination: { total: opts.degradedIncident ? 1 : 0 },
      }),
    }),
  );
}

test.describe("Degraded detection — dashboard surfaces", () => {
  test("the dry-run banner names when the rules would have fired and offers to enable them", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const check: MockCheck = {
      uid: "e2e-degraded-dry-run",
      name: "Degraded Dry Run Check",
      degradedEnabled: false,
      degradedWouldFireAt: EPISODE_END,
    };

    await mockChecksList(page, [check]);
    await mockCheckDetail(page, check, { degradedIncident: false });

    await page.goto(`orgs/${ORG}/checks/${check.uid}`);
    await page.waitForLoadState("networkidle");

    const banner = page.getByTestId("degraded-dry-run-banner");
    await expect(banner).toBeVisible();
    await expect(banner).toContainText("degraded");
    await expect(page.getByTestId("degraded-enable-button")).toBeVisible();
    await expect(
      page.getByTestId("degraded-dry-run-window-link"),
    ).toBeVisible();
  });

  test("an enabled check shows no banner", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    // Negative control: the same stamp, but the feature is already on — the
    // banner must not keep asking for something already done.
    const check: MockCheck = {
      uid: "e2e-degraded-enabled",
      name: "Degraded Enabled Check",
      degradedEnabled: true,
      degradedWouldFireAt: EPISODE_END,
    };

    await mockChecksList(page, [check]);
    await mockCheckDetail(page, check, { degradedIncident: false });

    await page.goto(`orgs/${ORG}/checks/${check.uid}`);
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("degraded-dry-run-banner")).toHaveCount(0);
  });

  test("the chart shades a degraded episode", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    const check: MockCheck = {
      uid: "e2e-degraded-band",
      name: "Degraded Band Check",
      degradedEnabled: true,
    };

    await mockChecksList(page, [check]);
    await mockCheckDetail(page, check, { degradedIncident: true });
    await mockChartData(page);

    await page.goto(`orgs/${ORG}/checks/${check.uid}`);
    await page.waitForLoadState("networkidle");

    // A band, not dots: isolated red dots over an hour do not read as an event.
    await expect(
      page.locator('[data-testid="chart-degraded-span"]').first(),
    ).toBeAttached();
  });

  test("a check with no degraded episode gets no band", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    // Negative control for the band test above: same chart, same data, no
    // degraded incident — so a band that rendered unconditionally would fail
    // here instead of passing both ways.
    const check: MockCheck = {
      uid: "e2e-degraded-no-band",
      name: "Degraded No Band Check",
      degradedEnabled: true,
    };

    await mockChecksList(page, [check]);
    await mockCheckDetail(page, check, { degradedIncident: false });
    await mockChartData(page);

    await page.goto(`orgs/${ORG}/checks/${check.uid}`);
    await page.waitForLoadState("networkidle");

    await expect(
      page.locator('[data-testid="chart-degraded-span"]'),
    ).toHaveCount(0);
  });

  test("an episode that began before the visible window still shades its overlap", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    // recharts defaults a ReferenceArea to ifOverflow="discard", which drops the
    // element outright when one edge falls outside the domain. An hour-long
    // episode that started before a 24 h view is the COMMON case, and dropping
    // it means the operator sees isolated dots again — the exact failure this
    // feature exists to fix. The chart passes ifOverflow="hidden" so the overlap
    // is drawn, clipped to the plot area.
    const check: MockCheck = {
      uid: "e2e-degraded-overflow",
      name: "Degraded Overflow Check",
      degradedEnabled: true,
    };

    await mockChecksList(page, [check]);
    await mockCheckDetail(page, check, {
      degradedIncident: true,
      // Starts 36 h back — outside the default 24 h domain — and ends inside it.
      startedAt: new Date(Date.now() - 36 * 60 * 60_000).toISOString(),
      resolvedAt: new Date(Date.now() - 60 * 60_000).toISOString(),
    });
    await mockChartData(page);

    await page.goto(`orgs/${ORG}/checks/${check.uid}`);
    await page.waitForLoadState("networkidle");

    await expect(
      page.locator('[data-testid="chart-degraded-span"]').first(),
    ).toBeAttached();
  });

  test("the wouldHaveFired filter narrows the checks list and round-trips through the URL", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const checks: MockCheck[] = [
      {
        uid: "e2e-wh-flagged",
        name: "WH Flagged Check",
        degradedEnabled: false,
        degradedWouldFireAt: EPISODE_END,
      },
      {
        uid: "e2e-wh-quiet",
        name: "WH Quiet Check",
        degradedEnabled: false,
      },
    ];

    await mockChecksList(page, checks);

    await page.goto(`orgs/${ORG}/checks`);
    await page.waitForLoadState("networkidle");

    await expect(page.getByText("WH Flagged Check")).toBeVisible();
    await expect(page.getByText("WH Quiet Check")).toBeVisible();

    await page.getByTestId("would-have-fired-filter").click();
    await page.waitForLoadState("networkidle");

    // A bare `true`, not a JSON-quoted string: the param has to survive being
    // copied out of the address bar and pasted back in.
    await expect
      .poll(() => new URL(page.url()).searchParams.get("wouldHaveFired"))
      .toBe("true");

    await expect(page.getByText("WH Flagged Check")).toBeVisible();
    // Negative control: the never-flagged check is gone, not merely lower down.
    await expect(page.getByText("WH Quiet Check")).not.toBeVisible();

    // Toggling off restores the full list and clears the param.
    await page.getByTestId("would-have-fired-filter").click();
    await page.waitForLoadState("networkidle");

    await expect
      .poll(() => new URL(page.url()).searchParams.has("wouldHaveFired"))
      .toBe(false);
    await expect(page.getByText("WH Quiet Check")).toBeVisible();
  });

  test("a cold deep link with ?wouldHaveFired=true filters on first paint", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    // The half of this that a bookmarked or pasted URL depends on. While the
    // param was a string typed "true", the router handed validateSearch a native
    // boolean for this URL, the string comparison missed it, and the link
    // silently showed the unfiltered list.
    const checks: MockCheck[] = [
      {
        uid: "e2e-wh-cold-flagged",
        name: "WH Cold Flagged Check",
        degradedEnabled: false,
        degradedWouldFireAt: EPISODE_END,
      },
      {
        uid: "e2e-wh-cold-quiet",
        name: "WH Cold Quiet Check",
        degradedEnabled: false,
      },
    ];

    await mockChecksList(page, checks);

    await page.goto(`orgs/${ORG}/checks?wouldHaveFired=true`);
    await page.waitForLoadState("networkidle");

    await expect(page.getByText("WH Cold Flagged Check")).toBeVisible();
    await expect(page.getByText("WH Cold Quiet Check")).not.toBeVisible();
    await expect(page.getByTestId("would-have-fired-filter")).toHaveAttribute(
      "aria-pressed",
      "true",
    );
  });
});
