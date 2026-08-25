import { test, expect } from "./fixtures";

// The dashboard used to show a bare "Acked" badge and an "Acknowledged"
// timeline entry with nothing but a timestamp — so the one question an
// operator opens an acknowledged incident to answer ("who has this?") was the
// one thing the page would not tell them.
test.describe("Incident acknowledgment attribution", () => {
  // Deterministically seeded in test mode (server/test/testdata,
  // createTestFailureResponseCaptures): an incident that STAYS active for the
  // whole run. The notification fixture's incident (…0013) is not usable here
  // — its check really runs, so the worker resolves it a minute or two in and
  // a resolved incident has no Acknowledge button at all.
  const incidentUid = "00000000-0000-0000-0000-000000000017";

  // Every test leaves the shared fixture unacknowledged again: other specs
  // read this same incident, and an ack that outlives its test is exactly the
  // kind of order-dependent state that makes a suite flaky.
  async function clearAck(page: import("@playwright/test").Page) {
    const unack = page.getByRole("button", { name: "Unacknowledge" });
    if (await unack.isVisible().catch(() => false)) {
      await unack.click();
      await expect(
        page.getByRole("button", { name: "Acknowledge" }),
      ).toBeVisible({ timeout: 10000 });
    }
  }

  test("acknowledging names the acker in the badge and the timeline", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto(`/dash0/orgs/test/incidents/${incidentUid}`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByText("Incident Details")).toBeVisible();

    // Start from a clean slate: a previous run may have left it acknowledged.
    await clearAck(page);

    await page.getByRole("button", { name: "Acknowledge" }).click();

    // The badge names the acker rather than saying only "Acked".
    const badge = page.getByTestId("incident-acked-badge");
    await expect(badge).toBeVisible({ timeout: 10000 });
    await expect(badge).toContainText("Acked by");

    // …and so does the timeline entry, which previously carried a bare
    // timestamp.
    const attribution = page.getByTestId("incident-timeline-acknowledged-by");
    await expect(attribution).toBeVisible();
    await expect(attribution).toContainText("by");

    await page.screenshot({
      path: "test-results/screenshots/incident-ack-actor.png",
      fullPage: true,
    });

    await clearAck(page);
  });

  test("the attribution disappears again when the ack is withdrawn", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto(`/dash0/orgs/test/incidents/${incidentUid}`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByText("Incident Details")).toBeVisible();

    const ack = page.getByRole("button", { name: "Acknowledge" });
    if (await ack.isVisible().catch(() => false)) {
      await ack.click();
    }

    await expect(page.getByTestId("incident-acked-badge")).toBeVisible({
      timeout: 10000,
    });

    await page.getByRole("button", { name: "Unacknowledge" }).click();

    // An incident nobody has taken must not keep naming anybody.
    await expect(page.getByTestId("incident-acked-badge")).toBeHidden({
      timeout: 10000,
    });
    await expect(
      page.getByTestId("incident-timeline-acknowledged-by"),
    ).toBeHidden();
  });

  test("the attribution stays readable on a mobile viewport", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.setViewportSize({ width: 375, height: 812 });

    await page.goto(`/dash0/orgs/test/incidents/${incidentUid}`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByText("Incident Details")).toBeVisible();

    const ack = page.getByRole("button", { name: "Acknowledge" });
    if (await ack.isVisible().catch(() => false)) {
      await ack.click();
    }

    await expect(page.getByTestId("incident-acked-badge")).toBeVisible({
      timeout: 10000,
    });

    const hasOverflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth + 1,
    );
    expect(hasOverflow).toBe(false);

    await clearAck(page);
  });
});
