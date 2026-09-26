import { API_BASE, DASH_BASE, expect, getAuthToken, test, uniqueStamp, type Page } from "./fixtures";

// Coverage for spec 2026-09-25-34: the check page's Screenshots card.
//
// Seeded in test mode (server/test/testdata/testdata.go,
// createTestIncidentScreenshot): the browser check "shot-check" carries an
// incident screenshot from eu-west (the newest) and an older check-scoped
// capture from us-east, so the card has a latest capture with an incident link
// and one thumbnail in the strip.
//
// "Capture now" is driven for real up to the API: the side-car has no browser
// engine, so the capture itself is never produced here (the forced capture is
// covered by the Go tests in checkbrowser/checkjs/incidents/checkjobsvc). What
// this pins is the button, its pending state, and the rate-limit message.
const SHOT_CHECK = "00000000-0000-0000-0000-000000000025";
const SHOT_INCIDENT = "00000000-0000-0000-0000-000000000026";

async function createCheck(
  page: Page,
  token: string,
  data: Record<string, unknown>,
): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
    headers: { Authorization: `Bearer ${token}` },
    data,
  });
  expect(resp.status(), await resp.text()).toBe(201);

  return ((await resp.json()) as { uid: string }).uid;
}

async function deleteCheck(page: Page, token: string, uid: string): Promise<void> {
  await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}

test.describe("Check page screenshots", () => {
  test("shows the latest capture with its region, incident link and older captures", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto(`${DASH_BASE}/orgs/test/checks/${SHOT_CHECK}`);

    const card = page.getByTestId("check-screenshots-card");
    await expect(card).toBeVisible();
    await expect(card).toContainText("Screenshots");

    await expect(page.getByTestId("check-screenshot-region")).toHaveText("eu-west");
    await expect(page.getByTestId("check-screenshot-trigger")).toHaveText("Opened an incident");

    const img = page.getByTestId("check-screenshot-image");
    const src = await img.getAttribute("src");
    expect(src).toContain("/pub/files/");
    expect(src).toContain("sig=");
    // The signed URL really serves a decodable image (a broken src leaves
    // naturalWidth at 0, which "visible" alone would not catch).
    await expect
      .poll(() => img.evaluate((el) => (el as HTMLImageElement).naturalWidth))
      .toBeGreaterThan(0);

    // The older, check-scoped capture sits in the strip.
    await expect(page.getByTestId("check-screenshots-thumbnail")).toHaveCount(1);
    await expect(page.getByTestId("check-screenshots-thumbnail")).toHaveAttribute(
      "title",
      /us-east/,
    );

    const incidentLink = page.getByTestId("check-screenshot-incident-link");
    await expect(incidentLink).toHaveAttribute("href", new RegExp(`/incidents/${SHOT_INCIDENT}$`));
    await incidentLink.click();
    await expect(page).toHaveURL(new RegExp(`/incidents/${SHOT_INCIDENT}$`));
    await expect(page.getByTestId("incident-screenshot-card")).toBeVisible();
  });

  test("shows the empty state on a browser check with no capture", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const uid = await createCheck(page, token, {
      name: `E2E screenshots empty ${uniqueStamp()}`,
      type: "browser",
      period: "01:00:00",
      config: { url: "https://acme.com/empty" },
    });

    try {
      await page.goto(`${DASH_BASE}/orgs/test/checks/${uid}`);

      const empty = page.getByTestId("check-screenshots-empty");
      await expect(empty).toBeVisible();
      await expect(empty).toContainText("No screenshot yet. Captures are taken when a run fails.");

      // The screenshot option is off, so the card links to it.
      const enable = page.getByTestId("check-screenshots-enable-link");
      await expect(enable).toHaveAttribute("href", /section=browser-screenshot/);
      await enable.click();
      await expect(page.getByTestId("check-browser-screenshot-checkbox")).toBeVisible();
      await expect(page.getByTestId("check-browser-screenshot-checkbox")).not.toBeChecked();
    } finally {
      await deleteCheck(page, token, uid);
    }
  });

  test("renders nothing for an http check", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const uid = await createCheck(page, token, {
      name: `E2E screenshots http ${uniqueStamp()}`,
      type: "http",
      config: { url: "https://acme.com/health" },
    });

    try {
      // Positive control: the card renders on the seeded browser check, so the
      // absence below is the type guard and not a wrong test id.
      await page.goto(`${DASH_BASE}/orgs/test/checks/${SHOT_CHECK}`);
      await expect(page.getByTestId("check-screenshots-card")).toBeVisible();

      await page.goto(`${DASH_BASE}/orgs/test/checks/${uid}`);
      await expect(page.getByTestId("check-detail-header")).toBeVisible();
      await expect(page.getByTestId("check-screenshots-card")).toHaveCount(0);
    } finally {
      await deleteCheck(page, token, uid);
    }
  });

  test("Capture now schedules a run, then is rate limited", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const uid = await createCheck(page, token, {
      name: `E2E capture now ${uniqueStamp()}`,
      type: "browser",
      period: "01:00:00",
      config: { url: "https://acme.com/capture" },
    });

    try {
      await page.goto(`${DASH_BASE}/orgs/test/checks/${uid}`);

      const button = page.getByTestId("check-screenshots-capture-now");
      await expect(button).toBeEnabled();

      const accepted = page.waitForResponse(
        (resp) =>
          resp.url().endsWith(`/checks/${uid}/screenshots/capture`) &&
          resp.request().method() === "POST",
      );
      await button.click();
      expect((await accepted).status()).toBe(202);

      await expect(page.getByText(/Capture requested/)).toBeVisible();
      await expect(page.getByTestId("check-screenshots-pending")).toBeVisible();
      await expect(button).toBeDisabled();

      // A second request inside the minute is refused, with the delay.
      const again = await page.request.post(
        `${API_BASE}/api/v1/orgs/test/checks/${uid}/screenshots/capture`,
        { headers: { Authorization: `Bearer ${token}` } },
      );
      expect(again.status()).toBe(429);
      expect(Number(again.headers()["retry-after"])).toBeGreaterThan(0);

      // And the UI says so: a fresh page has no pending state, the click goes
      // through to the server, and the refusal becomes a readable message.
      await page.reload();
      await page.getByTestId("check-screenshots-capture-now").click();
      await expect(page.getByText(/Capture now is rate limited\. Try again in \d+ s\./)).toBeVisible();
    } finally {
      await deleteCheck(page, token, uid);
    }
  });

  test("the card is usable on mobile", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    await page.setViewportSize({ width: 375, height: 812 });
    await page.goto(`${DASH_BASE}/orgs/test/checks/${SHOT_CHECK}`);

    const card = page.getByTestId("check-screenshots-card");
    await expect(card).toBeVisible();

    // At 375px the card sits below the fold once the check has history; the
    // image is lazy, so bring the card into view before judging it.
    await card.scrollIntoViewIfNeeded();

    const img = page.getByTestId("check-screenshot-image");
    await img.scrollIntoViewIfNeeded();
    await expect(img).toBeVisible();
    // Loaded for real, not just a laid-out box.
    await expect
      .poll(() => img.evaluate((el) => (el as HTMLImageElement).naturalWidth))
      .toBeGreaterThan(0);
    await expect(page.getByTestId("check-screenshots-capture-now")).toBeVisible();

    const hasOverflow = await page.evaluate(
      () => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1,
    );
    expect(hasOverflow).toBe(false);
  });
});
