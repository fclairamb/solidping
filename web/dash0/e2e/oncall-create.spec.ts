import { test, expect } from "./fixtures";

test.describe("On-call schedule create page — slug auto-fill", () => {
  test("slug auto-fills from name while slug is unedited", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.goto("orgs/test/on-call/new");
    await page.waitForLoadState("networkidle");

    const nameInput = page.getByLabel(/^name/i).first();
    const slugInput = page.getByLabel(/slug/i).first();

    await nameInput.fill("Production team");
    await expect(slugInput).toHaveValue("production-team");

    await nameInput.fill("Production Team EU");
    await expect(slugInput).toHaveValue("production-team-eu");
  });

  test("manually editing slug breaks the auto-fill link", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.goto("orgs/test/on-call/new");
    await page.waitForLoadState("networkidle");

    const nameInput = page.getByLabel(/^name/i).first();
    const slugInput = page.getByLabel(/slug/i).first();

    await nameInput.fill("Production");
    await expect(slugInput).toHaveValue("production");

    // Manually override the slug.
    await slugInput.fill("custom-slug");

    // Typing more in the name must NOT overwrite the manually set slug.
    await nameInput.fill("Production Team Extra");
    await expect(slugInput).toHaveValue("custom-slug");
  });
});
