import {
  test,
  expect,
  apiLogin,
  ensureCleanShowcaseOrg,
  applyShowcaseIdentity,
  restoreShowcaseIdentity,
  seedDemoData,
  deleteAllChecks,
  still,
  beat,
  uiLogin,
  installCursor,
  clickOn,
  typeHuman,
  focus,
  writeCues,
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
 *
 * ## The choreography
 *
 * `focus()` calls do not zoom the browser — they record cue points that
 * `postprocess.ts` turns into a camera move over the finished footage. The
 * house rules they follow:
 *
 * - never more than ~1.8x (2x is the crisp ceiling at deviceScaleFactor 2);
 * - the zoom follows the cursor, never leads it, so a `focus()` sits
 *   immediately before the travel it accompanies;
 * - back to the full frame before any route change — a zoom across a
 *   navigation reads as a glitch;
 * - the last move eases back out, so the looping `<video>` has a seamless
 *   join.
 */
test("create an HTTP check", async ({ page }) => {
  // Provision (or wipe clean) a dedicated org and stage the demo data BEFORE
  // anything is filmed, so the only content that ever reaches the camera is
  // content this pipeline put there.
  const bootstrapToken = await apiLogin(page);
  const token = await ensureCleanShowcaseOrg(page, bootstrapToken);

  // Borrowed, not permanent: /auth/me writes the global user row, so the
  // previous display name is restored in the finally below.
  const previousUserName = await applyShowcaseIdentity(page, token);

  try {
    await seedDemoData(page, token);

    // Headless Chromium paints no pointer. Must be installed before the first
    // navigation — uiLogin() is what performs it.
    await installCursor(page);
    await uiLogin(page);

    // 1. The checks list — the starting point, with realistic demo data.
    await page.goto(`orgs/${SHOWCASE_ORG}/checks`);
    await page.waitForLoadState("networkidle");
    const newCheckButton = page.getByTestId("new-check-button");
    await expect(newCheckButton).toBeVisible();
    await focus(page, null, { label: "checks-list" });
    await beat(page, 1800);
    await still(page, "01-checks-list");

    // 2. "New check" — a gentle push-in that travels with the cursor, then
    //    straight back out because the click changes route.
    await focus(page, newCheckButton, { zoom: 1.4, label: "new-check-button" });
    await clickOn(page, newCheckButton);
    const typeSelect = page.getByTestId("check-type-select");
    await expect(typeSelect).toBeVisible();
    await focus(page, null, { label: "form-loaded" });
    await beat(page, 1400);

    // 3. Pick the HTTP check type from the searchable type combobox.
    await focus(page, typeSelect, { zoom: 1.6, label: "type-picker" });
    await clickOn(page, typeSelect);
    const typeSearch = page.getByPlaceholder("Search check types...");
    await expect(typeSearch).toBeVisible();
    await typeHuman(page, typeSearch, "http");
    await beat(page, 800);
    await page.getByRole("option").first().click();
    const urlInput = page.getByTestId("check-url-input");
    await expect(urlInput).toBeVisible();
    await beat(page, 700);

    // 4. Target URL and name, typed rather than injected.
    const nameInput = page.getByTestId("check-name-input");
    await focus(page, [urlInput, nameInput], { zoom: 1.5, label: "url-and-name" });
    await clickOn(page, urlInput);
    await typeHuman(page, urlInput, FEATURED_CHECK.url);
    await beat(page, 600);
    await clickOn(page, nameInput);
    await typeHuman(page, nameInput, FEATURED_CHECK.name);
    await beat(page, 900);

    // 5. Interval — pick the shortest offered cadence so the detail page shows
    //    results quickly.
    const periodSelect = page.getByTestId("check-period-select");
    if (await periodSelect.isVisible()) {
      await focus(page, periodSelect, { zoom: 1.35, label: "interval" });
      await clickOn(page, periodSelect);
      await beat(page, 600);
      const options = page.getByRole("option");
      const preferred = options.filter({ hasText: /1 minute/ });
      await ((await preferred.count()) > 0
        ? preferred.first()
        : options.first()
      ).click();
      await beat(page, 700);
    }

    // 6. Regions — tick every offered region so the check runs from everywhere.
    const regions = page.locator('[data-testid^="region-option-"]');
    const regionCount = await regions.count();
    if (regionCount > 0) {
      await focus(page, [regions.first(), regions.last()], {
        zoom: 1.3,
        label: "regions",
      });
    }
    for (let i = 0; i < regionCount; i++) {
      const region = regions.nth(i);
      const box = region.getByRole("checkbox");
      if ((await box.getAttribute("data-state")) !== "checked") {
        await clickOn(page, region, { durationMs: 260 });
        await beat(page, 350);
      }
    }

    // 7. Full frame for the still and for the save — never zoom across a route
    //    change.
    await focus(page, null, { label: "form-complete" });
    await beat(page, 1400);
    await still(page, "02-check-form-filled");

    // 8. Save → land on the check detail page.
    await clickOn(page, page.getByTestId("check-submit-button"));
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 20000 });
    await page.waitForLoadState("networkidle");

    // 9. Let the detail page settle so the first results have a chance to land.
    await focus(page, null, { label: "detail-page" });
    await beat(page, 4000);
    await still(page, "03-check-detail");

    // 10. A slow Ken-Burns push-in toward the status / response-time area, then
    //     an ease back out so the loop point is seamless.
    const chart = page.getByTestId("response-time-chart-wrapper");
    await focus(page, (await chart.count()) > 0 ? chart.first() : null, {
      zoom: 1.15,
      transitionMs: 2600,
      label: "detail-ken-burns",
    });
    await beat(page, 3200);
    await focus(page, null, { transitionMs: 1200, label: "loop-out" });
    await beat(page, 1600);
  } finally {
    // The cue list is worth keeping even when the take failed — it is how you
    // find out whether the framing or the flow was the problem.
    await writeCues(page, "create-http-check").catch(() => undefined);
    // Leave the showcase org empty; the next run wipes it clean anyway.
    await deleteAllChecks(page, token);
    // And give the account its own name back — recording media must not
    // permanently rename anybody's admin user.
    await restoreShowcaseIdentity(page, token, previousUserName);
  }
});
