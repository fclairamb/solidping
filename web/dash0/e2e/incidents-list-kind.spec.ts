import type { Page, Route } from "@playwright/test";

import { test, expect, DASH_BASE } from "./fixtures";

/**
 * Spec 2026-09-27-02: the incidents list shows each incident's kind (Down /
 * Degraded / SLO burn) as a chip, the full title (no truncation), an
 * "escalated → #n" badge on a degraded incident superseded by an outage, and
 * a kind filter backed by `?kind=` on the list API.
 *
 * Fully mocked, like incidents-list-check-name.spec.ts, so it asserts the
 * rendering rules rather than whatever the seeded org contains.
 */

const CHECK_UID = "66666666-6666-6666-6666-666666666661";
const SLO_CHECK_UID = "66666666-6666-6666-6666-666666666662";

const OUTAGE_UID = "77777777-7777-7777-7777-777777777771";
const DEGRADED_UID = "77777777-7777-7777-7777-777777777772";
const SLO_UID = "77777777-7777-7777-7777-777777777773";

const CHECK_SLUG = "region-heartbeat-lauterbourg-2";
const CHECK_NAME = "Lauterbourg heartbeat";

// 90 characters: the words that say what happened start around character 30,
// exactly where the old layout cut every row to "region-heartbeat-lauter…".
const LONG_TITLE = `${CHECK_SLUG} is degraded: 7 of the last 10 probes were slower than 800ms`;
const SHORT_TITLE = `${CHECK_SLUG} is down`;
const SLO_TITLE = "Fast burn: API availability error budget burning at 14.4x";

const minutesAgo = (m: number) => new Date(Date.now() - m * 60_000).toISOString();

const CHECKS = [
  { uid: CHECK_UID, name: CHECK_NAME, slug: CHECK_SLUG, type: "heartbeat" },
  { uid: SLO_CHECK_UID, name: "API", slug: "api", type: "http" },
];

const INCIDENTS = [
  {
    uid: OUTAGE_UID,
    number: 98,
    kind: "check",
    checkUid: CHECK_UID,
    checkSlug: CHECK_SLUG,
    checkName: CHECK_NAME,
    state: "active",
    title: SHORT_TITLE,
    startedAt: minutesAgo(8),
    failureCount: 4,
    // The outage opened while the degraded episode below was still open.
    causedByIncidentUid: DEGRADED_UID,
  },
  {
    uid: DEGRADED_UID,
    number: 97,
    kind: "degraded",
    checkUid: CHECK_UID,
    checkSlug: CHECK_SLUG,
    checkName: CHECK_NAME,
    state: "resolved",
    title: LONG_TITLE,
    startedAt: minutesAgo(56),
    resolvedAt: minutesAgo(8),
    resolutionType: "escalated",
    failureCount: 7,
  },
  {
    uid: SLO_UID,
    number: 96,
    kind: "slo_burn",
    checkUid: SLO_CHECK_UID,
    checkSlug: "api",
    checkName: "API",
    state: "resolved",
    title: SLO_TITLE,
    startedAt: minutesAgo(300),
    resolvedAt: minutesAgo(240),
    resolutionType: "auto",
    failureCount: 0,
  },
];

const json = (body: unknown) => ({
  status: 200,
  contentType: "application/json",
  body: JSON.stringify(body),
});

/** Mocks every list the page reads; returns the incidents request URLs. */
async function mockIncidentsPage(page: Page): Promise<string[]> {
  const incidentRequests: string[] = [];

  await page.route("**/api/v1/orgs/*/incidents**", (route: Route) => {
    const url = new URL(route.request().url());
    incidentRequests.push(url.toString());
    const kinds = url.searchParams.get("kind")?.split(",");
    const data = kinds ? INCIDENTS.filter((i) => kinds.includes(i.kind)) : INCIDENTS;
    return route.fulfill(json({ data, pagination: { total: data.length, size: 50 } }));
  });
  await page.route("**/api/v1/orgs/*/checks**", (route) =>
    route.fulfill(json({ data: CHECKS, pagination: { total: CHECKS.length } })),
  );
  await page.route("**/api/v1/orgs/*/check-groups**", (route) =>
    route.fulfill(json({ data: [] })),
  );

  return incidentRequests;
}

const rowFor = (page: Page, uid: string) => page.locator(`[data-testid="incident-row"][data-incident-uid="${uid}"]`);

