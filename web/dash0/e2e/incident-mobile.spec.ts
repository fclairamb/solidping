import { test, expect, DASH_BASE, type Page } from "./fixtures";

// Every card of the incident detail page must stay usable on a phone: the card
// renders, and the page itself never scrolls sideways (wide content lives in
// its own overflow container — frontend convention, design reference).
//
// One test at 375px walks the deterministically seeded incidents
// (server/test/testdata/testdata.go) that carry each card, instead of one test
// per card file paying its own page context. What each card renders at full
// width stays in its own spec (incident-failure-details, incident-timestamps,
// incident-comments, incident-probe-response, incident-ack-actor,
// incident-screenshot, incident-traceroute).

// createTestIncidentNotification: active incident with a first-failure snapshot.
const FAILURE_INCIDENT = "00000000-0000-0000-0000-000000000013";
// createTestFailureResponseCaptures: a text capture, and an incident that stays
// active for the whole run (so it can be acknowledged).
const CAPTURE_INCIDENT = "00000000-0000-0000-0000-000000000017";
// createTestIncidentScreenshot: a real screenshot attachment.
const SCREENSHOT_INCIDENT = "00000000-0000-0000-0000-000000000026";
// createTestIncidentTraceroute: a real traceroute attachment.
const TRACE_INCIDENT = "00000000-0000-0000-0000-000000000028";

async function expectNoHorizontalOverflow(page: Page, what: string) {
  const hasOverflow = await page.evaluate(
    () =>
      document.documentElement.scrollWidth >
      document.documentElement.clientWidth + 1,
  );
  expect(hasOverflow, `${what} makes the page scroll sideways at 375px`).toBe(false);
}

test.describe("Incident detail on a phone", () => {
  test("every incident card is usable at 375px with no horizontal page scroll", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.setViewportSize({ width: 375, height: 812 });

    // --- First-failure panel, timeline timestamps and comment composer.
    await page.goto(`${DASH_BASE}/orgs/test/incidents/${FAILURE_INCIDENT}`);
    await expect(page.getByText("Incident Details")).toBeVisible();

    await expect(page.getByTestId("failure-details-card")).toBeVisible();
    await expect(page.getByTestId("incident-timeline-time").first()).toBeVisible();

    const composer = page.getByTestId("comment-input");
    await composer.scrollIntoViewIfNeeded();
    await expect(composer).toBeVisible();

    await expectNoHorizontalOverflow(page, "the failure details, timeline or comments");

    // --- Probe-response capture, then the acknowledgment attribution.
    await page.goto(`${DASH_BASE}/orgs/test/incidents/${CAPTURE_INCIDENT}`);
    await expect(page.getByText("Incident Details")).toBeVisible();

    await expect(page.getByTestId("probe-response-card")).toBeVisible();
    await expect(page.getByTestId("probe-response-headers-table")).toBeVisible();
    await expectNoHorizontalOverflow(page, "the probe-response card");

    const ack = page.getByRole("button", { name: "Acknowledge" });
    if (await ack.isVisible().catch(() => false)) {
      await ack.click();
    }
    await expect(page.getByTestId("incident-acked-badge")).toBeVisible({
      timeout: 10000,
    });
    await expectNoHorizontalOverflow(page, "the acknowledgment attribution");

    // Leave the shared fixture unacknowledged again: other specs read this
    // same incident (see incident-ack-actor.spec.ts).
    const unack = page.getByRole("button", { name: "Unacknowledge" });
    if (await unack.isVisible().catch(() => false)) {
      await unack.click();
      await expect(page.getByRole("button", { name: "Acknowledge" })).toBeVisible({
        timeout: 10000,
      });
    }

    // --- Screenshot attachment.
    await page.goto(`${DASH_BASE}/orgs/test/incidents/${SCREENSHOT_INCIDENT}`);
    await expect(page.getByTestId("incident-screenshot-card")).toBeVisible();
    const image = page.getByTestId("incident-screenshot-image");
    await expect(image).toBeVisible();
    // Measure once the image has its real size: an unconstrained screenshot
    // only widens the page after it has decoded.
    await expect
      .poll(() => image.evaluate((el) => (el as HTMLImageElement).naturalWidth))
      .toBeGreaterThan(0);
    await expectNoHorizontalOverflow(page, "the screenshot card");

    // --- Traceroute hop table: scrolls inside itself, not the page.
    await page.goto(`${DASH_BASE}/orgs/test/incidents/${TRACE_INCIDENT}`);
    await expect(page.getByTestId("incident-traceroute-table")).toBeVisible();
    await expectNoHorizontalOverflow(page, "the traceroute hop table");
  });
});
