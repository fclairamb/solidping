import { test, expect } from "./fixtures";

/**
 * The blast-radius table (incident detail page, "rolled up" children) used to
 * render raw check UUIDs and overflow at mobile widths — see
 * specs/todos/2026-08-17-06-blast-radius-table-renders-check-uuids.md.
 *
 * This is fully deterministic: it mocks every /incidents endpoint touching
 * the fake parent/child UIDs below so the test never depends on the org
 * having real rolled-up incidents seeded.
 */

const PARENT_UID = "aaaaaaaa-0000-4000-8000-000000000001";
const CHILD_LONG_NAME_UID = "aaaaaaaa-0000-4000-8000-000000000002";
const CHILD_DELETED_CHECK_UID = "aaaaaaaa-0000-4000-8000-000000000003";
const LONG_CHECK_NAME = "api.acme-staging.io document-storage version (http)";
// Stands in for the check's own historical UID once its name/slug can no
// longer be hydrated (hard-deleted check) — this is what the cell must fall
// back to displaying.
const DELETED_CHECK_UID = "84698cfb-5b01-4fab-b898-13beda200722";

function incident(overrides: Record<string, unknown>) {
  const now = new Date().toISOString();
  return {
    number: 1,
    checkUid: "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
    checkName: "Some check",
    state: "active",
    startedAt: now,
    failureCount: 1,
    relapseCount: 0,
    pagingSuppressed: false,
    ...overrides,
  };
}

const PARENT_INCIDENT = incident({
  uid: PARENT_UID,
  number: 9001,
  checkUid: "bbbbbbbb-0000-4000-8000-000000000000",
  checkName: "Root cause check",
});

const CHILDREN = [
  incident({
    uid: CHILD_LONG_NAME_UID,
    number: 1001,
    checkUid: "dddddddd-0000-4000-8000-000000000002",
    checkName: LONG_CHECK_NAME,
    pagingSuppressed: false,
  }),
  incident({
    uid: CHILD_DELETED_CHECK_UID,
    number: 1002,
    checkUid: DELETED_CHECK_UID,
    checkName: undefined,
    checkSlug: undefined,
    state: "resolved",
    pagingSuppressed: true,
  }),
];

const CHILD_DETAILS: Record<string, unknown> = {
  [CHILD_LONG_NAME_UID]: incident({ uid: CHILD_LONG_NAME_UID, checkName: LONG_CHECK_NAME }),
  [CHILD_DELETED_CHECK_UID]: incident({
    uid: CHILD_DELETED_CHECK_UID,
    checkUid: DELETED_CHECK_UID,
    checkName: undefined,
    checkSlug: undefined,
    state: "resolved",
  }),
};

test.describe("Blast radius table (mobile)", () => {
  test("shows check names, truncates without page overflow, and both links work at 375px", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.setViewportSize({ width: 375, height: 812 });

    await page.route("**/api/v1/orgs/*/incidents**", async (route) => {
      const url = new URL(route.request().url());
      const parts = url.pathname.split("/");
      const idx = parts.indexOf("incidents");
      const uid = parts[idx + 1];
      const sub = parts[idx + 2];

      // List endpoint: /incidents?...
      if (!uid) {
        if (url.searchParams.get("causedByIncidentUid") === PARENT_UID) {
          return route.fulfill({
            status: 200,
            contentType: "application/json",
            body: JSON.stringify({
              data: CHILDREN,
              pagination: { total: CHILDREN.length, size: CHILDREN.length },
            }),
          });
        }
        return route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({ data: [], pagination: { total: 0, size: 0 } }),
        });
      }

      if (sub === "notifications") {
        return route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({ data: [] }),
        });
      }

      if (uid === PARENT_UID) {
        return route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify(PARENT_INCIDENT),
        });
      }
      if (uid in CHILD_DETAILS) {
        return route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify(CHILD_DETAILS[uid]),
        });
      }
      return route.continue();
    });

    await page.goto(`orgs/test/incidents/${PARENT_UID}`);
    await page.waitForLoadState("networkidle");

    const card = page.getByTestId("blast-radius-card");
    await expect(card).toBeVisible();

    const rows = page.getByTestId("blast-radius-row");
    await expect(rows).toHaveCount(2);

    // Criterion: a real check name is shown, not a raw UUID.
    const longNameLink = rows.nth(0).getByTestId("blast-radius-check-link");
    await expect(longNameLink).toHaveText(LONG_CHECK_NAME);
    await expect(longNameLink).toHaveAttribute("title", LONG_CHECK_NAME);

    // Criterion: a UUID is shown only when the check no longer exists.
    const deletedCheckLink = rows.nth(1).getByTestId("blast-radius-check-link");
    await expect(deletedCheckLink).toHaveText(DELETED_CHECK_UID);

    // Criterion: paging badge/column still present for the suppressed child.
    await expect(rows.nth(1).getByText("rolled up")).toBeVisible();

    // Criterion: no column header is visually truncated — assert the exact
    // localized text (no defaultValue fallback) and that every header's
    // right edge sits inside the 375px viewport (nothing clipped off-screen).
    const headers = {
      check: page.getByTestId("blast-radius-header-check"),
      state: page.getByTestId("blast-radius-header-state"),
      paging: page.getByTestId("blast-radius-header-paging"),
    };
    await expect(headers.check).toHaveText("Check");
    await expect(headers.state).toHaveText("State");
    await expect(headers.paging).toHaveText("Paging");
    for (const header of Object.values(headers)) {
      const box = await header.boundingBox();
      expect(box).not.toBeNull();
      expect(box!.x).toBeGreaterThanOrEqual(0);
      expect(box!.x + box!.width).toBeLessThanOrEqual(375);
    }

    // Criterion: the child-incident link is reachable without horizontal
    // scrolling — the page must not overflow horizontally at all, and the
    // link itself must already sit inside the viewport before any click.
    const pageOverflows = await page.evaluate(
      () => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1,
    );
    expect(pageOverflows).toBe(false);

    const linkBox = await longNameLink.boundingBox();
    expect(linkBox).not.toBeNull();
    expect(linkBox!.x + linkBox!.width).toBeLessThanOrEqual(375);

    // Criterion: clicking a check's name navigates to that child incident.
    await longNameLink.click();
    await page.waitForURL(new RegExp(`/incidents/${CHILD_LONG_NAME_UID}`));

    // Criterion: each row also offers a link to the check's own page — but
    // never a dead one. This is a positive/negative pair; keep both
    // assertions together, since either half alone proves nothing:
    //   - row 0's check was hydrated (checkName present) -> link present.
    //   - row 1's check was NOT hydrated (hard-deleted, name fell back to
    //     its raw UID) -> no link, because checkUid alone (still present as
    //     a historical FK) is not "the check still exists".
    await page.goBack();
    await page.waitForLoadState("networkidle");

    const openCheckLinkLive = rows.nth(0).getByTestId("blast-radius-open-check");
    await expect(openCheckLinkLive).toBeVisible();
    await expect(openCheckLinkLive).toHaveAttribute(
      "href",
      /\/checks\/dddddddd-0000-4000-8000-000000000002/,
    );

    const openCheckLinkDeleted = rows.nth(1).getByTestId("blast-radius-open-check");
    await expect(openCheckLinkDeleted).toHaveCount(0);
  });
});