test.describe("Incidents list: kind, full title, escalation", () => {
  test("each row shows its kind, the full title and the escalation link", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await mockIncidentsPage(page);

    await page.goto(`${DASH_BASE}/orgs/test/incidents`);
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("incident-row")).toHaveCount(3);

    const cases = [
      { uid: OUTAGE_UID, kind: "check", label: "Down", state: "active" },
      { uid: DEGRADED_UID, kind: "degraded", label: "Degraded", state: "resolved" },
      { uid: SLO_UID, kind: "slo_burn", label: "SLO burn", state: "resolved" },
    ];
    for (const c of cases) {
      const row = rowFor(page, c.uid);
      await expect(row).toHaveAttribute("data-incident-kind", c.kind);
      const chip = row.getByTestId("incident-kind-chip");
      await expect(chip).toHaveText(c.label);
      await expect(chip).toHaveAttribute("data-state", c.state);
    }

    // Full title, not clipped. The short title is the control: both must
    // pass the same "no horizontal clipping" check, and the long one must
    // actually render every character.
    expect(LONG_TITLE).toHaveLength(90);
    const longTitle = rowFor(page, DEGRADED_UID).getByTestId("incident-title");
    const shortTitle = rowFor(page, OUTAGE_UID).getByTestId("incident-title");
    await expect(longTitle).toHaveText(LONG_TITLE);
    await expect(shortTitle).toHaveText(SHORT_TITLE);
    for (const title of [longTitle, shortTitle]) {
      await expect(title).toBeVisible();
      const { scrollWidth, clientWidth, textOverflow } = await title.evaluate((el) => ({
        scrollWidth: el.scrollWidth,
        clientWidth: el.clientWidth,
        textOverflow: getComputedStyle(el).textOverflow,
      }));
      expect(scrollWidth).toBeLessThanOrEqual(clientWidth);
      expect(textOverflow).not.toBe("ellipsis");
    }

    // The check display name sits under the title and links to the check.
    await expect(rowFor(page, OUTAGE_UID).getByTestId("incident-check-link")).toHaveText(CHECK_NAME);

    // The resolved degraded episode names the outage that superseded it.
    const escalated = rowFor(page, DEGRADED_UID).getByTestId("incident-escalated-badge");
    await expect(escalated).toHaveText(/escalated → #98/i);
    await expect(escalated).toHaveAttribute("href", new RegExp(`/incidents/${OUTAGE_UID}$`));
    // Negative control: rows that did not escalate carry no such badge.
    await expect(rowFor(page, OUTAGE_UID).getByTestId("incident-escalated-badge")).toHaveCount(0);
    await expect(rowFor(page, SLO_UID).getByTestId("incident-escalated-badge")).toHaveCount(0);

    // When column: ongoing for the active row, "lasted" once resolved.
    await expect(rowFor(page, OUTAGE_UID).getByTestId("incident-when")).toContainText("ongoing");
    await expect(rowFor(page, DEGRADED_UID).getByTestId("incident-when")).toContainText("lasted 48m");

    // The Check column is gone: the header reads Incident · When · Failures.
    await expect(page.getByRole("columnheader")).toHaveText(["Incident", "When", "Failures"]);
  });

  test("the kind filter narrows the list through ?kind= on the page and the API", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const incidentRequests = await mockIncidentsPage(page);

    await page.goto(`${DASH_BASE}/orgs/test/incidents?state=all`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("incident-row")).toHaveCount(3);

    // Positive control: with no kind picked, nothing is sent.
    expect(incidentRequests.length).toBeGreaterThan(0);
    for (const u of incidentRequests) {
      expect(new URL(u).searchParams.has("kind")).toBe(false);
    }

    await page.getByTestId("incidents-kind-filter").click();
    await page.getByRole("option", { name: "Degraded" }).click();

    await expect(page).toHaveURL(/[?&]kind=degraded(&|$)/);
    // The other search params are carried forward.
    await expect(page).toHaveURL(/[?&]state=all(&|$)/);
    await expect
      .poll(() => incidentRequests.some((u) => new URL(u).searchParams.get("kind") === "degraded"))
      .toBe(true);
    await expect(page.getByTestId("incident-row")).toHaveCount(1);
    await expect(page.getByTestId("incident-row")).toHaveAttribute("data-incident-kind", "degraded");

    // Back to all kinds: the param leaves the URL again.
    await page.getByTestId("incidents-kind-filter").click();
    await page.getByRole("option", { name: "All kinds" }).click();
    await expect(page).not.toHaveURL(/[?&]kind=/);
    await expect(page.getByTestId("incident-row")).toHaveCount(3);
  });

  test("an empty kind filter says which kind has no incidents", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    await page.route("**/api/v1/orgs/*/incidents**", (route) =>
      route.fulfill(json({ data: [], pagination: { total: 0, size: 50 } })),
    );
    await page.route("**/api/v1/orgs/*/checks**", (route) =>
      route.fulfill(json({ data: CHECKS, pagination: { total: CHECKS.length } })),
    );
    await page.route("**/api/v1/orgs/*/check-groups**", (route) =>
      route.fulfill(json({ data: [] })),
    );

    await page.goto(`${DASH_BASE}/orgs/test/incidents?kind=degraded`);
    await expect(page.getByText("No degraded incidents")).toBeVisible();
  });

  test("phone (375px): no horizontal scroll; chip, number, title and time on every row", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.setViewportSize({ width: 375, height: 812 });
    await mockIncidentsPage(page);

    await page.goto(`${DASH_BASE}/orgs/test/incidents`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("incident-row")).toHaveCount(3);

    const overflows = await page.evaluate(
      () => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1,
    );
    expect(overflows).toBe(false);

    for (const uid of [OUTAGE_UID, DEGRADED_UID, SLO_UID]) {
      const row = rowFor(page, uid);
      await expect(row.getByTestId("incident-kind-chip")).toBeVisible();
      await expect(row.getByTestId("incident-number")).toBeVisible();
      await expect(row.getByTestId("incident-title")).toBeVisible();
      await expect(row.getByTestId("incident-started-at")).toBeVisible();
      await expect(row.getByTestId("incident-started-at")).toHaveCount(1);

      const clipped = await row
        .getByTestId("incident-title")
        .evaluate((el) => el.scrollWidth > el.clientWidth);
      expect(clipped).toBe(false);
    }
    await expect(rowFor(page, DEGRADED_UID).getByTestId("incident-title")).toHaveText(LONG_TITLE);
  });
});
