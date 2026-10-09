import { test, expect, DASH_BASE } from "./fixtures";

// Spec 2026-10-08-02: deleting a check resolves its active incidents with
// resolution_type "check_deleted". The incident page must say why it closed,
// rather than reading like a recovery.
test.describe("Incident closed by check deletion", () => {
  // The seeded incident that stays active for the whole run (see
  // incident-ack-actor.spec.ts). Deleting its check would break every other
  // spec reading it, so the API response is patched instead: the server side
  // of the resolution is covered by the Go tests.
  const incidentUid = "00000000-0000-0000-0000-000000000017";

  async function patchIncident(
    page: import("@playwright/test").Page,
    resolutionType: string,
  ) {
    await page.route(
      `**/api/v1/orgs/test/incidents/${incidentUid}`,
      async (route) => {
        if (route.request().method() !== "GET") {
          await route.continue();

          return;
        }

        const response = await route.fetch();
        const body = (await response.json()) as Record<string, unknown>;

        await route.fulfill({
          response,
          json: {
            ...body,
            state: "resolved",
            resolvedAt: new Date().toISOString(),
            resolutionType,
          },
        });
      },
    );
  }

  test("the timeline says the check was deleted", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await patchIncident(page, "check_deleted");

    await page.goto(`${DASH_BASE}/orgs/test/incidents/${incidentUid}`);
    await expect(page.getByText("Incident Details")).toBeVisible();

    const reason = page.getByTestId("incident-timeline-resolved-reason");
    await expect(reason).toBeVisible({ timeout: 10000 });
    await expect(reason).toHaveText("Closed because the check was deleted");

    await page.screenshot({
      path: "test-results/screenshots/incident-check-deleted.png",
      fullPage: true,
    });
  });

  test("an ordinary resolution carries no deletion reason", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await patchIncident(page, "auto");

    await page.goto(`${DASH_BASE}/orgs/test/incidents/${incidentUid}`);
    await expect(page.getByText("Incident Details")).toBeVisible();
    await expect(page.getByText("Resolved", { exact: true }).first()).toBeVisible({
      timeout: 10000,
    });

    await expect(
      page.getByTestId("incident-timeline-resolved-reason"),
    ).toHaveCount(0);
  });
});
