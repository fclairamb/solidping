import { test, expect } from "./fixtures";

/**
 * Spec 2026-08-28-11: the Incidents list "Check" column must render the
 * check's display NAME, not its slug — the fallback order used to be
 * inverted (`checkSlug || checkName`).
 *
 * The fixture deliberately uses a name and slug that read differently, so
 * the assertion actually proves which field won. A fixture where
 * `checkName === checkSlug` (as in incident-group-header.spec.ts) cannot
 * distinguish the two orderings.
 */

const CHECK_UID = "44444444-4444-4444-4444-444444444444";
const INCIDENT_UID = "55555555-5555-5555-5555-555555555555";

const CHECK_SLUG = "http-test-api";
const CHECK_NAME = "Payments API";

const CHECKS = [
  { uid: CHECK_UID, name: CHECK_NAME, slug: CHECK_SLUG, type: "http" },
];

const INCIDENTS = [
  {
    uid: INCIDENT_UID,
    number: 1,
    checkUid: CHECK_UID,
    checkSlug: CHECK_SLUG,
    checkName: CHECK_NAME,
    state: "active",
    title: `${CHECK_NAME} is down`,
    startedAt: "2026-08-23T23:23:30Z",
    failureCount: 3,
  },
];

test.describe("Incidents list: Check column", () => {
  test("renders the check name, not its slug", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    const json = (body: unknown) => ({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(body),
    });

    await page.route("**/api/v1/orgs/*/incidents**", (route) =>
      route.fulfill(
        json({ data: INCIDENTS, pagination: { total: INCIDENTS.length, size: INCIDENTS.length } }),
      ),
    );
    await page.route("**/api/v1/orgs/*/checks**", (route) =>
      route.fulfill(json({ data: CHECKS, pagination: { total: CHECKS.length } })),
    );
    await page.route("**/api/v1/orgs/*/check-groups**", (route) =>
      route.fulfill(json({ data: [] })),
    );

    await page.goto("/dash0/orgs/test/incidents");
    await page.waitForLoadState("networkidle");

    const row = page.getByTestId("incident-row").filter({ hasText: CHECK_NAME }).first();
    await expect(row).toBeVisible();

    const checkLink = row.locator('a[href*="/checks/"]');
    await expect(checkLink).toHaveText(CHECK_NAME);
    await expect(checkLink).not.toHaveText(CHECK_SLUG);
  });
});
