import { test, expect } from "./fixtures";

// Deterministically seeded in test mode (server/test/testdata): an active
// incident with a first-failure snapshot, shared with the notifications and
// failure-details E2E specs (incident-notifications.spec.ts,
// incident-failure-details.spec.ts).
const INCIDENT_UID = "00000000-0000-0000-0000-000000000013";

const UTC_ISO_RE = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/;

test.describe("TimeAgo: hover tooltip + click-to-copy", () => {
  test("incidents list: hover shows the local+UTC tooltip, click copies ISO 8601 UTC", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);

    await page.goto("/dash0/orgs/test/incidents");
    await page.waitForLoadState("networkidle");

    const timestamp = page.getByTestId("incident-started-at").first();
    await expect(timestamp).toBeVisible({ timeout: 15000 });

    // Affordance (Proposal item 5): a dotted underline hints the timestamp
    // is interactive, since there's no other visual cue in a dense table row.
    await expect(timestamp).toHaveClass(/decoration-dotted/);
    await expect(timestamp).toHaveClass(/cursor-pointer/);

    // Hover reveals the absolute time: local time and UTC (Proposal item 2).
    await timestamp.hover();
    const tooltip = page.getByTestId("time-ago-tooltip");
    await expect(tooltip).toBeVisible();
    await expect(tooltip).toContainText("(local)");
    await expect(tooltip).toContainText(/\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z/);

    // Click copies the UTC timestamp as ISO 8601 (Proposal item 3) and does
    // NOT navigate the row away from the list (Proposal item 4 — the
    // click handler must preventDefault/stopPropagation).
    const urlBefore = page.url();
    await timestamp.click();
    expect(page.url()).toBe(urlBefore);

    const clipboardText = await page.evaluate(() => navigator.clipboard.readText());
    expect(clipboardText).toMatch(UTC_ISO_RE);

    // Visible copy feedback replaces the tooltip body (Proposal item 3).
    await expect(tooltip).toContainText("Copied");
    await expect(tooltip).toContainText(clipboardText);
  });

  test("incident detail: timeline uses the inline variant with absolute time always visible", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);

    await page.goto(`/dash0/orgs/test/incidents/${INCIDENT_UID}`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByText("Incident Details")).toBeVisible();

    // Proposal item 7: the inline variant shows "HH:MM:SS UTC · Nx ago"
    // without needing a hover, so several timestamps can be compared at a
    // glance.
    const timelineTime = page.getByTestId("incident-timeline-time").first();
    await expect(timelineTime).toBeVisible();
    await expect(timelineTime).toContainText(/UTC/);
    await expect(timelineTime).toContainText(/ago|just now/);

    // Click-to-copy still applies to the inline variant.
    await timelineTime.click();
    const clipboardText = await page.evaluate(() => navigator.clipboard.readText());
    expect(clipboardText).toMatch(UTC_ISO_RE);
  });

  test("incident detail page stays usable on a mobile viewport", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.setViewportSize({ width: 375, height: 812 });

    await page.goto(`/dash0/orgs/test/incidents/${INCIDENT_UID}`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByText("Incident Details")).toBeVisible();

    const timelineTime = page.getByTestId("incident-timeline-time").first();
    await expect(timelineTime).toBeVisible();

    const hasOverflow = await page.evaluate(
      () => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1,
    );
    expect(hasOverflow).toBe(false);
  });
});
