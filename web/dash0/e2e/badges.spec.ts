import { test, expect, API_BASE, DASH_BASE, uniqueStamp } from "./fixtures";
import type { Page } from "@playwright/test";

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  const body = await resp.json();
  return body.accessToken;
}

async function createCheck(
  page: Page,
  token: string,
  name: string
): Promise<{ uid: string; slug: string }> {
  const timestamp = Date.now();
  const randomSuffix = uniqueStamp();
  const resp = await page.request.post(
    `${API_BASE}/api/v1/orgs/test/checks`,
    {
      headers: { Authorization: `Bearer ${token}` },
      data: {
        type: "http",
        name,
        config: { url: `https://httpbin.org/anything/${timestamp}-${randomSuffix}` },
        period: "00:05:00",
      },
    }
  );
  return resp.json();
}

// Create `count` extra checks so they sort AHEAD of an already-created target
// check. The list endpoint orders by created_at DESC, so checks created later
// appear earlier; creating >20 newer checks pushes an earlier check past the
// first page (limit=20) of GET /checks.
async function createExtraChecks(
  page: Page,
  token: string,
  prefix: string,
  count: number
): Promise<void> {
  for (let i = 0; i < count; i++) {
    await createCheck(page, token, `${prefix} filler ${i}`);
  }
}

// The badge builder is a child of the check it belongs to (spec
// 2026-09-16-08): /orgs/:org/checks/:checkUid/badges. `badgesUrl` builds that
// path; the legacy org-level /badges?check= URL survives only as a redirect,
// which the "legacy redirect" cases below pin.
function badgesUrl(checkId: string, query = ""): string {
  return `${DASH_BASE}/orgs/test/checks/${checkId}/badges${query}`;
}

