import { test, expect } from "./fixtures";

// Coverage for spec 2026-08-21-01: the incident screenshot card, the first
// consumer of the generic attachments rail.
//
// Deterministically seeded in test mode (server/test/testdata/testdata.go,
// createTestIncidentScreenshot): a down browser check whose active incident
// carries a REAL screenshot attachment — bytes written through the normal
// attachment path, so the signed download URL serves a decodable PNG.
//
// Incident 17 (createTestFailureResponseCaptures) is the negative case: it has
// a failure-response capture but no attachment, so its page must render the
// probe-response card and NOT this one.
const SHOT_INCIDENT = "00000000-0000-0000-0000-000000000026";
const NO_SHOT_INCIDENT = "00000000-0000-0000-0000-000000000017";
const SHOT_CHECK = "00000000-0000-0000-0000-000000000025";

test.describe("Incident screenshot attachment", () => {
  test("renders the image with a captured-at + region caption", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto(`/dash0/orgs/test/incidents/${SHOT_INCIDENT}`);
    await page.waitForLoadState("networkidle");

    const card = page.getByTestId("incident-screenshot-card");
    await expect(card).toBeVisible();

    // The card must never claim to be the failure frame itself.
    await expect(card).toContainText("Screenshot at failure detection");
    await expect(card).toContainText("Captured a moment AFTER detection");

    const caption = page.getByTestId("incident-screenshot-caption");
    await expect(caption).toBeVisible();
    await expect(caption).toContainText("eu-west");
    await expect(caption).toContainText("shortly after failure detection");
    await expect(caption).not.toContainText("an unknown region");
    await expect(caption).not.toContainText("at an unknown time");

    // The signed URL is relative and points at the public files route.
    const img = page.getByTestId("incident-screenshot-image");
    await expect(img).toBeVisible();

    const src = await img.getAttribute("src");
    expect(src).toContain("/pub/files/");
    expect(src).toContain("sig=");
    expect(src).toContain("exp=");

    // The bytes really are served and really are an image: a broken src would
    // leave naturalWidth at 0, which is the failure mode a "visible" assertion
    // alone would miss.
    await expect
      .poll(async () =>
        img.evaluate((el) => (el as HTMLImageElement).naturalWidth),
      )
      .toBeGreaterThan(0);
  });

  test("the card is absent entirely when the incident has no attachment", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Positive control first: on the seeded incident the card IS rendered, so
    // "absent" below is evidence of the guard rather than of a wrong test id.
    await page.goto(`/dash0/orgs/test/incidents/${SHOT_INCIDENT}`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("incident-screenshot-card")).toBeVisible();

    await page.goto(`/dash0/orgs/test/incidents/${NO_SHOT_INCIDENT}`);
    await page.waitForLoadState("networkidle");

    // The page really did render — its sibling cards are there; only the
    // screenshot card is withheld.
    await expect(page.getByTestId("probe-response-card")).toBeVisible();
    await expect(page.getByTestId("incident-screenshot-card")).toHaveCount(0);
  });

  test("the card is usable on mobile", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    await page.setViewportSize({ width: 375, height: 812 });
    await page.goto(`/dash0/orgs/test/incidents/${SHOT_INCIDENT}`);
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("incident-screenshot-card")).toBeVisible();
    await expect(page.getByTestId("incident-screenshot-image")).toBeVisible();

    const hasOverflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth + 1,
    );
    expect(hasOverflow).toBe(false);
  });

  test("the browser check form round-trips the screenshot opt-in", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto(`/dash0/orgs/test/checks/${SHOT_CHECK}/edit`);
    await page.waitForLoadState("networkidle");

    const checkbox = page.getByTestId("check-browser-screenshot-checkbox");
    await expect(checkbox).toBeVisible();
    // The fixture check has the option ON, so an unchecked box here would mean
    // the form silently drops a stored setting on every edit.
    await expect(checkbox).toBeChecked();
  });
});
