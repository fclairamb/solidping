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
  uiFirstLogin,
  installCursor,
  clickOn,
  typeHuman,
  focus,
  writeCues,
  SHOWCASE_ORG,
  FEATURED_CHECK,
} from "../fixtures";

/**
 * Showcase recording: from a brand-new install to the first results.
 *
 * NOT a test — it asserts only enough to fail loudly when the UI moved and the
 * recording would otherwise be garbage. Run via `make showcase`, which films
 * the `docker run` segment first (`terminal.ts`) and stitches the two together
 * afterwards (`postprocess.ts`).
 *
 * ## What changed from the cut this replaces
 *
 * `create-http-check.showcase.ts` opened on an already-running, already
 * logged-in dashboard, which answered none of the question the audience
 * actually has — *how long until it runs*. So this take starts where a new
 * install starts:
 *
 * 1. the first sign-in with the seeded credentials, and the **forced password
 *    rotation** every fresh database imposes — filmed, not satisfied over the
 *    API beforehand. That ordering is the whole trick: the bootstrap calls
 *    below (`apiLogin`, `ensureCleanShowcaseOrg`) only run once the account is
 *    already through the rotation screen on camera;
 * 2. creating an HTTP check, the flow the old cut had, unchanged;
 * 3. the first results arriving — from **two regions** when the server offers
 *    more than one, which is the beat the previous cut could never film
 *    because the recommended side-car was a single node.
 *
 * ## The choreography
 *
 * `focus()` calls do not zoom the browser — they record cue points that
 * `postprocess.ts` turns into a camera move over the finished footage, and that
 * the burned-in lower thirds are anchored to. The house rules they follow:
 *
 * - never more than ~1.8x (2x is the crisp ceiling at deviceScaleFactor 2);
 * - the zoom follows the cursor, never leads it, so a `focus()` sits
 *   immediately before the travel it accompanies;
 * - back to the full frame before any route change — a zoom across a
 *   navigation reads as a glitch;
 * - the last move eases back out, so the looping `<video>` has a seamless
 *   join.
 *
 * Cue labels are load-bearing beyond the camera: `postprocess.ts` anchors the
 * "First login" / "First check" / results captions to `login`, `checks-list`
 * and `detail-page`, and decides whether the dwell needs a time-lapse from the
 * `detail-page` → `chart` gap. Renaming one fails the run rather than silently
 * publishing a cut with a caption missing.
 */

/** Intervals this flow is willing to film, fastest first. */
const PREFERRED_INTERVALS = [
  { label: "5 seconds", seconds: 5 },
  { label: "10 seconds", seconds: 10 },
];

/**
 * How many full check periods the detail page is held for.
 *
 * Two points on a chart read as "it ran twice", not as a check reporting on a
 * cadence. Four periods put a short line on the chart and a handful of rows in
 * the results table, which is what a monitor looks like once it is working.
 * The dwell is long enough that `postprocess.ts` plays it as a tagged
 * time-lapse.
 */
const DETAIL_DWELL_PERIODS = 4;

/**
 * Extra time held on the detail page beyond the last full check period, in ms.
 *
 * The dwell has to exceed the periods, not merely wait for the results:
 * the scheduler aligns runs to wall-clock boundaries, so the tick after the
 * creation run can land a second or two later and the chart would plot two
 * points across a two-second window — technically two results, but it reads as
 * a glitch rather than as a check reporting on a cadence.
 *
 * Filming at 5 seconds rather than 10 halves that dwell; `postprocess.ts`
 * plays it as a tagged time-lapse either way.
 */
const DETAIL_DWELL_MARGIN_MS = 2_500;

