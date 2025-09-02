import { test, expect } from "./fixtures";
import type { Page } from "@playwright/test";

const API_BASE = "http://localhost:4000";

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
    await expect(page.getByTestId("badge-format-select")).toBeVisible();
    await expect(page.getByTestId("badge-style-select")).toBeVisible();
    await expect(page.getByTestId("badge-custom-label")).toBeVisible();

    // Verify placeholder text when no check is selected
    await expect(
      page.getByText("Select a check to preview and generate badges")
    ).toBeVisible();
  });

  test("should select a check and sync to URL", async ({
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

    // Verify download buttons appear
    await expect(page.getByTestId("badge-download-svg")).toBeVisible();
    await expect(page.getByTestId("badge-download-png")).toBeVisible();
    await expect(page.getByTestId("badge-download-jpg")).toBeVisible();

    // Verify embed URL contains the check identifier and format
    const urlText = await page.getByTestId("badge-embed-url").textContent();
    expect(urlText).toContain("/badges/status");
    expect(urlText).toContain("/orgs/test/checks/");
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
      `/dash0/orgs/test/badges?check=${slug}&format=availability&period=7d&style=flat-square&label=My+Badge`
    );
    await page.waitForLoadState("networkidle");

    // Verify check is pre-selected
    await expect(page.getByTestId("badge-check-select")).toContainText(
      checkName,
      { timeout: 10000 }
    );

    // Verify format is set to availability
    await expect(page.getByTestId("badge-format-select")).toContainText(
      "Availability"
    );

    // Verify period selector is visible (availability format) and set to 7 days
    await expect(page.getByTestId("badge-period-select")).toBeVisible();
    await expect(page.getByTestId("badge-period-select")).toContainText(
      "7 days"
    );

    // Verify style is set to flat-square
    await expect(page.getByTestId("badge-style-select")).toContainText(
      "Flat Square"
    );

    // Verify custom label is filled
    await expect(page.getByTestId("badge-custom-label")).toHaveValue(
      "My Badge"
    );

    // Verify preview is showing
    await expect(page.getByTestId("badge-preview-img")).toBeVisible({
      timeout: 10000,
    });

    // Verify embed URL reflects all params
    const urlText = await page.getByTestId("badge-embed-url").textContent();
    expect(urlText).toContain("/badges/availability");
    expect(urlText).toContain("period=7d");
    expect(urlText).toContain("style=flat-square");
    expect(urlText).toContain("label=My+Badge");
  });

  test("should change format and update URL", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const checkName = `Badge Format ${Date.now()}`;
    const check = await createCheck(page, token, checkName);

    await page.goto(`/dash0/orgs/test/badges?check=${check.slug}`);
    await page.waitForLoadState("networkidle");

    // Wait for check to load
    await expect(page.getByTestId("badge-preview-img")).toBeVisible({
      timeout: 10000,
    });

    // Default format is "status" - period select should NOT be visible
    await expect(page.getByTestId("badge-period-select")).not.toBeVisible();

    // URL should not have format param (default is stripped)
    expect(new URL(page.url()).searchParams.has("format")).toBe(false);

    // Switch to availability format
    await page.getByTestId("badge-format-select").click();
    await page.getByRole("option", { name: /^AvailabilityUptime/ }).click();

    // Period select should now be visible
    await expect(page.getByTestId("badge-period-select")).toBeVisible();

    // URL should now have format=availability
    expect(new URL(page.url()).searchParams.get("format")).toBe("availability");

    // Verify embed URL updated
    const availUrl = await page.getByTestId("badge-embed-url").textContent();
    expect(availUrl).toContain("/badges/availability");
  });

  test("should change period and style, updating URL", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const checkName = `Badge Options ${Date.now()}`;
    const check = await createCheck(page, token, checkName);

    // Start with availability format to have the period selector
    await page.goto(
      `/dash0/orgs/test/badges?check=${check.slug}&format=availability`
    );
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("badge-preview-img")).toBeVisible({
      timeout: 10000,
    });

    // Change period to 7 days
    await page.getByTestId("badge-period-select").click();
    await page.getByRole("option", { name: "7 days" }).click();

    // Verify URL updated
    expect(new URL(page.url()).searchParams.get("period")).toBe("7d");

    // Verify embed URL
    const url7d = await page.getByTestId("badge-embed-url").textContent();
    expect(url7d).toContain("period=7d");

    // Change style to flat-square
    await page.getByTestId("badge-style-select").click();
    await page.getByRole("option", { name: "Flat Square" }).click();

    // Verify URL updated
    expect(new URL(page.url()).searchParams.get("style")).toBe("flat-square");

    // Verify embed URL
    const urlStyled = await page.getByTestId("badge-embed-url").textContent();
    expect(urlStyled).toContain("style=flat-square");
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

    // Navigate with all explicit defaults
    await page.goto(
      `/dash0/orgs/test/badges?check=${check.slug}&format=availability&period=7d&style=flat-square`
    );
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("badge-preview-img")).toBeVisible({
      timeout: 10000,
    });

    // Switch format back to status (default)
    await page.getByTestId("badge-format-select").click();
    await page.getByRole("option", { name: /^StatusCurrent/ }).click();

    // format should be stripped from URL (it's the default)
    expect(new URL(page.url()).searchParams.has("format")).toBe(false);

    // Switch style back to flat (default)
    await page.getByTestId("badge-style-select").click();
    await page.getByRole("option", { name: "Flat", exact: true }).click();

    // style should be stripped from URL (it's the default)
    expect(new URL(page.url()).searchParams.has("style")).toBe(false);

    // check param should remain
    expect(new URL(page.url()).searchParams.get("check")).toBe(check.slug);
  });
});
