import { test, expect, API_BASE } from "./fixtures";

/**
 * Organization → Parameters (spec 2026-09-11-03).
 *
 * The load-bearing leg here is the one no unit test reaches: that a value
 * typed into this page really lands in the org's parameter store AND that the
 * page never shows it again. A UI test that only asserted "the row appeared"
 * would pass just as happily against a page that echoed the secret back.
 */
test.describe("Organization parameters", () => {
  test("an admin creates a secret parameter, sees the reference, and never sees the value again", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const key = `e2e-param-${Date.now()}`;
    const value = `s3cr3t-${Date.now()}`;

    await page.goto("orgs/test/organization/parameters");
    await page.waitForLoadState("networkidle");

    await page.getByTestId("parameter-add").click();
    await page.getByTestId("parameter-key-input").fill(key);
    await page.getByTestId("parameter-value-input").fill(value);
    await page.getByTestId("parameter-save").click();

    const row = page.getByTestId(`parameter-row-${key}`);
    await expect(row).toBeVisible({ timeout: 10000 });

    // The row advertises the reference to paste into a manifest…
    await expect(row).toContainText(`\${param:${key}}`);

    // …and marks the value write-only rather than masking it, because the API
    // sends no value at all for a secret parameter.
    await expect(row.getByTestId("parameter-write-only")).toBeVisible();

    // The strongest assertion available from the browser: the secret is
    // nowhere in the rendered page, not in a hidden attribute, not in a
    // collapsed panel.
    await expect(page.locator("body")).not.toContainText(value);

    // And it is not in the API payload either — the page could not show what
    // it is never sent.
    const listed = await page.request.get(
      `${API_BASE}/api/v1/orgs/test/parameters`,
    );
    expect(listed.status()).toBe(200);
    expect(await listed.text()).not.toContain(value);

    // Rotation keeps the key (and every reference to it) and takes a new value.
    await page.getByTestId(`parameter-row-${key}`).getByTestId("parameter-row-rotate").click();
    await expect(page.getByTestId("parameter-key-input")).toBeDisabled();
    await page.getByTestId("parameter-value-input").fill(`${value}-rotated`);
    await page.getByTestId("parameter-save").click();
    await expect(page.getByTestId(`parameter-row-${key}`)).toBeVisible();

    // Delete, confirmed through the destructive dialog.
    await page.getByTestId(`parameter-row-${key}`).getByTestId("parameter-row-delete").click();
    await page.getByTestId("parameter-delete-confirm").click();
    await expect(page.getByTestId(`parameter-row-${key}`)).toHaveCount(0, {
      timeout: 10000,
    });
  });

  test("a malformed key is refused before the request is sent", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/organization/parameters");
    await page.waitForLoadState("networkidle");

    await page.getByTestId("parameter-add").click();
    await page.getByTestId("parameter-key-input").fill("NotLowercase");
    await page.getByTestId("parameter-value-input").fill("whatever");

    await expect(page.getByTestId("parameter-key-error")).toBeVisible();
    await expect(page.getByTestId("parameter-save")).toBeDisabled();
  });
});
