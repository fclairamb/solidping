import { test, expect, API_BASE, disableHttpCache, type Page, STATUS_BASE } from "./fixtures";

// Hand-written status page text in several languages (spec 2026-11-18-04).
//
// An operator writes a status update in dash0 with French and Chinese
// translations. The public page shows the French title and body to a reader in
// French, the Chinese text to a reader in Chinese, and the literal (English)
// text to a reader in German, who has no entry: the read order is translation,
// then recipe, then literal.
//
// The French and Chinese readings are the positive control of the German one:
// the same page and the same update, so "no French text with ?lang=de" cannot
// pass because the update never reached the page.

async function createStatusPage(page: Page, suffix: string): Promise<{ slug: string; name: string }> {
  await page.goto("orgs/test/status-pages/new");

  const slug = `e2e-tr-${suffix}`.slice(0, 40);
  const name = `e2e-translations-${suffix}`;
  const nameInput = page.locator("#name");
  await expect(nameInput).toBeVisible({ timeout: 30000 });
  await nameInput.fill(name);
  await page.locator("#slug").fill(slug);

  const create = page.getByRole("button", { name: "Create Status Page" });
  await expect(create).toBeEnabled();
  await create.click();

  await page.waitForURL(
    (url) =>
      /\/status-pages\/[^/]+$/.test(url.pathname) &&
      !url.pathname.endsWith("/new"),
    { timeout: 45000 },
  );

  return { slug, name };
}

test.describe("Status update translations", () => {
  test.describe.configure({ timeout: 120_000 });

  test("shows the French text to a French reader and the literal text to a German one", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const suffix = Date.now().toString().slice(-9);
    const { slug, name } = await createStatusPage(page, suffix);

    const title = `Payments outage ${suffix}`;
    const body = `We are investigating the payments outage ${suffix}.`;
    const frTitle = `Panne des paiements ${suffix}`;
    const frBody = `Nous enquêtons sur la panne des paiements ${suffix}.`;
    const zhTitle = `付款故障 ${suffix}`;
    const zhBody = `我们正在调查付款故障 ${suffix}。`;

    await page.goto("orgs/test/status-updates/new");
    await expect(page.getByTestId("status-update-form-title")).toBeVisible({ timeout: 30000 });

    // The update goes on the page just created.
    await page.locator("#statusPage").click();
    await page.getByRole("option", { name }).click();

    await page.getByTestId("status-update-form-title").fill(title);
    await page.getByTestId("status-update-form-body").fill(body);

    // The "Other languages" block: one collapsed group per language, none for
    // the page's own language (English here).
    await expect(page.getByTestId("translations-editor")).toBeVisible();
    await expect(page.getByTestId("translation-en-toggle")).toHaveCount(0);
    await page.getByTestId("translation-fr-toggle").click();
    await page.getByTestId("translation-fr-title").fill(frTitle);
    await page.getByTestId("translation-fr-body").fill(frBody);
    await page.getByTestId("translation-zh-toggle").click();
    await page.getByTestId("translation-zh-title").fill(zhTitle);
    await page.getByTestId("translation-zh-body").fill(zhBody);

    await page.getByTestId("status-update-form-submit").click();
    await page.waitForURL(/\/status-updates\/?$/, { timeout: 15000 });

    const publicPage = await page.context().newPage();
    await disableHttpCache(publicPage);

    // The page is new, so this is its only update.
    const card = (reader: Page) => reader.getByTestId("status-update-title");

    // Positive control: French reader sees the French text.
    await publicPage.goto(`${API_BASE}${STATUS_BASE}/test/${slug}?lang=fr`);
    await expect(card(publicPage)).toHaveText(frTitle, { timeout: 30000 });
    await expect(publicPage.getByTestId("status-update-body")).toHaveText(frBody);

    // Chinese reader sees the Chinese text.
    await publicPage.goto(`${API_BASE}${STATUS_BASE}/test/${slug}?lang=zh`);
    await expect(card(publicPage)).toHaveText(zhTitle, { timeout: 30000 });
    await expect(publicPage.getByTestId("status-update-body")).toHaveText(zhBody);

    // German reader: no entry, so the literal text.
    await publicPage.goto(`${API_BASE}${STATUS_BASE}/test/${slug}?lang=de`);
    await expect(card(publicPage)).toHaveText(title, { timeout: 30000 });
    await expect(publicPage.getByTestId("status-update-body")).toHaveText(body);

    // The edit route round-trips the translations. Pick this test's row by its
    // title: other specs add updates to the same org in parallel, so the first
    // row is not always ours.
    await page
      .locator(`[data-testid="status-update-row"][data-update-title=${JSON.stringify(title)}]`)
      .getByTestId("status-update-row-title")
      .click();
    await page.waitForURL(/\/status-updates\/[0-9a-f-]{36}\/edit/, { timeout: 15000 });
    await expect(page.getByTestId("translation-fr-title")).toHaveValue(frTitle);
    await expect(page.getByTestId("translation-zh-title")).toHaveValue(zhTitle);
  });
});
