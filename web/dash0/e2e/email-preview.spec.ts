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

    // The fixture's comment author, in the subject the index reports.
    await expect(page.getByTestId("email-preview-subject")).toContainText(
      "Bob Builder on",
    );

    const frame = page.getByTestId("email-preview-frame");
    await expect(frame).toBeVisible();

    // The iframe really rendered the template, not an error page.
    const framed = page.frameLocator('[data-testid="email-preview-frame"]');
    await expect(framed.locator("h1")).toContainText("Bob Builder commented on");
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
      .toBe("rgb(22, 30, 39)"); // base.html's dark .container, #161e27

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

  test("previews another language and keeps it in the URL", async ({
    authenticatedPage: page,
  }) => {
    // The endpoint validates the language before rendering anything.
    const refused = await page.request.get(
      `${API_BASE}/api/mgmt/email-preview?locale=xx`,
    );
    expect(refused.status()).toBe(400);
    const frIndex = await page.request.get(
      `${API_BASE}/api/mgmt/email-preview?locale=fr`,
    );
    expect(frIndex.status()).toBe(200);
    for (const row of (await frIndex.json()).data) {
      expect(row.error ?? "").toBe("");
    }

    await page.goto("orgs/test/test/emails?template=incident-created.html");

    const frame = page.getByTestId("email-preview-frame");
    const framed = page.frameLocator('[data-testid="email-preview-frame"]');
    await expect(page.getByTestId("email-preview-locale-en")).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    // English is the endpoint's default: no param, the bytes the mailer sends.
    await expect(frame).not.toHaveAttribute("src", /locale=/);
    await expect(framed.locator("html")).toHaveAttribute("lang", "en");

    // The subjects are re-read in the picked language too.
    const frIndexRequest = page.waitForRequest((request) =>
      request.url().includes("/api/mgmt/email-preview?locale=fr"),
    );
    await page.getByTestId("email-preview-locale-fr").click();
    await frIndexRequest;

    await expect(frame).toHaveAttribute("src", /locale=fr/);
    await expect(page).toHaveURL(/locale=fr/);
    await expect(framed.locator("html")).toHaveAttribute("lang", "fr");

    // The plaintext part follows the same language.
    const frTextRequest = page.waitForRequest(
      (request) =>
        request.url().includes("/api/mgmt/email-preview/incident-created.html") &&
        request.url().includes("format=text") &&
        request.url().includes("locale=fr"),
    );
    await page.getByTestId("email-preview-format-text").click();
    await frTextRequest;
    await expect(page.getByTestId("email-preview-text")).not.toContainText("{{");
    await page.getByTestId("email-preview-format-html").click();

    // Back to English drops the param entirely.
    await page.getByTestId("email-preview-locale-en").click();
    await expect(frame).not.toHaveAttribute("src", /locale=/);
    await expect(page).not.toHaveURL(/locale=/);
    await expect(framed.locator("html")).toHaveAttribute("lang", "en");
  });

  test("deep-links straight into a language", async ({
    authenticatedPage: page,
  }) => {
    await page.goto("orgs/test/test/emails?template=welcome.html&locale=de");

    await expect(page.getByTestId("email-preview-locale-de")).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    const frame = page.getByTestId("email-preview-frame");
    await expect(frame).toHaveAttribute("src", /locale=de/);
    const framed = page.frameLocator('[data-testid="email-preview-frame"]');
    await expect(framed.locator("html")).toHaveAttribute("lang", "de");
  });
});
