import { test, expect, API_BASE, disableHttpCache, type Page } from "./fixtures";

// End-to-end proof that spec 2026-08-22-03 is a refactor and not a regression.
//
// Branding moved out of three `status_pages` columns into the page's `settings`
// JSON, and the unsigned public route stopped asking "is this the current asset
// of a live, enabled page?" in favour of the file's own attachment topic. None
// of that is visible from here — which is the point. What this test asserts is
// the operator-visible loop that has to keep working across the move:
//
//   upload a logo → it renders on the public status page → clear it → it is gone.
//
// It deliberately drives the real upload endpoint and the real public route,
// because the two halves of the change meet exactly there: an upload that
// forgot to set the topic would store the blob fine, return a URL fine, and
// 404 on the page.

// A 1x1 transparent PNG. Self-contained, so nothing here needs network access.
const PNG_BYTES = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==",
  "base64",
);

async function createStatusPage(page: Page, suffix: string): Promise<string> {
  await page.goto(`orgs/test/status-pages/new`);

  const slug = `e2e-brand-${suffix}`.slice(0, 40);
  const nameInput = page.locator("#name");
  await expect(nameInput).toBeVisible({ timeout: 30000 });
  await nameInput.fill(`e2e-branding-${suffix}`);
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

test.describe("Status page branding", () => {
  // Creating a page, uploading a blob and round-tripping it through the public
  // renderer is well past the 30 s default.
  test.describe.configure({ timeout: 120_000 });

  test("uploads a logo, serves it on the public page, and un-publishes it on clear", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const suffix = Date.now().toString().slice(-9);
    const slug = await createStatusPage(page, suffix);

    const pageUid = new URL(page.url()).pathname.split("/").pop() ?? "";
    expect(pageUid).not.toBe("");

    await page.goto(`orgs/test/status-pages/${pageUid}/edit`);
    await expect(page.getByTestId("status-page-branding-card")).toBeVisible({
      timeout: 30000,
    });

    // --- Control: no logo yet, so the public page wears the SolidPing mark. --
    const publicPage = await page.context().newPage();
    // This tab re-reads the public page after each dashboard save; the
    // endpoint's public, max-age=60 directive would otherwise pin it to the
    // pre-upload body for the whole test. See disableHttpCache.
    await disableHttpCache(publicPage);
    await publicPage.goto(`${API_BASE}/status0/test/${slug}`);
    await expect(publicPage.locator(".sp-logo").first()).toBeVisible({
      timeout: 30000,
    });
    await expect(publicPage.getByTestId("status-page-logo")).toHaveCount(0);

    // --- Upload. ---------------------------------------------------------
    await page.getByTestId("status-page-asset-input-logo").setInputFiles({
      name: "logo.png",
      mimeType: "image/png",
      buffer: PNG_BYTES,
    });

    const preview = page.getByTestId("status-page-asset-preview-logo");
    await expect(preview).toBeVisible({ timeout: 30000 });

    // The URL shape is part of the contract this refactor promised not to
    // change, so it is asserted rather than merely followed.
    await expect(preview).toHaveAttribute(
      "src",
      /\/pub\/status-page-assets\//,
      { timeout: 30000 },
    );

    const assetUrl = await preview.getAttribute("src");
    expect(assetUrl).toBeTruthy();

    // --- It renders on the public page. ----------------------------------
    await publicPage.reload();

    const uploadedLogo = publicPage.getByTestId("status-page-logo");
    await expect(uploadedLogo).toBeVisible({ timeout: 30000 });
    await expect(uploadedLogo).toHaveAttribute("src", assetUrl!, {
      timeout: 30000,
    });

    // ...and the bytes are really served, unsigned, by the public route. A
    // logo that renders as a broken <img> would still satisfy the attribute
    // assertion above.
    const served = await publicPage.request.get(`${API_BASE}${assetUrl}`);
    expect(served.status()).toBe(200);
    expect(served.headers()["content-type"]).toContain("image/png");
    expect(served.headers()["x-content-type-options"]).toBe("nosniff");

    // --- Clear: the blob must stop being publicly reachable. --------------
    await page.getByTestId("status-page-asset-remove-logo").click();
    await expect(preview).toHaveCount(0, { timeout: 30000 });

    const afterClear = await publicPage.request.get(`${API_BASE}${assetUrl}`);
    expect(afterClear.status()).toBe(404);

    await publicPage.reload();
    await expect(publicPage.locator(".sp-logo").first()).toBeVisible({
      timeout: 30000,
    });
    await expect(publicPage.getByTestId("status-page-logo")).toHaveCount(0);

    await publicPage.close();
  });
});
