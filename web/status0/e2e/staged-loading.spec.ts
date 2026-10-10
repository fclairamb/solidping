import { test, expect, type Page, type Route } from "@playwright/test";
import { API_BASE as BASE, STATUS_BASE } from "./fixtures";

/**
 * Staged public page load (spec 2026-10-10-01). The API is mocked per stage
 * (`?include=` / `?include=availability,responseTime` / `?include=updates`):
 * the point under test is the client's ordering and failure behaviour, which a
 * seeded server cannot make deterministic (it cannot delay or fail one stage).
 */
const ORG = "e2e-staged";
const SLUG = "staged";
const RES_UID = "88888888-8888-8888-8888-888888888888";

function resource(withAvailability: boolean) {
  return {
    uid: RES_UID,
    checkUid: "99999999-9999-9999-9999-999999999999",
    position: 0,
    check: { name: "API", type: "http", status: "up", inMaintenance: false },
    ...(withAvailability
      ? {
          availability: {
            dailyAvailability: [
              {
                date: "2026-07-01",
                time: "2026-07-01T00:00:00Z",
                availabilityPct: 100,
                status: "up",
              },
            ],
            overallAvailabilityPct: 99.944,
            period: "30d",
            bucketUnit: "day",
            responseTimeSeries: [],
          },
        }
      : {}),
  };
}

function payload(withAvailability: boolean) {
  return {
    uid: "66666666-6666-6666-6666-666666666666",
    name: "Staged Demo Page",
    slug: SLUG,
    visibility: "public",
    isDefault: false,
    enabled: true,
    showAvailability: true,
    showResponseTime: false,
    historyDays: 30,
    historyPeriod: "30d",
    availabilityThresholds: { thresholdUp: 99.9, thresholdDegraded: 99 },
    overallStatus: "operational",
    sections: [
      {
        uid: "77777777-7777-7777-7777-777777777777",
        name: "Core",
        slug: "core",
        position: 0,
        resources: [resource(withAvailability)],
      },
    ],
  };
}

type Stage = "base" | "details" | "updates";

function stageOf(url: string): Stage {
  const include = new URL(url).searchParams.get("include");
  if (include === null || include === "") return "base";
  return include.includes("availability") ? "details" : "updates";
}

async function mockStages(
  page: Page,
  onStage: (stage: Stage, route: Route) => Promise<void> | void,
  seen: Stage[] = [],
) {
  await page.route(`**/api/v1/status-pages/${ORG}/${SLUG}*`, async (route) => {
    const stage = stageOf(route.request().url());
    seen.push(stage);
    await onStage(stage, route);
  });
  await page.route(
    `**/api/v1/status-pages/${ORG}/${SLUG}/incidents*`,
    (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ data: [] }),
      }),
  );
}

function ok(route: Route, body: unknown) {
  return route.fulfill({
    status: 200,
    contentType: "application/json",
    body: JSON.stringify(body),
  });
}

