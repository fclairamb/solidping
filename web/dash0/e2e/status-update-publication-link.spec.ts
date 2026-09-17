import { test, expect, type Page } from "./fixtures";

/**
 * The publication link on the Updates & notices list (spec 2026-09-16-14).
 *
 * The list is a flat, cross-page view of every narrative post in the org, and
 * before this spec it flattened away the one piece of context that matters:
 * whether a post stands alone or is one entry in the thread of a published
 * incident. The link is that context.
 *
 * Mocked, not seeded: a threaded update needs a publication with an update
 * hanging off it, which the dev seed does not produce and which no probe can
 * be made to produce on demand. The standalone row is the negative control —
 * `incidentPublicationUid` is nullable, and a maintenance notice that grew a
 * dead link would be worse than no link at all.
 */

const PAGE_UID = "e2e-linkpage-uid";
const PUB_UID = "e2e-linkpub-uid";
const THREADED_TITLE = "Investigating elevated error rates";
const STANDALONE_TITLE = "Planned database maintenance";

async function mockUpdates(page: Page): Promise<void> {
  await page.route("**/api/v1/orgs/*/status-pages", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: [
          {
            uid: PAGE_UID,
            name: "Acme Status",
            slug: "acme",
            visibility: "public",
            isDefault: true,
            enabled: true,
          },
        ],
      }),
    }),
  );

  await page.route("**/api/v1/orgs/*/status-updates*", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: [
          {
            uid: "e2e-threaded-update",
            statusPageUid: PAGE_UID,
            incidentPublicationUid: PUB_UID,
            title: THREADED_TITLE,
            bodyMarkdown: "We are looking into it.",
            kind: "investigating",
            publishedAt: "2026-09-16T09:00:00Z",
            authorUid: "e2e-author",
            createdAt: "2026-09-16T09:00:00Z",
            updatedAt: "2026-09-16T09:00:00Z",
          },
          {
            // No incidentPublicationUid: a standalone maintenance notice.
            uid: "e2e-standalone-update",
            statusPageUid: PAGE_UID,
            title: STANDALONE_TITLE,
            bodyMarkdown: "We will be upgrading the primary database.",
            kind: "maintenance",
            publishedAt: "2026-09-16T08:00:00Z",
            authorUid: "e2e-author",
            createdAt: "2026-09-16T08:00:00Z",
            updatedAt: "2026-09-16T08:00:00Z",
          },
        ],
      }),
    }),
  );
}

test.describe("Updates & notices — publication link", () => {
  test("links a threaded update to its publication, and leaves a standalone one alone", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await mockUpdates(page);

    await page.goto("orgs/test/status-updates");
    await page.waitForLoadState("networkidle");

    // The page says what it is before anything else does.
    await expect(
      page.getByRole("heading", { name: "Updates & notices" }),
    ).toBeVisible();
    await expect(
      page.getByText(
        /Narrative posts published to your status pages/,
      ),
    ).toBeVisible();

    const threadedRow = page
      .getByTestId("status-update-row")
      .filter({ hasText: THREADED_TITLE });
    await expect(threadedRow).toBeVisible();

    const publicationLink = threadedRow.getByTestId(
      "status-update-row-publication",
    );
    await expect(publicationLink).toBeVisible();
    await expect(publicationLink).toHaveAttribute(
      "href",
      new RegExp(`/status-pages/${PAGE_UID}/incidents/${PUB_UID}$`),
    );

    // Negative control: the standalone maintenance notice renders in the same
    // table, from the same component, and must carry no link at all.
    const standaloneRow = page
      .getByTestId("status-update-row")
      .filter({ hasText: STANDALONE_TITLE });
    await expect(standaloneRow).toBeVisible();
    await expect(
      standaloneRow.getByTestId("status-update-row-publication"),
    ).toHaveCount(0);

    // The link actually opens the publication editor, not a 404 route.
    await publicationLink.click();
    await page.waitForURL(
      new RegExp(`/status-pages/${PAGE_UID}/incidents/${PUB_UID}$`),
      { timeout: 10000 },
    );
  });
});