test("setup to first result", async ({ page }, testInfo) => {
  // Headless Chromium paints no pointer. Must be installed before the first
  // navigation — uiFirstLogin() is what performs it.
  await installCursor(page);

  // 1. The first sign-in on a brand-new install: seeded credentials, the
  //    forced rotation screen, and the dashboard behind it. Nothing has
  //    touched the API yet, which is exactly why the rotation is on camera.
  await uiFirstLogin(page);
  await beat(page, 1200);

  // 2. Only now bootstrap: provision (or wipe clean) the dedicated org and
  //    stage the demo data, so the only content that ever reaches the camera
  //    from here on is content this pipeline put there.
  // Everything between this cue and `checks-list` is the pipeline provisioning
  // its org over the API while the dashboard sits still — `postprocess.ts` cuts
  // it out rather than publishing it or speeding it up.
  await focus(page, null, { label: "bootstrap" });
  const bootstrapToken = await apiLogin(page);
  const token = await ensureCleanShowcaseOrg(page, bootstrapToken);

  // Borrowed, not permanent: /auth/me writes the global user row, so the
  // previous display name is restored in the finally below.
  const previousUserName = await applyShowcaseIdentity(page, token);

  try {
    await seedDemoData(page, token);

    // 3. The checks list — a full page load, so the sidebar picks up both the
    //    new org and the borrowed display name.
    await page.goto(`orgs/${SHOWCASE_ORG}/checks`);
    await page.waitForLoadState("networkidle");
    const newCheckButton = page.getByTestId("new-check-button");
    await expect(newCheckButton).toBeVisible();
    await focus(page, null, { label: "checks-list" });
    await beat(page, 1100);
    await still(page, "01-checks-list");

    // 4. "New check" — stays on the full frame. The click changes route, and
    //    a push-in that has to snap straight back out for the navigation reads
    //    as a glitch rather than as emphasis.
    await clickOn(page, newCheckButton);
    const typeSelect = page.getByTestId("check-type-select");
    await expect(typeSelect).toBeVisible();
    await focus(page, null, { label: "form-loaded" });
    await beat(page, 800);

    // 5. No check-type step. The form already opens on HTTP
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

    // 6. Target URL and name, typed rather than injected.
    const nameInput = page.getByTestId("check-name-input");
    await focus(page, [urlInput, nameInput], { zoom: 1.5, label: "url-and-name" });
    await clickOn(page, urlInput);
    await typeHuman(page, urlInput, FEATURED_CHECK.url, {
      minDelayMs: 28,
      maxDelayMs: 46,
    });
    await beat(page, 350);
    await clickOn(page, nameInput);
    await typeHuman(page, nameInput, FEATURED_CHECK.name, {
      minDelayMs: 28,
      maxDelayMs: 46,
    });
    await beat(page, 550);

    // 7. Interval — the fastest one on offer, so the dwell on the detail page
    //    stays as short as DETAIL_DWELL_PERIODS allows.
    //
    //    Asserted rather than best-effort: the option ladder is filtered by the
    //    check type's minPeriodSeconds and by the org's rate entitlement
    //    (buildIntervalOptions in check-form.tsx), so if neither interval is on
    //    offer the take would silently film a different cadence than the one
    //    the docs describe. Fail loudly instead.
    const periodSelect = page.getByTestId("check-period-select");
    await expect(periodSelect).toBeVisible();
    await focus(page, periodSelect, { zoom: 1.35, label: "interval" });
    await clickOn(page, periodSelect);
    await beat(page, 400);

    let chosenInterval: { label: string; seconds: number } | null = null;
    for (const candidate of PREFERRED_INTERVALS) {
      const option = page
        .getByRole("option")
        .filter({ hasText: new RegExp(candidate.label) });
      if ((await option.count()) > 0) {
        await option.first().click();
        chosenInterval = candidate;
        break;
      }
    }
    expect(
      chosenInterval,
      `none of ${PREFERRED_INTERVALS.map((i) => i.label).join(" / ")} is offered — ` +
        "check minPeriodSeconds for the http check type and the org's " +
        "maxChecksPerMinute entitlement",
    ).not.toBeNull();
    console.log(`showcase: filming a ${chosenInterval?.label} interval`);
    await beat(page, 450);

    // 8. Regions — tick every offered region so the check runs from everywhere.
    //    The picker only renders when the server offers more than one region
    //    (`availableRegions.length > 1` in check-form.tsx), which is what the
    //    two-node side-car recipe in showcase/README.md exists to arrange.
    const regions = page.locator('[data-testid^="region-option-"]');
    const regionCount = await regions.count();
    console.log(`showcase: the form offers ${regionCount} region option(s)`);
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
        await beat(page, 260);
      }
    }

    // 9. Full frame for the still and for the save — never zoom across a route
    //    change.
    await focus(page, null, { label: "form-complete" });
    await beat(page, 900);
    await still(page, "02-check-form-filled");

    // 10. Scroll down to the submit button, then save → land on the check
    //     detail page.
    //
    //     The scroll is explicit and on camera. Playwright would auto-scroll as
    //     part of the click, but that happens instantly at click time, so the
    //     painted cursor would appear to travel toward a button the viewer has
    //     never seen. Smooth window scroll for the look; scrollIntoViewIfNeeded
    //     is the guarantee, since it also handles the case where the form sits
    //     in its own scrollable container rather than scrolling the window.
    const submitButton = page.getByTestId("check-submit-button");
    await page.evaluate(() =>
      window.scrollTo({
        top: document.documentElement.scrollHeight,
        behavior: "smooth",
      }),
    );
    await beat(page, 600);
    await submitButton.scrollIntoViewIfNeeded();
    await expect(submitButton).toBeInViewport();
    await beat(page, 300);
    await clickOn(page, submitButton);
    // Saving and loading the detail page is another wait with nothing to see —
    // cut out between this cue and `detail-page`.
    await focus(page, null, { label: "saving" });
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 20000 });
    await page.waitForLoadState("networkidle");
    const detailArrivedAt = Date.now();

    // 11. Let the detail page settle: long enough for the "Check created
    //     successfully" toast to expire, so the published still is not a
    //     screenshot of a notification sitting on top of the search box.
    await focus(page, null, { label: "detail-page" });
    await beat(page, 2200);

    // 12. Hold past DETAIL_DWELL_PERIODS full periods before the still is
    //     taken, so the plotted points are genuine intervals apart (see
    //     DETAIL_DWELL_MARGIN_MS).
    const dwellMs =
      DETAIL_DWELL_PERIODS * (chosenInterval?.seconds ?? 10) * 1000 +
      DETAIL_DWELL_MARGIN_MS;
    const dwellRemaining = dwellMs - (Date.now() - detailArrivedAt);
    if (dwellRemaining > 0) {
      await beat(page, dwellRemaining);
    }

    // 13. ...and only then confirm the check has actually reported
    //     DETAIL_DWELL_PERIODS times from every region, so the published frame
    //     shows a populated results table and a response-time chart with a
    //     line in it rather than an empty state. The table lists one row per
    //     run per region, and a single-node server shows no region picker.
    const expectedRows = DETAIL_DWELL_PERIODS * Math.max(1, regionCount);
    const resultRows = page.locator('[data-testid^="result-row-"]');
    await expect
      .poll(() => resultRows.count(), { timeout: 60_000, intervals: [500] })
      .toBeGreaterThanOrEqual(expectedRows);
    await beat(page, 800);
    await still(page, "03-check-detail");

    // 14. A slow Ken-Burns push-in toward the status / response-time area, then
    //     an ease back out so the loop point is seamless.
    const chart = page.getByTestId("response-time-chart-wrapper");
    await focus(page, (await chart.count()) > 0 ? chart.first() : null, {
      zoom: 1.15,
      transitionMs: 2400,
      label: "chart",
    });
    await beat(page, 2200);
    await focus(page, null, { transitionMs: 1100, label: "loop-out" });
    await beat(page, 1000);
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