test.describe("Public status page - staged loading", () => {
  test("sections render before the delayed availability stage lands", async ({
    page,
  }) => {
    let release: () => void = () => {};
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });

    await mockStages(page, async (stage, route) => {
      if (stage === "base") return ok(route, payload(false));
      if (stage === "details") {
        await gate;
        return ok(route, payload(true));
      }
      return ok(route, { ...payload(false), recentUpdates: [] });
    });

    await page.goto(`${BASE}${STATUS_BASE}/${ORG}/${SLUG}`);

    await expect(page.getByText("Core")).toBeVisible();
    await expect(page.getByText("API")).toBeVisible();
    await expect(page.getByTestId("availability-skeleton")).toBeVisible();
    await expect(page.getByTestId("resource-availability-pct")).toHaveCount(0);

    release();

    await expect(page.getByTestId("resource-availability-pct")).toBeVisible();
    await expect(page.getByTestId("availability-skeleton")).toHaveCount(0);
  });

  test("a failing details stage keeps the sections on screen", async ({
    page,
  }) => {
    await mockStages(page, (stage, route) => {
      if (stage === "base") return ok(route, payload(false));
      if (stage === "details") {
        return route.fulfill({
          status: 500,
          contentType: "application/json",
          body: JSON.stringify({ title: "boom", code: "INTERNAL_ERROR" }),
        });
      }
      return ok(route, payload(false));
    });

    await page.goto(`${BASE}${STATUS_BASE}/${ORG}/${SLUG}`);
    await page.waitForLoadState("networkidle");

    await expect(page.getByText("Core")).toBeVisible();
    await expect(page.getByText("API")).toBeVisible();
    await expect(page.getByText(/not found/i)).toHaveCount(0);
  });

  test("a locked page shows the unlock form and no later stage fires", async ({
    page,
  }) => {
    const seen: Stage[] = [];
    await mockStages(
      page,
      (_stage, route) =>
        route.fulfill({
          status: 401,
          contentType: "application/json",
          body: JSON.stringify({
            title: "This status page is password protected",
            code: "STATUS_PAGE_LOCKED",
          }),
        }),
      seen,
    );

    await page.goto(`${BASE}${STATUS_BASE}/${ORG}/${SLUG}`);
    await expect(page.getByTestId("status-page-password-input")).toBeVisible();
    await page.waitForLoadState("networkidle");

    expect(seen.every((stage) => stage === "base")).toBe(true);
  });

  test("a resource row keeps its height when the availability stage lands", async ({
    page,
  }) => {
    let release: () => void = () => {};
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });

    const times = [0, 1, 2].map((i) =>
      new Date(Date.UTC(2026, 6, 1, i)).toISOString(),
    );
    const full = payload(true);
    const [section] = full.sections;
    const [res] = section.resources;
    const details = {
      ...full,
      showResponseTime: true,
      sections: [
        {
          ...section,
          resources: [
            {
              ...res,
              availability: {
                ...res.availability,
                responseTimeSeries: [
                  {
                    region: "eu1",
                    points: times.map((time) => ({
                      time,
                      durationP95: 40,
                      status: "up",
                      totalChecks: 60,
                      successfulChecks: 60,
                      availabilityPct: 100,
                      availabilityStatus: "up",
                    })),
                  },
                ],
              },
            },
          ],
        },
      ],
    };

    await mockStages(page, async (stage, route) => {
      if (stage === "base")
        return ok(route, { ...payload(false), showResponseTime: true });
      if (stage === "details") {
        await gate;
        return ok(route, details);
      }
      return ok(route, { ...payload(false), recentUpdates: [] });
    });

    await page.goto(`${BASE}${STATUS_BASE}/${ORG}/${SLUG}`);

    const row = page.getByTestId("resource-row");
    await expect(page.getByTestId("availability-skeleton")).toBeVisible();
    await expect(page.getByTestId("response-time-skeleton")).toBeVisible();
    await expect(
      page.getByTestId("overall-uptime-pill-skeleton"),
    ).toBeVisible();
    const before = await row.boundingBox();

    release();

    await expect(
      page.getByTestId("response-time-chart-availability-strip"),
    ).toBeVisible();
    await expect(page.getByTestId("overall-uptime-pill")).toBeVisible();
    await expect(page.getByTestId("availability-skeleton")).toHaveCount(0);
    await expect(page.getByTestId("response-time-skeleton")).toHaveCount(0);
    const after = await row.boundingBox();

    expect(Math.abs(after!.height - before!.height)).toBeLessThan(1);
  });

  test("the org's default page (no slug) loads in the same stages", async ({
    page,
  }) => {
    let release: () => void = () => {};
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    const seen: Stage[] = [];

    // The default page is read from /status-pages/{org}, without a slug.
    await page.route(
      (url) => url.pathname === `/api/v1/status-pages/${ORG}`,
      async (route) => {
        const stage = stageOf(route.request().url());
        seen.push(stage);
        if (stage === "base") return ok(route, payload(false));
        if (stage === "details") {
          await gate;
          return ok(route, payload(true));
        }
        return ok(route, { ...payload(false), recentUpdates: [] });
      },
    );
    await page.route(
      `**/api/v1/status-pages/${ORG}/${SLUG}/incidents*`,
      (route) => ok(route, { data: [] }),
    );

    await page.goto(`${BASE}${STATUS_BASE}/${ORG}/`);

    await expect(page.getByText("API")).toBeVisible();
    await expect(page.getByTestId("availability-skeleton")).toBeVisible();

    release();

    await expect(page.getByTestId("resource-availability-pct")).toBeVisible();
    expect([...new Set(seen)].sort()).toEqual(["base", "details", "updates"]);
  });
});
