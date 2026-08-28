import { test, expect, type Page } from "./fixtures";

/**
 * Coverage for spec 2026-08-28-08: the checks and incidents list tables now
 * hide secondary columns below `sm`/`md` instead of side-scrolling on a
 * phone. This is fully mocked (deterministic) rather than relying on
 * whatever the `test` org happens to have seeded, so it can exercise the
 * exact edge cases the fix targets:
 *   - a check/incident name with no natural break point (the class of string
 *     that forces horizontal overflow if a column relies on wrapping alone)
 *   - an incident with every badge (snoozed/acked/relapse/flapping/rolled-up
 *     are mutually exclusive in pairs, so this uses relapse+flapping+
 *     rolled-up) to stress the badge-cluster flex-wrap fix
 *   - an incident whose check belongs to a check group, so the
 *     GroupHeaderRow (colSpan) renders too
 *
 * The real invariant asserted throughout is
 * `document.documentElement.scrollWidth <= clientWidth` — hidden columns
 * alone don't guarantee that; an unbounded cell can still force the page
 * wider even with every secondary column gone.
 */

const LONG_CHECK_NAME =
  "verylongcheckhostnamewithoutanyspacesthatcouldwraptoavoidoverflow1234567890";
const LONG_INCIDENT_TITLE =
  "verylongincidenttitlewithoutanyspacesthatcouldwraptoavoidoverflow0987654321";

async function assertNoHorizontalOverflow(page: Page): Promise<void> {
  const overflows = await page.evaluate(
    () =>
      document.documentElement.scrollWidth >
      document.documentElement.clientWidth + 1,
  );
  expect(overflows).toBe(false);
}

test.describe("Checks list (mobile)", () => {
  test("no horizontal overflow at 375px; name/response/actions stay visible", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.setViewportSize({ width: 375, height: 812 });

    await page.route("**/api/v1/orgs/*/check-groups*", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ data: [] }),
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
      if (url.includes("/check-groups") || url.includes("/escalation-policies")) {
        return route.continue();
      }
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: [
            {
              uid: "e2e-mobile-check-1",
              name: LONG_CHECK_NAME,
              slug: "e2e-mobile-check-1",
              type: "http",
              enabled: true,
              checkGroupUid: null,
              status: "up",
              lastResult: { status: "up", durationMs: 42 },
              config: { url: `https://${LONG_CHECK_NAME}/` },
            },
          ],
          pagination: { total: 1 },
        }),
      });
    });

    await page.goto("orgs/test/checks");
    await page.waitForLoadState("networkidle");

    const row = page.getByRole("row", { name: new RegExp(LONG_CHECK_NAME) });
    await expect(row).toBeVisible();

    // Always-visible columns: name (with the long, unbroken text truncated
    // rather than overflowing), response time, and the row actions trigger.
    await expect(row.getByText(LONG_CHECK_NAME)).toBeVisible();
    await expect(row.getByText("42ms")).toBeVisible();
    const actionsButton = row.getByRole("button").last();
    await expect(actionsButton).toBeVisible();

    // Secondary columns are hidden below their breakpoint.
    await expect(page.getByText("Type", { exact: true })).toBeHidden();
    await expect(page.getByText("Target", { exact: true })).toBeHidden();

    await assertNoHorizontalOverflow(page);
  });
});

test.describe("Incidents list (mobile)", () => {
  test("no horizontal overflow at 375px with a grouped, badge-heavy, long-title incident", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.setViewportSize({ width: 375, height: 812 });

    const GROUP_UID = "e2e-mobile-group-1";
    const CHECK_UID = "e2e-mobile-incident-check-1";

    await page.route("**/api/v1/orgs/*/check-groups*", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: [
            { uid: GROUP_UID, name: "Mobile Group", slug: "mobile-group", checkCount: 1, sortOrder: 0 },
          ],
        }),
      }),
    );
    await page.route("**/api/v1/orgs/*/checks*", (route) => {
      const url = route.request().url();
      if (url.includes("/check-groups")) return route.continue();
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: [
            {
              uid: CHECK_UID,
              name: "Mobile Check",
              slug: "mobile-check",
              type: "http",
              enabled: true,
              checkGroupUid: GROUP_UID,
              status: "down",
            },
          ],
          pagination: { total: 1 },
        }),
      });
    });
    await page.route("**/api/v1/orgs/*/incidents*", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: [
            {
              uid: "e2e-mobile-incident-1",
              number: 4242,
              checkUid: CHECK_UID,
              checkName: "Mobile Check",
              checkSlug: "mobile-check",
              state: "active",
              title: LONG_INCIDENT_TITLE,
              startedAt: new Date(Date.now() - 5 * 60_000).toISOString(),
              failureCount: 3,
              relapseCount: 2,
              flapLevel: 1,
              pagingSuppressed: true,
            },
          ],
          pagination: { total: 1 },
        }),
      }),
    );

    await page.goto("orgs/test/incidents");
    await page.waitForLoadState("networkidle");

    const groupHeader = page.getByTestId("incident-group-header");
    await expect(groupHeader).toBeVisible();

    const row = page.getByTestId("incident-row").first();
    await expect(row).toBeVisible();

    // Always-visible: title (long, unbroken text truncated rather than
    // overflowing) and the started-at timestamp.
    await expect(row.getByText(LONG_INCIDENT_TITLE)).toBeAttached();
    await expect(row.getByTestId("incident-started-at")).toBeVisible();
    await expect(row.getByTestId("incident-number")).toBeVisible();

    // The badge cluster (relapse + flapping + rolled-up) must all render —
    // flex-wrap lets them drop to a second line instead of forcing overflow.
    await expect(row.getByText("rolled up")).toBeVisible();

    // Secondary columns are hidden below their breakpoint.
    await expect(page.getByText("Check", { exact: true })).toBeHidden();
    await expect(page.getByText("Failures", { exact: true })).toBeHidden();

    await assertNoHorizontalOverflow(page);
  });
});
