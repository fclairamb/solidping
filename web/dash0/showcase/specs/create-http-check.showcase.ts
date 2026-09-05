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
/**
 * How long to stay on the check detail page before the published still is
 * taken, in ms. Must exceed the 10-second interval the flow selects, so a full
 * period elapses and the two plotted results are a genuine interval apart.
 */
const MIN_DETAIL_DWELL_MS = 11_000;

test("create an HTTP check", async ({ page }, testInfo) => {
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

    // 2. "New check" — stays on the full frame. The click changes route, and
    //    a push-in that has to snap straight back out for the navigation reads
    //    as a glitch rather than as emphasis.
    await clickOn(page, newCheckButton);
    const typeSelect = page.getByTestId("check-type-select");
    await expect(typeSelect).toBeVisible();
    await focus(page, null, { label: "form-loaded" });
    await beat(page, 1400);

    // 3. No check-type step. The form already opens on HTTP
    //    (`initialType = initialData?.type || "http"` in check-form.tsx), so
    //    opening the combobox and searching for the value that was already
    //    selected filmed a no-op — "HTTP" in, "HTTP" out — which is dead time
    //    in a demo that is meant to be tight.
    //
    //    Asserted rather than assumed: if that default ever changes, this
    //    fails loudly instead of quietly filming a check of the wrong type
    //    whose form has none of the fields the rest of this script drives.
    await expect(typeSelect).toContainText("HTTP");
    const urlInput = page.getByTestId("check-url-input");
    await expect(urlInput).toBeVisible();

    // 4. Target URL and name, typed rather than injected.
    const nameInput = page.getByTestId("check-name-input");
    await focus(page, [urlInput, nameInput], { zoom: 1.5, label: "url-and-name" });
    await clickOn(page, urlInput);
    await typeHuman(page, urlInput, FEATURED_CHECK.url);
    await beat(page, 600);
    await clickOn(page, nameInput);
    await typeHuman(page, nameInput, FEATURED_CHECK.name);
    await beat(page, 900);

    // 5. Interval — 10 seconds, so two results land while the camera is still
    //    on the detail page (step 9 waits for them).
    //
    //    Asserted rather than best-effort: the option ladder is filtered by the
    //    check type's minPeriodSeconds and by the org's rate entitlement
    //    (buildIntervalOptions in check-form.tsx), so if "10 seconds" is not on
    //    offer the take would silently film a different cadence than the one
    //    the docs describe. Fail loudly instead.
    const periodSelect = page.getByTestId("check-period-select");
    await expect(periodSelect).toBeVisible();
    await focus(page, periodSelect, { zoom: 1.35, label: "interval" });
    await clickOn(page, periodSelect);
    await beat(page, 600);
    const tenSeconds = page.getByRole("option").filter({ hasText: /10 seconds/ });
    await expect(
      tenSeconds,
      'the "10 seconds" interval must be offered — check minPeriodSeconds for ' +
        "the http check type and the org's maxChecksPerMinute entitlement",
    ).toHaveCount(1);
    await tenSeconds.first().click();
    await beat(page, 700);

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

    // 8. Scroll down to the submit button, then save → land on the check
    //    detail page.
    //
    //    The scroll is explicit and on camera. Playwright would auto-scroll as
    //    part of the click, but that happens instantly at click time, so the
    //    painted cursor would appear to travel toward a button the viewer has
    //    never seen. Smooth window scroll for the look; scrollIntoViewIfNeeded
    //    is the guarantee, since it also handles the case where the form sits
    //    in its own scrollable container rather than scrolling the window.
    const submitButton = page.getByTestId("check-submit-button");
    await page.evaluate(() =>
      window.scrollTo({
        top: document.documentElement.scrollHeight,
        behavior: "smooth",
      }),
    );
    await beat(page, 900);
    await submitButton.scrollIntoViewIfNeeded();
    await expect(submitButton).toBeInViewport();
    await beat(page, 500);
    await clickOn(page, submitButton);
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 20000 });
    await page.waitForLoadState("networkidle");
    const detailArrivedAt = Date.now();

    // 9. Let the detail page settle: long enough for the "Check created
    //    successfully" toast to expire, so the published still is not a
    //    screenshot of a notification sitting on top of the search box.
    await focus(page, null, { label: "detail-page" });
    await beat(page, 4000);

    // 9b. Hold on the detail page for at least one FULL period before the
    //     still is taken.
    //
    //     Waiting only for a second result is not enough: the scheduler aligns
    //     runs to wall-clock boundaries, so the tick after the creation run can
    //     land a second or two later, and the chart then plots two points
    //     across a two-second window — technically two results, but it reads as
    //     a glitch rather than as a check reporting on a cadence. Dwelling past
    //     the 10-second interval guarantees the two points are a real interval
    //     apart and the chart spans something meaningful.
    const dwellRemaining = MIN_DETAIL_DWELL_MS - (Date.now() - detailArrivedAt);
    if (dwellRemaining > 0) {
      await beat(page, dwellRemaining);
    }

    // 9c. ...and only then confirm the check has actually reported twice, so
    //     the published frame shows a populated results table and a
    //     response-time chart with a line in it rather than an empty state.
    const resultRows = page.locator('[data-testid^="result-row-"]');
    await expect
      .poll(() => resultRows.count(), { timeout: 60_000, intervals: [500] })
      .toBeGreaterThanOrEqual(2);
    await beat(page, 1200);
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
    await writeCues(page, testInfo).catch(() => undefined);
    // Leave the showcase org empty; the next run wipes it clean anyway.
    await deleteAllChecks(page, token);
    // And give the account its own name back — recording media must not
    // permanently rename anybody's admin user.
    await restoreShowcaseIdentity(page, token, previousUserName);
  }
});