test.describe("Badges", () => {
  test.describe.configure({ mode: "serial" });

  test("opens from the check detail Badges button, with no check picker", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const checkName = `Badge Entry ${Date.now()}`;
    const check = await createCheck(page, token, checkName);

    // The builder is reached from the check it belongs to, not from a global
    // sidebar entry.
    await page.goto(`${DASH_BASE}/orgs/test/checks/${check.uid}`);
    await page.waitForLoadState("networkidle");
    await page.getByLabel("Badges").click();

    await page.waitForURL(`**${badgesUrl(check.uid)}`);
    await page.waitForLoadState("networkidle");

    // Verify page heading
    await expect(
      page.getByRole("heading", { name: "Badges", exact: true })
    ).toBeVisible();

    // The check comes from the path — there is no picker to select one, and no
    // "select a check" empty state.
    await expect(page.getByTestId("badge-check-select")).toHaveCount(0);
    await expect(
      page.getByText("Select a check to preview and generate badges")
    ).toHaveCount(0);

    // Verify component checkboxes are present (format select is gone)
    await expect(page.getByTestId("badge-component-status")).toBeVisible();
    await expect(page.getByTestId("badge-component-availability")).toBeVisible();
    await expect(page.getByTestId("badge-component-duration")).toBeVisible();
    await expect(page.getByTestId("badge-component-response-time")).toBeVisible();
    await expect(page.getByTestId("badge-component-uptime-bar")).toBeVisible();
    await expect(
      page.getByTestId("badge-component-response-time-graph")
    ).toBeVisible();

    // Status is checked by default
    await expect(page.getByTestId("badge-component-status")).toBeChecked();

    // Download card must NOT exist
    await expect(page.getByText("Download the badge in different formats")).not.toBeVisible();
  });

  test("shows the preview with download buttons in the header", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const checkName = `Badge E2E ${Date.now()}`;
    const check = await createCheck(page, token, checkName);

    await page.goto(badgesUrl(check.uid));
    await page.waitForLoadState("networkidle");

    // Verify preview appears
    await expect(page.getByTestId("badge-preview")).toBeVisible({
      timeout: 10000,
    });

    // Verify embed codes appear
    await expect(page.getByTestId("badge-embed-url")).toBeVisible();
    await expect(page.getByTestId("badge-embed-markdown")).toBeVisible();
    await expect(page.getByTestId("badge-embed-html")).toBeVisible();

    // SVG and PNG download buttons are in the preview card header
    await expect(page.getByTestId("badge-download-svg")).toBeVisible();
    await expect(page.getByTestId("badge-download-png")).toBeVisible();

    // No JPG button
    await expect(page.getByTestId("badge-download-jpg")).not.toBeVisible();

    // Verify embed URL contains the check identifier and default components
    const urlText = await page.getByTestId("badge-embed-url").textContent();
    expect(urlText).toContain("/badges/status");
    expect(urlText).toContain(`/orgs/test/checks/${check.slug || check.uid}/`);
  });

  test("should toggle Availability checkbox on and update preview URL", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const checkName = `Badge Avail ${Date.now()}`;
    const check = await createCheck(page, token, checkName);

    await page.goto(badgesUrl(check.uid));
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("badge-preview")).toBeVisible({
      timeout: 10000,
    });

    // Period selector should NOT be visible when only status is selected
    await expect(page.getByTestId("badge-period-select")).not.toBeVisible();

    // Toggle Availability on
    await page.getByTestId("badge-component-availability").click();

    // URL should now contain status,availability
    const urlText = await page.getByTestId("badge-embed-url").textContent();
    expect(urlText).toContain("/badges/status,availability");

    // Period selector should now be visible
    await expect(page.getByTestId("badge-period-select")).toBeVisible();

    // components param in page URL
    const pageUrl = new URL(page.url());
    expect(pageUrl.searchParams.get("components")).toBe("status,availability");
  });

  test("toggling all components off falls back to status", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const checkName = `Badge Fallback ${Date.now()}`;
    const check = await createCheck(page, token, checkName);

    await page.goto(badgesUrl(check.uid));
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("badge-preview")).toBeVisible({
      timeout: 10000,
    });

    // Status is the only token; unchecking it must not loop — it falls back to status.
    await page.getByTestId("badge-component-status").click();

    // Status remains checked (fallback) and the embed URL still points at status.
    await expect(page.getByTestId("badge-component-status")).toBeChecked();
    const urlText = await page.getByTestId("badge-embed-url").textContent();
    expect(urlText).toContain("/badges/status");
  });

  test("preview defaults to an interactive object embed, switchable to img", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const checkName = `Badge Mode ${Date.now()}`;
    const check = await createCheck(page, token, checkName);

    await page.goto(badgesUrl(check.uid));
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("badge-preview")).toBeVisible({
      timeout: 10000,
    });

    // The default preview is an <object> embed — the only embedding that
    // shows the badge's hover tooltips in every browser.
    const tagOf = () =>
      page.getByTestId("badge-preview").evaluate((el) => el.tagName);

    expect(await tagOf()).toBe("OBJECT");
    await expect(page.getByTestId("badge-embed-object")).toBeVisible();

    // Switching to the static image embed renders an <img> and records the
    // choice in the URL.
    await page.getByTestId("badge-preview-mode").click();
    await page.getByRole("option", { name: /Static image/i }).click();

    await expect.poll(tagOf).toBe("IMG");
    const pageUrl = new URL(page.url());
    expect(pageUrl.searchParams.get("preview")).toBe("img");
  });

  test("toggling uptime-bar and response-time-graph grows the preview", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const checkName = `Badge Rows ${Date.now()}`;
    const check = await createCheck(page, token, checkName);

    await page.goto(badgesUrl(check.uid));
    await page.waitForLoadState("networkidle");
    const img = page.getByTestId("badge-preview");
    await expect(img).toBeVisible({ timeout: 10000 });

    const heightOf = async () =>
      (await img.boundingBox())?.height ?? 0;

    const baseHeight = await heightOf();

    // Enable uptime-bar → URL gains the token and the width input appears.
    await page.getByTestId("badge-component-uptime-bar").click();
    let urlText = await page.getByTestId("badge-embed-url").textContent();
    expect(urlText).toContain("uptime-bar");
    await expect(page.getByTestId("badge-width")).toBeVisible();

    await expect.poll(heightOf, { timeout: 10000 }).toBeGreaterThan(baseHeight);
    const barHeight = await heightOf();

    // Enable response-time-graph → URL gains the token, preview grows again.
    await page.getByTestId("badge-component-response-time-graph").click();
    urlText = await page.getByTestId("badge-embed-url").textContent();
    expect(urlText).toContain("response-time-graph");

    await expect.poll(heightOf, { timeout: 10000 }).toBeGreaterThan(barHeight);
  });

  test("old uptime-bar section and embeds are absent from the DOM", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const checkName = `Badge No Bar ${Date.now()}`;
    const check = await createCheck(page, token, checkName);

    await page.goto(badgesUrl(check.uid));
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("badge-preview")).toBeVisible({
      timeout: 10000,
    });

    // The standalone uptime-bar preview / embed / period selector are gone.
    await expect(page.getByTestId("uptime-bar-preview-img")).toHaveCount(0);
    await expect(page.getByTestId("uptime-bar-embed-url")).toHaveCount(0);
    await expect(page.getByTestId("uptime-bar-period-select")).toHaveCount(0);
    await expect(page.getByTestId("uptime-bar-width")).toHaveCount(0);

    // The embed URL never points at the removed /uptime-bar route.
    const urlText = await page.getByTestId("badge-embed-url").textContent();
    expect(urlText).not.toContain("/uptime-bar");
  });

  test("SVG download button triggers download", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const checkName = `Badge DL SVG ${Date.now()}`;
    const check = await createCheck(page, token, checkName);

    await page.goto(badgesUrl(check.uid));
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("badge-preview")).toBeVisible({
      timeout: 10000,
    });

    const downloadPromise = page.waitForEvent("download");
    await page.getByTestId("badge-download-svg").click();
    const download = await downloadPromise;
    expect(download.suggestedFilename()).toContain(".svg");
  });

  test("PNG download button triggers download", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const checkName = `Badge DL PNG ${Date.now()}`;
    const check = await createCheck(page, token, checkName);

    await page.goto(badgesUrl(check.uid));
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("badge-preview")).toBeVisible({
      timeout: 10000,
    });

    const downloadPromise = page.waitForEvent("download");
    await page.getByTestId("badge-download-png").click();
    const download = await downloadPromise;
    expect(download.suggestedFilename()).toContain(".png");
  });

  test("no Download card exists in DOM", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const checkName = `Badge No Card ${Date.now()}`;
    const check = await createCheck(page, token, checkName);

    await page.goto(badgesUrl(check.uid));
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("badge-preview")).toBeVisible({
      timeout: 10000,
    });

    // The old Download card title and description must not be present
    await expect(page.getByText("Download the badge in different formats")).not.toBeVisible();
  });

  test("period selector visible with Availability, hidden with only Status+Duration", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const checkName = `Badge Period Vis ${Date.now()}`;
    const check = await createCheck(page, token, checkName);

    await page.goto(badgesUrl(check.uid));
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("badge-preview")).toBeVisible({
      timeout: 10000,
    });

    // Only status checked by default — period hidden
    await expect(page.getByTestId("badge-period-select")).not.toBeVisible();

    // Enable Duration — still hidden (no period-gated component)
    await page.getByTestId("badge-component-duration").click();
    await expect(page.getByTestId("badge-period-select")).not.toBeVisible();

    // Enable Availability — period must appear
    await page.getByTestId("badge-component-availability").click();
    await expect(page.getByTestId("badge-period-select")).toBeVisible();

    // Uncheck Availability — period hides again
    await page.getByTestId("badge-component-availability").click();
    await expect(page.getByTestId("badge-period-select")).not.toBeVisible();
  });

  test("should restore state from URL on page load", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const checkName = `Badge Restore ${Date.now()}`;
    const check = await createCheck(page, token, checkName);

    // Navigate directly with all params in URL
    await page.goto(
      badgesUrl(
        check.uid,
        "?components=availability&period=7d&style=flat-square&label=My+Badge"
      )
    );
    await page.waitForLoadState("networkidle");

    // Availability should be checked
    await expect(page.getByTestId("badge-component-availability")).toBeChecked();

    // Period selector is visible and set to 7 days
    await expect(page.getByTestId("badge-period-select")).toBeVisible();
    await expect(page.getByTestId("badge-period-select")).toContainText("7 days");

    // Style is flat-square
    await expect(page.getByTestId("badge-style-select")).toContainText("Flat Square");

    // Custom label is filled
    await expect(page.getByTestId("badge-custom-label")).toHaveValue("My Badge");

    // Preview is showing
    await expect(page.getByTestId("badge-preview")).toBeVisible({
      timeout: 10000,
    });

    // Embed URL reflects the components
    const urlText = await page.getByTestId("badge-embed-url").textContent();
    expect(urlText).toContain("/badges/availability");
    expect(urlText).toContain("period=7d");
    expect(urlText).toContain("style=flat-square");
    expect(urlText).toContain("label=My+Badge");
  });

  test("should update custom label in URL", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const checkName = `Badge Label ${Date.now()}`;
    const check = await createCheck(page, token, checkName);

    await page.goto(badgesUrl(check.uid));
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("badge-preview")).toBeVisible({
      timeout: 10000,
    });

    // Type a custom label
    await page.getByTestId("badge-custom-label").fill("My Custom Badge");

    // Verify URL updated with label
    expect(new URL(page.url()).searchParams.get("label")).toBe(
      "My Custom Badge"
    );

    // Verify embed URL reflects the label
    const urlText = await page.getByTestId("badge-embed-url").textContent();
    expect(urlText).toContain("label=My+Custom+Badge");
  });

  test("should strip default values from URL", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const checkName = `Badge Defaults ${Date.now()}`;
    const check = await createCheck(page, token, checkName);

    // Navigate with non-default components
    await page.goto(
      badgesUrl(check.uid, "?components=availability&period=7d&style=flat-square")
    );
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("badge-preview")).toBeVisible({
      timeout: 10000,
    });

    // Switch back to only status by enabling status and disabling availability
    await page.getByTestId("badge-component-status").click();
    await page.getByTestId("badge-component-availability").click();

    // components param should be stripped (default "status")
    expect(new URL(page.url()).searchParams.has("components")).toBe(false);

    // Switch style back to flat (default)
    await page.getByTestId("badge-style-select").click();
    await page.getByRole("option", { name: "Flat", exact: true }).click();

    // style should be stripped from URL (it's the default)
    expect(new URL(page.url()).searchParams.has("style")).toBe(false);

    // The check stays in the path — it is not a search param any more.
    expect(new URL(page.url()).pathname).toBe(
      `${DASH_BASE}/orgs/test/checks/${check.uid}/badges`
    );
  });

  test("width input updates badge URL after blur", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const checkName = `Badge Width ${Date.now()}`;
    const check = await createCheck(page, token, checkName);

    // Enable uptime-bar so the width input is visible
    await page.goto(badgesUrl(check.uid, "?components=status,uptime-bar"));
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("badge-preview")).toBeVisible({
      timeout: 10000,
    });

    const widthInput = page.getByTestId("badge-width");
    await expect(widthInput).toBeVisible();

    // Clear the field, type a new value, and commit with Tab (blur)
    await widthInput.click({ clickCount: 3 });
    await widthInput.fill("500");
    await widthInput.press("Tab");

    // URL should now contain width=500
    await expect
      .poll(() => new URL(page.url()).searchParams.get("width"), { timeout: 5000 })
      .toBe("500");

    // Typing an out-of-range value and blurring should revert
    await widthInput.click({ clickCount: 3 });
    await widthInput.fill("30");
    await widthInput.press("Tab");

    await expect
      .poll(() => page.getByTestId("badge-width").inputValue(), { timeout: 3000 })
      .toBe("500");
  });

  test("breadcrumb reads Checks > check > Badges and the check crumb links back", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const checkName = `Badge Crumb ${Date.now()}`;
    const check = await createCheck(page, token, checkName);

    await page.goto(badgesUrl(check.uid));
    await page.waitForLoadState("networkidle");

    const header = page.locator("header");

    // The leaf crumb names this page (the old ad-hoc back-link is gone).
    await expect(page.getByTestId("badge-back-to-check")).toHaveCount(0);
    await expect(header.getByTestId("badge-breadcrumb")).toBeVisible();

    // The section crumb links back to the list...
    await expect(header.getByRole("link", { name: /^checks$/i })).toBeVisible();

    // ...and the check crumb links back to the check it belongs to.
    const checkCrumb = header.getByRole("link", { name: checkName });
    await expect(checkCrumb).toBeVisible();
    await checkCrumb.click();
    await page.waitForURL(`**${DASH_BASE}/orgs/test/checks/${check.uid}`);
    expect(new URL(page.url()).pathname).toBe(
      `${DASH_BASE}/orgs/test/checks/${check.uid}`
    );
  });

  test("downloads SVG of a multi-row badge", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const checkName = `Badge MultiRow DL ${Date.now()}`;
    const check = await createCheck(page, token, checkName);

    await page.goto(
      badgesUrl(check.uid, "?components=status,uptime-bar,response-time-graph")
    );
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("badge-preview")).toBeVisible({
      timeout: 10000,
    });

    // Embed URL carries all three tokens.
    const urlText = await page.getByTestId("badge-embed-url").textContent();
    expect(urlText).toContain("/badges/status,uptime-bar,response-time-graph");

    const downloadPromise = page.waitForEvent("download");
    await page.getByTestId("badge-download-svg").click();
    const download = await downloadPromise;
    expect(download.suggestedFilename()).toContain(".svg");
  });

  test("legacy /badges?check=<slug> redirects to the check's builder", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    // Create the target FIRST, then 25 newer checks so the target sorts past
    // index 20 (created_at DESC) and is absent from the first list page (20) —
    // the redirect resolves the slug with a direct fetch, not a list lookup.
    const targetName = `Badge OOP Slug ${Date.now()}`;
    const target = await createCheck(page, token, targetName);
    await createExtraChecks(page, token, `OOP Slug ${Date.now()}`, 25);

    // The legacy URL, with builder params that must survive the redirect.
    await page.goto(
      `${DASH_BASE}/orgs/test/badges?check=${target.slug}&components=availability&period=7d`
    );
    await page.waitForURL(`**${badgesUrl(target.uid)}*`, { timeout: 15000 });
    await page.waitForLoadState("networkidle");

    // The slug resolved to the canonical uid path...
    expect(new URL(page.url()).pathname).toBe(
      `${DASH_BASE}/orgs/test/checks/${target.uid}/badges`
    );
    // ...and every other search param came along.
    const params = new URL(page.url()).searchParams;
    expect(params.get("components")).toBe("availability");
    expect(params.get("period")).toBe("7d");
    expect(params.has("check")).toBe(false);

    // The builder renders for the resolved check.
    await expect(page.getByTestId("badge-preview")).toBeVisible({
      timeout: 10000,
    });
    await expect(page.getByTestId("badge-check-not-found")).toHaveCount(0);
    const urlText = await page.getByTestId("badge-embed-url").textContent();
    expect(urlText).toContain(`/checks/${target.slug}/badges/`);
  });

  test("legacy /badges?check=<uid> redirects to the check's builder", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    const targetName = `Badge OOP Uid ${Date.now()}`;
    const target = await createCheck(page, token, targetName);
    await createExtraChecks(page, token, `OOP Uid ${Date.now()}`, 25);

    // A uid deep link resolves identically to the slug case.
    await page.goto(`${DASH_BASE}/orgs/test/badges?check=${target.uid}`);
    await page.waitForURL(`**${badgesUrl(target.uid)}`, { timeout: 15000 });
    await page.waitForLoadState("networkidle");

    expect(new URL(page.url()).pathname).toBe(
      `${DASH_BASE}/orgs/test/checks/${target.uid}/badges`
    );
    await expect(page.getByTestId("badge-preview")).toBeVisible({
      timeout: 10000,
    });
    await expect(page.getByTestId("badge-check-not-found")).toHaveCount(0);
  });

  test("legacy /badges with no check lands on the check list", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto(`${DASH_BASE}/orgs/test/badges`);
    await page.waitForURL(`**${DASH_BASE}/orgs/test/checks`, { timeout: 15000 });
    await page.waitForLoadState("networkidle");

    expect(new URL(page.url()).pathname).toBe(`${DASH_BASE}/orgs/test/checks`);
    // No builder is rendered by the legacy route — it only redirects.
    await expect(page.getByTestId("badge-component-status")).toHaveCount(0);
  });

  test("unknown check in the path shows a not-found notice, not a blank pane", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    const missing = `does-not-exist-${Date.now()}`;
    await page.goto(badgesUrl(missing));
    await page.waitForLoadState("networkidle");

    // The route's 404: the not-found alert, no preview, and no redirect away.
    await expect(page.getByTestId("badge-check-not-found")).toBeVisible({
      timeout: 10000,
    });
    await expect(page.getByTestId("badge-preview")).toHaveCount(0);
    expect(new URL(page.url()).pathname).toBe(
      `${DASH_BASE}/orgs/test/checks/${missing}/badges`
    );
  });

  test("legacy /badges with an unknown check forwards to the route's 404", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    const missing = `does-not-exist-${Date.now()}`;
    await page.goto(`${DASH_BASE}/orgs/test/badges?check=${missing}`);
    await page.waitForURL(`**${badgesUrl(missing)}`, { timeout: 15000 });
    await page.waitForLoadState("networkidle");

    // One not-found state, owned by the builder route — the legacy URL has
    // none of its own.
    await expect(page.getByTestId("badge-check-not-found")).toBeVisible({
      timeout: 10000,
    });
  });
});
