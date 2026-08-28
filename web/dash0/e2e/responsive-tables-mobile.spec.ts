import { test, expect, type Page } from "./fixtures";

/**
 * Coverage for spec 2026-08-28-08 (checks/incidents) and spec 2026-08-28-09
 * (members/SLOs/integrations): these list tables hide secondary columns
 * below `sm`/`md`/`lg` instead of side-scrolling on a phone. This is fully
 * mocked (deterministic) rather than relying on whatever the `test` org
 * happens to have seeded, so it can exercise the exact edge cases the fix
 * targets:
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

// Spec 2026-08-28-09: same idiom applied to members, SLOs and integrations.
const LONG_MEMBER_EMAIL =
  "verylongmemberemailaddresswithoutanyspacesthatcouldwraptoavoidoverflow@example.com";
const LONG_SLO_NAME =
  "verylongsloobjectivenamewithoutanyspacesthatcouldwraptoavoidoverflow1234567890";
const LONG_INTEGRATION_NAME =
  "verylongintegrationnamewithoutanyspacesthatcouldwraptoavoidoverflow1234567890";

test.describe("Members list (mobile)", () => {
  test("no horizontal overflow at 375px; member/role/actions stay visible", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.setViewportSize({ width: 375, height: 812 });

    // A member with no display name falls back to the (long, unbroken)
    // email as the identity cell's text — the exact case the spec calls
    // out ("long email-as-name") for the max-w-0/truncate treatment.
    await page.route("**/api/v1/orgs/*/members/coverage", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ data: [] }),
      }),
    );
    await page.route("**/api/v1/orgs/*/members", (route) => {
      if (route.request().method() !== "GET") return route.continue();
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: [
            {
              uid: "e2e-mobile-member-1",
              userUid: "e2e-mobile-user-1",
              email: LONG_MEMBER_EMAIL,
              role: "admin",
              createdAt: new Date().toISOString(),
            },
          ],
        }),
      });
    });

    await page.goto("orgs/test/organization/members");
    await page.waitForLoadState("networkidle");

    const row = page.getByRole("row", { name: new RegExp(LONG_MEMBER_EMAIL) });
    await expect(row).toBeVisible();

    // Always-visible columns: member identity (long text truncated rather
    // than overflowing), role, and the row actions.
    await expect(row.getByText(LONG_MEMBER_EMAIL)).toBeVisible();
    await expect(row.getByTestId(`member-role-${LONG_MEMBER_EMAIL}`)).toBeVisible();
    await expect(row.getByRole("button", { name: "Remove" })).toBeVisible();

    // Secondary columns are hidden below their breakpoint.
    await expect(page.getByText("Email", { exact: true })).toBeHidden();
    await expect(page.getByText("Joined", { exact: true })).toBeHidden();

    await assertNoHorizontalOverflow(page);
  });
});

test.describe("SLOs list (mobile)", () => {
  test("no horizontal overflow at 375px; name/attainment/state/actions stay visible", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.setViewportSize({ width: 375, height: 812 });

    const SLO_UID = "e2e-mobile-slo-1";

    await page.route("**/api/v1/orgs/*/slos/*/status", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          slo: { uid: SLO_UID, name: LONG_SLO_NAME },
          current: {
            window: { start: new Date().toISOString(), end: new Date().toISOString(), label: "This month" },
            attainmentPct: 99.95,
            hasData: true,
            targetPct: 99.9,
            totalChecks: 1000,
            successfulChecks: 999,
            monitoredSeconds: 2_000_000,
            elapsedSeconds: 2_000_000,
            budgetTotalSeconds: 2_592_000,
            budgetConsumedSeconds: 200,
            budgetRemainingSeconds: 2_591_800,
            excludedMaintenanceSeconds: 0,
            burnRate: 0.1,
            projectedExhaustionAt: null,
            state: "healthy",
            partial: false,
          },
          incidents: { count: 0 },
        }),
      }),
    );
    await page.route("**/api/v1/orgs/*/slos", (route) => {
      if (route.request().method() !== "GET") return route.continue();
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: [
            {
              uid: SLO_UID,
              name: LONG_SLO_NAME,
              slug: "e2e-mobile-slo-1",
              checkUid: "e2e-mobile-slo-check-1",
              checkName: "Mobile SLO Check",
              targetPct: 99.9,
              timezone: "UTC",
              excludeMaintenance: false,
              enabled: true,
              createdAt: new Date().toISOString(),
              updatedAt: new Date().toISOString(),
            },
          ],
        }),
      });
    });

    await page.goto("orgs/test/slos");
    await page.waitForLoadState("networkidle");

    const row = page.getByTestId("slo-row").first();
    await expect(row).toBeVisible();

    // Always-visible columns: name (long text truncated rather than
    // overflowing), attainment, state, and the row actions.
    await expect(row.getByTestId("slo-row-name")).toBeVisible();
    await expect(row.getByTestId("slo-row-name")).toHaveText(LONG_SLO_NAME);
    await expect(row.getByTestId("slo-row-attainment")).toBeVisible();
    await expect(row.getByTestId("slo-row-state")).toBeVisible();
    await expect(row.getByTestId("slo-row-delete")).toBeVisible();

    // Secondary columns are hidden below their breakpoint.
    await expect(page.getByText("Scope", { exact: true })).toBeHidden();
    await expect(page.getByText("Target", { exact: true })).toBeHidden();
    await expect(page.getByText("Budget remaining", { exact: true })).toBeHidden();

    await assertNoHorizontalOverflow(page);
  });
});

test.describe("Integrations list (mobile)", () => {
  test("no horizontal overflow at 375px; name/status/actions stay visible", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.setViewportSize({ width: 375, height: 812 });

    await page.route("**/api/v1/orgs/*/integrations", (route) => {
      if (route.request().method() !== "GET") return route.continue();
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: [
            {
              uid: "e2e-mobile-integration-1",
              type: "webhook",
              name: LONG_INTEGRATION_NAME,
              enabled: true,
              isDefault: false,
              createdAt: new Date().toISOString(),
              updatedAt: new Date().toISOString(),
            },
          ],
        }),
      });
    });

    await page.goto("orgs/test/integrations");
    await page.waitForLoadState("networkidle");

    const row = page.getByRole("row", { name: new RegExp(LONG_INTEGRATION_NAME) });
    await expect(row).toBeVisible();

    // Always-visible columns: name (long text truncated rather than
    // overflowing), status, and the row actions.
    await expect(row.getByText(LONG_INTEGRATION_NAME)).toBeVisible();
    await expect(row.getByText("Enabled")).toBeVisible();
    await expect(row.getByRole("button", { name: "Edit" })).toBeVisible();

    // Secondary columns are hidden below their breakpoint.
    await expect(page.getByText("Type", { exact: true })).toBeHidden();
    await expect(page.getByText("Used by", { exact: true })).toBeHidden();
    await expect(page.getByText("Updated", { exact: true })).toBeHidden();

    await assertNoHorizontalOverflow(page);
  });
});
