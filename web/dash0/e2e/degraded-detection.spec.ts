import { test, expect, type Page } from "./fixtures";

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

/** A fixed hour-long window, so the band's position is deterministic. */
const EPISODE_START = "2026-09-22T14:35:00.000Z";
const EPISODE_END = "2026-09-22T15:28:00.000Z";

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
  opts: { degradedIncident: boolean },
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
                startedAt: EPISODE_START,
                resolvedAt: EPISODE_END,
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

    await page.goto(`orgs/${ORG}/checks/${check.uid}`);
    await page.waitForLoadState("networkidle");

    // A band, not dots: isolated red dots over an hour do not read as an event.
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
});
