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
 */

const PAGE_UID = "e2e-stale-page";
const PUB_UID = "e2e-stale-pub";

function publication(overrides: Record<string, unknown> = {}) {
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
    ...overrides,
  };
}

/**
 * Mocks the two endpoints the banner reads. `stale` decides what the
 * `?stale=true` slice contains — that filter is the whole contract between the
 * server and this banner, so honouring it here is what makes the negative
 * control meaningful rather than a mock that simply returns nothing.
 */
async function mockStatusPages(
  page: Page,
  opts: { stale: boolean },
): Promise<void> {
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
    (route) => {
      const params = new URL(route.request().url()).searchParams;
      const staleOnly = params.get("stale") === "true";

      // In both scenarios the page carries ONE open publication. The only
      // difference is whether the incident behind it has resolved — which is
      // exactly what `?stale=true` selects on.
      const rows = staleOnly
        ? opts.stale
          ? [publication()]
          : []
        : [publication({ stale: opts.stale })];

      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ data: rows }),
      });
    },
  );
}

test.describe("Checks list — stale status-page publication", () => {
  test("warns when a public entry outlived the incident it tracks", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await mockStatusPages(page, { stale: true });

    await page.goto("orgs/test/checks");
    await page.waitForLoadState("networkidle");

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

  test("stays silent while the linked incident is still live", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await mockStatusPages(page, { stale: false });

    await page.goto("orgs/test/checks");
    await page.waitForLoadState("networkidle");

    // The checks list itself rendered — without this the assertion below would
    // pass on a page that never loaded at all.
    await expect(page.getByTestId("checks-search-input")).toBeVisible();
    await expect(page.getByTestId("stale-publications-banner")).toHaveCount(0);
  });
});
