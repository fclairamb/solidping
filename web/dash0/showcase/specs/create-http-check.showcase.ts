import {
  test,
  expect,
  apiLogin,
  seedDemoData,
  cleanupDemoData,
  still,
  beat,
  uiLogin,
  SHOWCASE_ORG,
  FEATURED_CHECK,
} from "../fixtures";

/**
 * Showcase recording: creating an HTTP check, end to end.
 *
 * NOT a test — it asserts only enough to fail loudly when the UI moved and the
 * recording would otherwise be garbage. Run via `make showcase`.
 *
 * The whole flow is captured as one video (`video: "on"` in
 * `showcase/playwright.config.ts`), and named still frames are written along
 * the way for the docs page to embed.
 */
test("create an HTTP check", async ({ page }) => {
  await uiLogin(page);
  const token = await apiLogin(page);
  const seeded = await seedDemoData(page, token);
  let createdUid: string | null = null;

  try {
    // 1. The checks list — the starting point, with realistic demo data.
    await page.goto(`orgs/${SHOWCASE_ORG}/checks`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("new-check-button")).toBeVisible();
    await beat(page, 1500);
    await still(page, "01-checks-list");

    // 2. "New check".
    await page.getByTestId("new-check-button").click();
    await expect(page.getByTestId("check-type-select")).toBeVisible();
    await beat(page);

    // 3. Pick the HTTP check type from the searchable type combobox.
    await page.getByTestId("check-type-select").click();
    await page.getByPlaceholder("Search check types...").fill("http");
    await beat(page, 800);
    await page.getByRole("option").first().click();
    await expect(page.getByTestId("check-url-input")).toBeVisible();
    await beat(page, 800);

    // 4. Target URL and name.
    await page.getByTestId("check-url-input").fill(FEATURED_CHECK.url);
    await beat(page, 600);
    await page.getByTestId("check-name-input").fill(FEATURED_CHECK.name);
    await beat(page, 800);

    // 5. Interval — pick the shortest offered cadence so the detail page shows
    //    results quickly.
    const periodSelect = page.getByTestId("check-period-select");
    if (await periodSelect.isVisible()) {
      await periodSelect.click();
      await beat(page, 600);
      const options = page.getByRole("option");
      const preferred = options.filter({ hasText: /1 minute/ });
      await ((await preferred.count()) > 0
        ? preferred.first()
        : options.first()
      ).click();
      await beat(page, 600);
    }

    // 6. Regions — tick every offered region so the check runs from everywhere.
    const regions = page.locator('[data-testid^="region-option-"]');
    const regionCount = await regions.count();
    for (let i = 0; i < regionCount; i++) {
      const region = regions.nth(i);
      const box = region.getByRole("checkbox");
      if ((await box.getAttribute("data-state")) !== "checked") {
        await region.click();
        await beat(page, 400);
      }
    }

    await beat(page, 1200);
    await still(page, "02-check-form-filled");

    // 7. Save → land on the check detail page.
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 20000 });
    await page.waitForLoadState("networkidle");
    createdUid = page.url().match(/\/checks\/([0-9a-f-]{36})/)?.[1] ?? null;

    // 8. Let the detail page settle so the first results have a chance to land.
    await beat(page, 4000);
    await still(page, "03-check-detail");
    await beat(page, 1500);
  } finally {
    await cleanupDemoData(page, token, [
      ...seeded,
      ...(createdUid ? [createdUid] : []),
    ]);
  }
});
