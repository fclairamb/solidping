import { test, expect } from "./fixtures";

test.describe("On-call schedule create page", () => {
  test("has no slug field — schedules are addressed by uid", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.goto("orgs/test/on-call/new");
    await page.waitForLoadState("networkidle");

    // The name field is present…
    await expect(page.getByLabel(/^name/i).first()).toBeVisible();
    // …but the removed slug field must not be.
    await expect(page.getByLabel(/slug/i)).toHaveCount(0);
  });

  test("creating with a name only navigates to the uid detail page", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.goto("orgs/test/on-call/new");
    await page.waitForLoadState("networkidle");

    const name = `E2E Create ${Date.now()}`;
    await page.getByLabel(/^name/i).first().fill(name);

    await page.getByRole("button", { name: /save|create/i }).first().click();

    // The detail route uses a UUID path segment, never a slug.
    await page.waitForURL(
      (url) =>
        /\/on-call\/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(
          url.pathname,
        ),
    );
    await expect(page.getByRole("heading", { name })).toBeVisible();
  });
});
