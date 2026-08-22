import { test, expect } from "./fixtures";

// Coverage for spec 2026-08-21-01: the screenshot card on the incident detail
// page, which renders the browser check's failure capture from the generic
// attachments rail.
//
// Deterministically seeded in test mode (server/test/testdata/testdata.go,
// createTestIncidentScreenshot): a down browser check with an active incident
// carrying a real PNG attachment, written through the files SERVICE so the
// bytes genuinely exist behind the storage backend and the signed download URL
// resolves. Incident 13 (createTestIncidentNotification) has no attachment and
// is the negative case.
const SCREENSHOT_INCIDENT = "00000000-0000-0000-0000-000000000026";
const NO_ATTACHMENT_INCIDENT = "00000000-0000-0000-0000-000000000013";

test.describe("Incident screenshot attachment", () => {
  test("renders the image with a capture timestamp and region caption", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto(`/dash0/orgs/test/incidents/${SCREENSHOT_INCIDENT}`);
    await page.waitForLoadState("networkidle");

    const card = page.getByTestId("probe-screenshot-card");
    await expect(card).toBeVisible();
    await expect(card).toContainText("Screenshot at failure");

    const image = page.getByTestId("probe-screenshot-image");
    await expect(image).toBeVisible();

    // The signed download URL must actually resolve: a broken image still
    // renders an <img>, so the assertion that matters is that the browser
    // DECODED it. naturalWidth is 0 for an image that failed to load.
    await expect
      .poll(async () =>
        image.evaluate((el) => (el as HTMLImageElement).naturalWidth),
      )
      .toBeGreaterThan(0);

    // The link is a signed /pub/files/ URL, not a stable path — it is minted
    // per response and expires.
    const href = await page
      .getByTestId("probe-screenshot-link")
      .getAttribute("href");
    expect(href).toContain("/pub/files/");
    expect(href).toContain("sig=");
    expect(href).toContain("exp=");

    // The caption carries the capture timestamp and the probing region, and is
    // explicit that this is AFTER detection — never "the failure frame".
    const caption = page.getByTestId("probe-screenshot-caption");
    await expect(caption).toContainText("Captured");
    await expect(caption).toContainText("shortly after failure detection");
    await expect(caption).toContainText("eu");

    // And the card says plainly that this is team-only evidence.
    await expect(card).toContainText("never shown on a status page");
  });

  test("the card is absent entirely when the incident has no attachment", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Positive control first: on the seeded incident the card IS there, so the
    // absence below is a property of the incident and not of a broken page.
    await page.goto(`/dash0/orgs/test/incidents/${SCREENSHOT_INCIDENT}`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("probe-screenshot-card")).toBeVisible();

    await page.goto(`/dash0/orgs/test/incidents/${NO_ATTACHMENT_INCIDENT}`);
    await page.waitForLoadState("networkidle");

    // The page really did render (its sibling failure card is there) — it is
    // only the screenshot card that is withheld.
    await expect(page.getByTestId("failure-details-card")).toBeVisible();
    await expect(page.getByTestId("probe-screenshot-card")).toHaveCount(0);
  });

  test("the card is usable on mobile", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    await page.setViewportSize({ width: 375, height: 812 });
    await page.goto(`/dash0/orgs/test/incidents/${SCREENSHOT_INCIDENT}`);
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("probe-screenshot-card")).toBeVisible();
    await expect(page.getByTestId("probe-screenshot-image")).toBeVisible();

    // A screenshot is the widest thing on this page; it must scale rather
    // than push the document into a horizontal scroll.
    const hasOverflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth + 1,
    );
    expect(hasOverflow).toBe(false);
  });
});
