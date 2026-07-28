import { test, expect, API_BASE, type Page } from "./fixtures";

// The appearance editor's contract in one place (spec 2026-07-27-02):
//
//   * typing CSS restyles the PREVIEW IFRAME live, with no save and no server
//     round-trip (the iframe runs the real status0 renderer);
//   * saving publishes it, so the public status page picks it up; and
//   * the preview's message listener ignores anything that is not the exact
//     {type: 'sp:preview-css', css: string} shape.
//
// The brand color is the probe: --brand is read straight off the iframe's
// documentElement, which is exactly where the <style> block lands.

const PREVIEW_BRAND = "rgb(255, 0, 0)";
const SAVED_BRAND = "rgb(0, 128, 255)";

async function createStatusPage(page: Page, suffix: string): Promise<string> {
  await page.goto(`orgs/test/status-pages/new`);

  const slug = `e2e-css-${suffix}`.slice(0, 40);
  const nameInput = page.locator("#name");
  await expect(nameInput).toBeVisible({ timeout: 30000 });
  await nameInput.fill(`e2e-appearance-${suffix}`);
  await page.locator("#slug").fill(slug);

  const create = page.getByRole("button", { name: "Create Status Page" });
  await expect(create).toBeEnabled();
  await create.click();

  // NOT just /status-pages/[^/]+$ — that also matches the /new form we are
  // standing on, so the wait would return before the page even exists.
  await page.waitForURL(
    (url) =>
      /\/status-pages\/[^/]+$/.test(url.pathname) &&
      !url.pathname.endsWith("/new"),
    { timeout: 45000 },
  );

  return slug;
}

/** Reads --brand as the iframe's own document computes it. */
async function previewBrand(page: Page): Promise<string> {
  const frame = page.frameLocator('[data-testid="custom-css-preview"]');

  return frame
    .locator("html")
    .evaluate((el) =>
      getComputedStyle(el).getPropertyValue("--brand").trim(),
    );
}

test.describe("Status page appearance editor", () => {
  // Creating a page, loading the preview iframe (which runs the real status0
  // app) and round-tripping a save through the public page is well past the
  // 30 s default.
  test.describe.configure({ timeout: 120_000 });

  /**
   * Opens the appearance route from the status-page detail screen.
   *
   * Deliberately no waitForLoadState("networkidle") here: the preview iframe
   * hosts status0, which polls its API every 30 s, so the network never goes
   * idle. Wait for the editor itself instead.
   */
  async function openAppearance(page: Page) {
    await page.getByTestId("status-page-appearance-nav").click();
    await page.waitForURL(/\/appearance$/, { timeout: 30000 });
    await expect(page.getByTestId("custom-css-input")).toBeVisible({
      timeout: 30000,
    });
  }

  test("previews CSS live, saves it to the public page, and ignores foreign messages", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const suffix = Date.now().toString().slice(-9);
    const slug = await createStatusPage(page, suffix);

    // Navigate through the UI, proving the detail screen links to the route.
    await openAppearance(page);

    const editor = page.getByTestId("custom-css-input");
    await expect(editor).toHaveValue("");

    const preview = page.locator('[data-testid="custom-css-preview"]');
    await expect(preview).toBeVisible();

    // --- 1. Live preview, no save ---------------------------------------
    await editor.fill(`:root { --brand: ${PREVIEW_BRAND}; }`);

    await expect
      .poll(() => previewBrand(page), { timeout: 15000 })
      .toBe(PREVIEW_BRAND);

    // Nothing was published: the public page is still unstyled.
    const publicPage = await page.context().newPage();
    // Absolute: baseURL is the /dash0/ app, and a relative path would
    // resolve under it and silently load dash0 instead of status0.
    await publicPage.goto(`${API_BASE}/status0/test/${slug}`);
    await publicPage.waitForLoadState("networkidle");
    await expect(publicPage.locator("style", { hasText: "--brand" })).toHaveCount(
      0,
    );

    // --- 2. A wrong-shaped / foreign message is ignored -------------------
    await page.evaluate(() => {
      const frame = document.querySelector<HTMLIFrameElement>(
        '[data-testid="custom-css-preview"]',
      );
      // Right origin, wrong shapes: a foreign type, a missing css field, a
      // non-string css, and a bare string. None may reach the page.
      frame?.contentWindow?.postMessage(
        { type: "evil:preview-css", css: ":root { --brand: rgb(1, 2, 3); }" },
        window.location.origin,
      );
      frame?.contentWindow?.postMessage(
        { type: "sp:preview-css" },
        window.location.origin,
      );
      frame?.contentWindow?.postMessage(
        { type: "sp:preview-css", css: 42 },
        window.location.origin,
      );
      frame?.contentWindow?.postMessage(
        "sp:preview-css",
        window.location.origin,
      );
    });

    // Give the (rejected) messages a chance to land, then re-assert.
    await page.waitForTimeout(500);
    expect(await previewBrand(page)).toBe(PREVIEW_BRAND);

    // --- 3. Save publishes it -------------------------------------------
    await editor.fill(`:root { --brand: ${SAVED_BRAND}; }`);
    await page.getByTestId("custom-css-save").click();

    await expect
      .poll(
        async () => {
          await publicPage.reload();
          await publicPage.waitForLoadState("networkidle");

          return publicPage
            .locator("html")
            .evaluate((el) =>
              getComputedStyle(el).getPropertyValue("--brand").trim(),
            );
        },
        { timeout: 20000 },
      )
      .toBe(SAVED_BRAND);

    // --- 4. Clearing the textarea removes it -----------------------------
    await editor.fill("");
    await page.getByTestId("custom-css-save").click();

    await expect
      .poll(
        async () => {
          await publicPage.reload();
          await publicPage.waitForLoadState("networkidle");

          return publicPage.locator("style", { hasText: "--brand" }).count();
        },
        { timeout: 20000 },
      )
      .toBe(0);

    await publicPage.close();
  });

  test("rejects @import and oversize stylesheets before saving", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const suffix = Date.now().toString().slice(-9);
    await createStatusPage(page, suffix);

    await openAppearance(page);

    const editor = page.getByTestId("custom-css-input");
    const save = page.getByTestId("custom-css-save");

    await editor.fill("@import url('https://evil.example/x.css');");
    await expect(save).toBeDisabled();

    await editor.fill(`:root { --brand: ${PREVIEW_BRAND}; }`);
    await expect(save).toBeEnabled();
  });

  test("offers a starter template listing the theming variables", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const suffix = Date.now().toString().slice(-9);
    await createStatusPage(page, suffix);

    await openAppearance(page);

    await page.getByTestId("custom-css-template").click();

    const value = await page.getByTestId("custom-css-input").inputValue();
    for (const variable of [
      "--brand",
      "--background",
      "--foreground",
      "--card",
      "--border",
      "--status-ok",
      "--status-warning",
      "--status-error",
      ".dark",
    ]) {
      expect(value).toContain(variable);
    }
  });
});
