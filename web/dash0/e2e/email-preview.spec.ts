import { test, expect, API_BASE } from "./fixtures";

/**
 * The dev-only email template catalog (/orgs/test/test/emails).
 *
 * The page and its backing endpoint both only exist when the server runs with
 * SP_RUNMODE=test — which is exactly how the E2E suite runs it, so the tab is
 * reachable here.
 */
test.describe("Email template preview", () => {
  test("lists every previewable template and renders one", async ({
    authenticatedPage: page,
  }) => {
    // The index endpoint is the page's data source; assert its REST shape
    // directly so a UI failure is distinguishable from an API failure.
    const indexResp = await page.request.get(
      `${API_BASE}/api/mgmt/email-preview`,
    );
    expect(indexResp.status()).toBe(200);

    const body = await indexResp.json();
    expect(Array.isArray(body.data)).toBe(true);
    expect(body.data.length).toBeGreaterThanOrEqual(22);

    for (const row of body.data) {
      expect(row.error ?? "").toBe("");
      expect(row.subject).not.toBe("");
    }

    await page.goto("orgs/test/test/emails");

    const list = page.getByTestId("email-preview-list");
    await expect(list).toBeVisible();
    await expect(list.locator("li")).toHaveCount(body.data.length);

    // A template that ships only because this spec added its fixture.
    await page.getByTestId("email-preview-item-incident-comment").click();

    await expect(page.getByTestId("email-preview-subject")).toContainText(
      "COMMENT",
    );

    const frame = page.getByTestId("email-preview-frame");
    await expect(frame).toBeVisible();

    // The iframe really rendered the branded wrapper, not an error page.
    const framed = page.frameLocator('[data-testid="email-preview-frame"]');
    await expect(framed.locator("body")).toContainText("New comment on");
  });

  test("switches to the plaintext part", async ({
    authenticatedPage: page,
  }) => {
    await page.goto("orgs/test/test/emails?template=welcome.html");

    await page.getByTestId("email-preview-format-text").click();

    const pre = page.getByTestId("email-preview-text");
    await expect(pre).toBeVisible();
    await expect(pre).not.toContainText("{{");

    // The scheme toggle is HTML-only — the plaintext part has no styling to
    // switch, so offering the choice there would be a dead control.
    await expect(page.getByTestId("email-preview-scheme-dark")).toBeHidden();
  });

  test("previews the dark rendering and keeps it in the URL", async ({
    authenticatedPage: page,
  }) => {
    await page.goto("orgs/test/test/emails?template=incident-created.html");

    const frame = page.getByTestId("email-preview-frame");
    await expect(frame).toHaveAttribute("data-scheme", "light");
    await expect(frame).not.toHaveAttribute("src", /colorScheme/);

    await page.getByTestId("email-preview-scheme-dark").click();

    await expect(frame).toHaveAttribute("src", /colorScheme=dark/);
    await expect(frame).toHaveAttribute("data-scheme", "dark");
    await expect(page).toHaveURL(/colorScheme=dark/);

    // The iframe really applied the dark palette: the media query is gone (an
    // iframe can never report prefers-color-scheme: dark on its own) and the
    // dark card surface is the one that ends up computed on .container.
    const framed = page.frameLocator('[data-testid="email-preview-frame"]');
    const card = framed.locator("table.container");
    await expect(card).toBeVisible();
    await expect
      .poll(() => card.evaluate((el) => getComputedStyle(el).backgroundColor))
      .toBe("rgb(20, 30, 40)");

    // Going back to Light drops the param entirely, so the light URL stays the
    // untouched one the mailer sends.
    await page.getByTestId("email-preview-scheme-light").click();
    await expect(frame).not.toHaveAttribute("src", /colorScheme/);
    await expect(page).not.toHaveURL(/colorScheme/);
  });

  test("deep-links straight into the dark preview", async ({
    authenticatedPage: page,
  }) => {
    await page.goto(
      "orgs/test/test/emails?template=incident-created.html&colorScheme=dark",
    );

    const frame = page.getByTestId("email-preview-frame");
    await expect(frame).toHaveAttribute("data-scheme", "dark");
    await expect(frame).toHaveAttribute("src", /colorScheme=dark/);
    await expect(page.getByTestId("email-preview-scheme-dark")).toHaveAttribute(
      "aria-pressed",
      "true",
    );
  });
});
