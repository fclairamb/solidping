import { test, expect, API_BASE } from "./fixtures";
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
  const randomSuffix = Math.random().toString(36).substring(7);
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

test.describe("Badges", () => {
  test.describe.configure({ mode: "serial" });

  test("should display the badges page and navigate via sidebar", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Navigate via sidebar
    await page
      .getByTestId("app-sidebar")
      .getByRole("link", { name: "Badges" })
      .click();

    await page.waitForURL(/\/badges/);
    await page.waitForLoadState("networkidle");

    // Verify page heading
    await expect(
      page.getByRole("heading", { name: "Badges", exact: true })
    ).toBeVisible();

    // Verify configuration panel is visible
    await expect(page.getByTestId("badge-check-select")).toBeVisible();

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

    // Verify placeholder text when no check is selected
    await expect(
      page.getByText("Select a check to preview and generate badges")
    ).toBeVisible();

    // Download card must NOT exist
    await expect(page.getByText("Download the badge in different formats")).not.toBeVisible();
  });

  test("should select a check and show preview with download buttons in header", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const checkName = `Badge E2E ${Date.now()}`;
    const check = await createCheck(page, token, checkName);

    // Navigate to badges page
    await page.goto(`/dash0/orgs/test/badges`);
    await page.waitForLoadState("networkidle");

    // Select the check
    await page.getByTestId("badge-check-select").click();
    await page.getByRole("option", { name: checkName }).click();

    // Verify check was selected
    await expect(page.getByTestId("badge-check-select")).toContainText(
      checkName,
      { timeout: 5000 }
    );

    // Verify preview appears
    await expect(page.getByTestId("badge-preview-img")).toBeVisible({
      timeout: 10000,
    });

    // Verify URL updated with check param (slug preferred over uid)
    const url = new URL(page.url());
    const checkParam = url.searchParams.get("check");
    expect(checkParam).toBe(check.slug || check.uid);

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
    expect(urlText).toContain("/orgs/test/checks/");
  });

  test("should toggle Availability checkbox on and update preview URL", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const checkName = `Badge Avail ${Date.now()}`;
    const check = await createCheck(page, token, checkName);

    await page.goto(`/dash0/orgs/test/badges?check=${check.slug}`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("badge-preview-img")).toBeVisible({
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

    await page.goto(`/dash0/orgs/test/badges?check=${check.slug}`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("badge-preview-img")).toBeVisible({
      timeout: 10000,
    });

    // Status is the only token; unchecking it must not loop — it falls back to status.
    await page.getByTestId("badge-component-status").click();

    // Status remains checked (fallback) and the embed URL still points at status.
    await expect(page.getByTestId("badge-component-status")).toBeChecked();
    const urlText = await page.getByTestId("badge-embed-url").textContent();
    expect(urlText).toContain("/badges/status");
  });

  test("toggling uptime-bar and response-time-graph grows the preview", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const checkName = `Badge Rows ${Date.now()}`;
    const check = await createCheck(page, token, checkName);

    await page.goto(`/dash0/orgs/test/badges?check=${check.slug}`);
    await page.waitForLoadState("networkidle");
    const img = page.getByTestId("badge-preview-img");
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

    await page.goto(`/dash0/orgs/test/badges?check=${check.slug}`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("badge-preview-img")).toBeVisible({
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

    await page.goto(`/dash0/orgs/test/badges?check=${check.slug}`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("badge-preview-img")).toBeVisible({
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

    await page.goto(`/dash0/orgs/test/badges?check=${check.slug}`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("badge-preview-img")).toBeVisible({
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

    await page.goto(`/dash0/orgs/test/badges?check=${check.slug}`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("badge-preview-img")).toBeVisible({
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

    await page.goto(`/dash0/orgs/test/badges?check=${check.slug}`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("badge-preview-img")).toBeVisible({
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
    const slug = check.slug;

    // Navigate directly with all params in URL
    await page.goto(
      `/dash0/orgs/test/badges?check=${slug}&components=availability&period=7d&style=flat-square&label=My+Badge`
    );
    await page.waitForLoadState("networkidle");

    // Verify check is pre-selected
    await expect(page.getByTestId("badge-check-select")).toContainText(
      checkName,
      { timeout: 10000 }
    );

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
    await expect(page.getByTestId("badge-preview-img")).toBeVisible({
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

    await page.goto(`/dash0/orgs/test/badges?check=${check.slug}`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("badge-preview-img")).toBeVisible({
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
      `/dash0/orgs/test/badges?check=${check.slug}&components=availability&period=7d&style=flat-square`
    );
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("badge-preview-img")).toBeVisible({
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

    // check param should remain
    expect(new URL(page.url()).searchParams.get("check")).toBe(check.slug);
  });

  test("width input updates badge URL after blur", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const checkName = `Badge Width ${Date.now()}`;
    const check = await createCheck(page, token, checkName);

    // Enable uptime-bar so the width input is visible
    await page.goto(
      `/dash0/orgs/test/badges?check=${check.slug}&components=status,uptime-bar`
    );
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("badge-preview-img")).toBeVisible({
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

  test("back-to-check link is shown for the selected check and navigates to it", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const checkName = `Badge Back Link ${Date.now()}`;
    const check = await createCheck(page, token, checkName);

    await page.goto(`/dash0/orgs/test/badges?check=${check.slug}`);
    await page.waitForLoadState("networkidle");

    // Link is visible, shows the check name, and uses the back arrow.
    const backLink = page.getByTestId("badge-back-to-check");
    await expect(backLink).toBeVisible();
    await expect(backLink).toHaveText(checkName);

    // Clicking navigates to the canonical check detail route keyed on uid.
    await backLink.click();
    await page.waitForURL(`**/dash0/orgs/test/checks/${check.uid}`);
    expect(new URL(page.url()).pathname).toBe(
      `/dash0/orgs/test/checks/${check.uid}`
    );
  });

  test("back-to-check link is absent when no check is selected", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto(`/dash0/orgs/test/badges`);
    await page.waitForLoadState("networkidle");

    // No check selected → the back link must not exist.
    await expect(page.getByTestId("badge-back-to-check")).toHaveCount(0);
  });

  test("downloads SVG of a multi-row badge", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const checkName = `Badge MultiRow DL ${Date.now()}`;
    const check = await createCheck(page, token, checkName);

    await page.goto(
      `/dash0/orgs/test/badges?check=${check.slug}&components=status,uptime-bar,response-time-graph`
    );
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("badge-preview-img")).toBeVisible({
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

  test("deep-link by slug resolves a check beyond the first list page", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    // Create the target FIRST, then 25 newer checks so the target sorts past
    // index 20 (created_at DESC) and is absent from the first list page (20).
    const targetName = `Badge OOP Slug ${Date.now()}`;
    const target = await createCheck(page, token, targetName);
    await createExtraChecks(page, token, `OOP Slug ${Date.now()}`, 25);

    // Deep-link by slug to the out-of-page check.
    await page.goto(`/dash0/orgs/test/badges?check=${target.slug}`);
    await page.waitForLoadState("networkidle");

    // The preview + embed URL render — the "select a check" prompt is NOT shown.
    await expect(page.getByTestId("badge-preview-img")).toBeVisible({
      timeout: 10000,
    });
    await expect(page.getByTestId("badge-embed-url")).toBeVisible();
    await expect(
      page.getByText("Select a check to preview and generate badges")
    ).not.toBeVisible();
    await expect(page.getByTestId("badge-check-not-found")).toHaveCount(0);

    // The embed URL points at the target check's identifier.
    const urlText = await page.getByTestId("badge-embed-url").textContent();
    expect(urlText).toContain(`/checks/${target.slug}/badges/`);

    // The dropdown trigger shows the deep-linked check's name (merge path:
    // it isn't in the first list page but is merged into the options).
    await expect(page.getByTestId("badge-check-select")).toContainText(
      targetName,
      { timeout: 5000 }
    );
  });

  test("deep-link by uid resolves a check beyond the first list page", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    const targetName = `Badge OOP Uid ${Date.now()}`;
    const target = await createCheck(page, token, targetName);
    await createExtraChecks(page, token, `OOP Uid ${Date.now()}`, 25);

    // Deep-link by uid resolves identically to the slug case.
    await page.goto(`/dash0/orgs/test/badges?check=${target.uid}`);
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("badge-preview-img")).toBeVisible({
      timeout: 10000,
    });
    await expect(
      page.getByText("Select a check to preview and generate badges")
    ).not.toBeVisible();
    await expect(page.getByTestId("badge-check-not-found")).toHaveCount(0);

    // Dropdown trigger shows the resolved check's name.
    await expect(page.getByTestId("badge-check-select")).toContainText(
      targetName,
      { timeout: 5000 }
    );
  });

  test("unknown check param shows a not-found notice, not a blank pane", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto(`/dash0/orgs/test/badges?check=does-not-exist-${Date.now()}`);
    await page.waitForLoadState("networkidle");

    // The not-found alert is shown; no preview and no redirect away from /badges.
    await expect(page.getByTestId("badge-check-not-found")).toBeVisible({
      timeout: 10000,
    });
    await expect(page.getByTestId("badge-preview-img")).toHaveCount(0);
    await expect(
      page.getByText("Select a check to preview and generate badges")
    ).not.toBeVisible();
    expect(new URL(page.url()).pathname).toBe("/dash0/orgs/test/badges");
  });
});
