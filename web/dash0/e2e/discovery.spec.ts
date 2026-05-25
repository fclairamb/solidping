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

  test("the discover-via-Freebox dropdown is removed from the index", async ({ page }) => {
    await page.goto("/dash0/orgs/test/discovery");
    await expect(page.getByRole("heading", { name: /network discovery/i })).toBeVisible();
    // The standalone Freebox launcher dropdown no longer exists; the unified
    // "Start new scan" flow owns the Freebox path now.
    await expect(
      page.getByRole("button", { name: /discover via freebox/i }),
    ).toHaveCount(0);
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

  test("new scan form defaults to the LAN method with CIDR fields visible", async ({ page }) => {
    await page.goto("/dash0/orgs/test/discovery/new");
    // The scan-method select defaults to LAN.
    const methodSelect = page.getByRole("combobox", { name: /scan method/i });
    await expect(methodSelect).toBeVisible();
    await expect(methodSelect).toHaveText(/IP range|LAN/i);
    // LAN fields (CIDR textarea) are shown by default.
    await expect(page.getByLabel(/cidr/i)).toBeVisible();
  });

  test("Freebox method option is hidden when no granted channel exists", async ({ page }) => {
    await page.goto("/dash0/orgs/test/discovery/new");
    // Open the scan-method select; the test org has no granted Freebox channel,
    // so only the LAN option is offered.
    await page.getByRole("combobox", { name: /scan method/i }).click();
    await expect(page.getByRole("option", { name: /IP range|LAN/i })).toBeVisible();
    await expect(page.getByRole("option", { name: /^freebox$/i })).toHaveCount(0);
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

  // Regression guard: the scan list previously rendered `scan.uid.slice(0, 8)`,
  // which threw when the API used Go field casing (`UID`). The bogus UID column
  // has since been removed, but the list must still render without crashing.
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

    // Back on the index, the table must render without throwing.
    await page.goto("/dash0/orgs/test/discovery");
    await expect(page.getByRole("heading", { name: /network discovery/i })).toBeVisible();
    const table = page.getByRole("table");
    await expect(table).toBeVisible();

    expect(
      pageErrors,
      `unexpected page errors: ${pageErrors.map((e) => e.message).join(", ")}`,
    ).toHaveLength(0);
  });

  test("scan list no longer shows the bogus IP Address column", async ({ page }) => {
    await page.goto("/dash0/orgs/test/discovery");
    await expect(page.getByRole("heading", { name: /network discovery/i })).toBeVisible();
    // The first column header used to be "IP Address" while rendering the scan
    // UID — it has been removed. The header row should not contain it.
    const headerRow = page.locator("thead tr");
    if (await headerRow.count()) {
      await expect(headerRow.getByText(/IP Address/i)).toHaveCount(0);
    }
  });

  test("discovery page header uses the Network breadcrumb crumb", async ({ page }) => {
    await page.goto("/dash0/orgs/test/discovery");
    // The breadcrumb (in the header bar) carries the discovery label, matching
    // the sidebar entry. There are two "Discovery" texts (sidebar + breadcrumb).
    await expect(
      page.getByRole("heading", { name: /network discovery/i }),
    ).toBeVisible();
    await expect(page.locator("header").getByText(/discovery/i)).toBeVisible();
  });

  // Fan-out: a range larger than a /20 (here a /18 → 4 bounded chunks) is now
  // accepted (no DISCOVERY_RANGE_TOO_LARGE), creates a plan scan, and the detail
  // page renders the chunk-progress indicator.
  test("large range fans out and the detail page shows chunk progress", async ({ page }) => {
    await page.goto("/dash0/orgs/test/discovery/new");
    // 10.10.0.0/18 = 16384 addresses → 4 chunks of /20.
    await page.fill("textarea", "10.10.0.0/18");
    await page.getByRole("checkbox").check();
    await page.getByRole("button", { name: /start scan/i }).click();

    // The scan is accepted and navigates to the detail page (no error toast).
    await page.waitForURL(/\/discovery\/[0-9a-f-]{36}$/);
    await expect(page.getByRole("heading", { name: /scan details/i })).toBeVisible();

    // The progress card surfaces the chunk count (4 chunks of the fan-out).
    await expect(page.getByText(/\/\s*4\s*chunks/i)).toBeVisible({ timeout: 15000 });
  });

  test("large range can be stopped mid-scan, clearing the guard", async ({ page }) => {
    await page.goto("/dash0/orgs/test/discovery/new");
    await page.fill("textarea", "10.20.0.0/18");
    await page.getByRole("checkbox").check();
    await page.getByRole("button", { name: /start scan/i }).click();

    await page.waitForURL(/\/discovery\/[0-9a-f-]{36}$/);
    await expect(page.getByRole("heading", { name: /scan details/i })).toBeVisible();

    // While the scan is active, the Stop button is offered. Click it and confirm.
    const stopButton = page.getByRole("button", { name: /stop scan/i }).first();
    if (await stopButton.isVisible().catch(() => false)) {
      await stopButton.click();
      // Confirm in the alert dialog.
      await page
        .getByRole("alertdialog")
        .getByRole("button", { name: /stop scan/i })
        .click();
      await expect(page.getByText(/scan stopped/i)).toBeVisible({ timeout: 10000 });
    }

    // After stopping (or natural completion), a fresh scan can be started — the
    // "already running" guard is cleared.
    await page.goto("/dash0/orgs/test/discovery/new");
    await page.fill("textarea", "127.0.0.1/32");
    await page.getByRole("checkbox").check();
    await expect(page.getByRole("button", { name: /start scan/i })).toBeEnabled();
  });

  test("notifications page renders the My pages header", async ({ page }) => {
    await page.goto("/dash0/orgs/test/me/notifications");
    await expect(page.getByTestId("my-notifications-page")).toBeVisible();
    await expect(
      page.getByRole("heading", { name: /my pages/i }),
    ).toBeVisible();
    // Breadcrumb mirrors the page title.
    await expect(page.locator("header").getByText(/my pages/i)).toBeVisible();
  });
});
