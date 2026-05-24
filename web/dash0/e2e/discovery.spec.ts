import { test, expect } from "@playwright/test";

test.describe("Network Discovery", () => {
  test.beforeEach(async ({ page }) => {
    // Log in with test credentials.
    await page.goto("/dash0/orgs/test/login");
    await page.getByTestId("login-email").fill("test@test.com");
    await page.getByTestId("login-password").fill("test");
    await page.getByTestId("login-submit").click();
    await page.waitForURL((url) => !url.pathname.includes("login"));
  });

  test("discovery sidebar link is visible for admin", async ({ page }) => {
    await page.goto("/dash0/orgs/test");
    const sidebar = page.getByTestId("app-sidebar");
    await expect(sidebar).toBeVisible();
    // Discovery link should appear in the sidebar.
    const discoveryLink = page.getByRole("link", { name: /discovery/i });
    await expect(discoveryLink).toBeVisible();
  });

  test("can navigate to discovery index", async ({ page }) => {
    await page.goto("/dash0/orgs/test/discovery");
    await expect(page.getByRole("heading", { name: /network discovery/i })).toBeVisible();
    await expect(page.getByRole("link", { name: /new scan|start new scan/i })).toBeVisible();
  });

  test("discover via Freebox button is visible for admin", async ({ page }) => {
    await page.goto("/dash0/orgs/test/discovery");
    await expect(
      page.getByRole("button", { name: /discover via freebox/i }),
    ).toBeVisible();
  });

  test("source filter is visible on the scans list", async ({ page }) => {
    await page.goto("/dash0/orgs/test/discovery");
    // The source filter is a combobox (Radix Select) labelled "Filter by source".
    await expect(
      page.getByRole("combobox", { name: /filter by source/i }),
    ).toBeVisible();
  });

  test("can navigate to new scan form", async ({ page }) => {
    await page.goto("/dash0/orgs/test/discovery/new");
    await expect(page.getByLabel(/cidr/i)).toBeVisible();
    await expect(page.getByRole("checkbox")).toBeVisible();
    await expect(page.getByRole("button", { name: /start scan/i })).toBeDisabled();
  });

  test("start scan button is disabled without confirmation", async ({ page }) => {
    await page.goto("/dash0/orgs/test/discovery/new");
    await page.fill("textarea", "127.0.0.1/32");
    // Confirmation not checked — submit should be disabled.
    await expect(page.getByRole("button", { name: /start scan/i })).toBeDisabled();
  });

  test("start scan button enables after confirmation", async ({ page }) => {
    await page.goto("/dash0/orgs/test/discovery/new");
    await page.fill("textarea", "127.0.0.1/32");
    await page.getByRole("checkbox").check();
    await expect(page.getByRole("button", { name: /start scan/i })).toBeEnabled();
  });

  // Regression guard: the scan list rendered `scan.uid.slice(0, 8)`, which threw
  // "Cannot read properties of undefined (reading 'slice')" when the API serialized
  // the job model with Go field casing (`UID`) instead of camelCase (`uid`).
  test("renders the scan list without crashing after a scan is created", async ({ page }) => {
    const pageErrors: Error[] = [];
    page.on("pageerror", (err) => pageErrors.push(err));

    // Create a scan through the form; on success it navigates to the detail page.
    await page.goto("/dash0/orgs/test/discovery/new");
    await page.fill("textarea", "127.0.0.1/32");
    await page.getByRole("checkbox").check();
    await page.getByRole("button", { name: /start scan/i }).click();

    await page.waitForURL(/\/discovery\/[0-9a-f-]{36}$/);
    const jobUid = page.url().split("/").pop() as string;
    await expect(page.getByRole("heading", { name: /scan details/i })).toBeVisible();
    // The uid is rendered on the detail page — blank if the API used the wrong casing.
    await expect(page.getByText(jobUid)).toBeVisible();

    // Back on the index, the row must render the truncated uid without throwing.
    await page.goto("/dash0/orgs/test/discovery");
    await expect(page.getByRole("heading", { name: /network discovery/i })).toBeVisible();
    const table = page.getByRole("table");
    await expect(table).toBeVisible();
    await expect(table.getByText(jobUid.slice(0, 8))).toBeVisible();

    expect(
      pageErrors,
      `unexpected page errors: ${pageErrors.map((e) => e.message).join(", ")}`,
    ).toHaveLength(0);
  });
});
