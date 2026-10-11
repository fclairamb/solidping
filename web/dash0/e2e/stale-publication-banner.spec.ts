import { test, expect, type Page } from "./fixtures";

/**
 * The stale-publication warning on the checks list (spec 2026-09-02-05).
 *
 * The state it reports is invisible to every other surface in dash0: the
 * checks are all up, the incident behind the public entry is resolved, and the
 * only thing still claiming trouble is the status page nobody in the office is
 * looking at. The reported case ran for ten days like that.
 *
 * Mocked, not seeded: the setup needs a publication whose linked incident has
 * already resolved, which the dev seed does not produce and which a real probe
 * cannot be made to produce on demand.
 *
 * The negative cases (incident still live, server ignoring `?stale=true`) are
 * covered by vitest: src/api/hooks.stale-publications.test.tsx and
 * src/lib/stale-publications.test.ts.
 */

const PAGE_UID = "e2e-stale-page";
const PUB_UID = "e2e-stale-pub";

function publication() {
  return {
    uid: PUB_UID,
    statusPageUid: PAGE_UID,
    incidentUid: "e2e-stale-incident",
    title: "Some services are experiencing issues",
    state: "identified",
    severity: "minor",
    autoCreated: false,
    humanTouched: false,
    publishedAt: "2026-08-23T12:17:51Z",
    createdAt: "2026-08-23T12:17:51Z",
    updatedAt: "2026-08-23T12:18:21Z",
    stale: true,
  };
}

/**
 * Mocks the two endpoints the banner reads: one public page carrying one open
 * publication whose incident has resolved.
 */
async function mockStatusPages(page: Page): Promise<void> {
  await page.route("**/api/v1/orgs/*/status-pages", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: [
          {
            uid: PAGE_UID,
            name: "E2E Stale Page",
            slug: "e2e-stale",
            visibility: "public",
            isDefault: false,
            enabled: true,
          },
        ],
      }),
    }),
  );

  await page.route(
    `**/api/v1/orgs/*/status-pages/${PAGE_UID}/incidents*`,
    (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ data: [publication()] }),
      }),
  );
}

test.describe("Checks list — stale status-page publication", () => {
  test("warns when a public entry outlived the incident it tracks", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await mockStatusPages(page);

    await page.goto("orgs/test/checks");

    const banner = page.getByTestId("stale-publications-banner");
    await expect(banner).toBeVisible();
    await expect(banner).toContainText("E2E Stale Page");

    // The link is the remedy: one click to the entry that has to be closed.
    const link = page.getByTestId("stale-publication-link");
    await expect(link).toHaveAttribute(
      "href",
      new RegExp(`/status-pages/${PAGE_UID}/incidents/${PUB_UID}$`),
    );
  });
});
