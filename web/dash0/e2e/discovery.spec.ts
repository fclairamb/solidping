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
  test("large range fans out into chunks and can be stopped mid-scan", async ({ page }) => {
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

    // The new-scan form re-arms: its Start button is gated only by the confirm
    // checkbox, not by a sticky client-side guard.
    await page.goto("/dash0/orgs/test/discovery/new");
    await page.fill("textarea", "127.0.0.1/32");
    await page.getByRole("checkbox").check();
    await expect(page.getByRole("button", { name: /start scan/i })).toBeEnabled();
  });

  test("entering a /8 CIDR shows the large-range warning with host and chunk estimate", async ({ page }) => {
    await page.goto("/dash0/orgs/test/discovery/new");
    // 10.0.0.0/8 = 16,777,216 addresses → 4096 chunks.
    await page.fill("textarea", "10.0.0.0/8");

    // The large-range warning Alert should appear.
    const warning = page.getByTestId("large-range-warning");
    await expect(warning).toBeVisible();
    // Should mention host count (16M+) and 4096 chunks.
    await expect(warning).toContainText(/16/);
    await expect(warning).toContainText(/4.096/);

    // Submission is still gated only by the confirm checkbox.
    await expect(page.getByRole("button", { name: /start scan/i })).toBeDisabled();
    await page.getByRole("checkbox").check();
    await expect(page.getByRole("button", { name: /start scan/i })).toBeEnabled();
  });

  // A stable, seeded completed scan with two suggested checks grouped under
  // 127.0.0.1 — lets the detail-page tests load directly without creating a scan
  // (which is gated by the one-active-scan-per-org rule and would flake).
  const SEEDED_SCAN_UID = "00000000-0000-0000-0000-000000000007";

  // Detail-page header: the back arrow lives as the leftmost item of the
  // right-aligned action cluster (not on the far left), and the Refresh button
  // is a full labelled button on desktop, icon-only on mobile.
  test("detail header places the back arrow in the right cluster and labels refresh on desktop", async ({
    page,
  }) => {
    await page.goto(`/dash0/orgs/test/discovery/${SEEDED_SCAN_UID}`);
    await expect(page.getByRole("heading", { name: /scan details/i })).toBeVisible();

    // The back arrow is rendered (ghost icon button with aria-label "Back").
    const backButton = page.getByRole("link", { name: /^back$/i });
    await expect(backButton).toBeVisible();

    // It sits inside the right-aligned cluster, *after* the title, not before it.
    const heading = page.getByRole("heading", { name: /scan details/i });
    const headingBox = await heading.boundingBox();
    const backBox = await backButton.boundingBox();
    expect(headingBox).not.toBeNull();
    expect(backBox).not.toBeNull();
    expect(backBox!.x).toBeGreaterThan(headingBox!.x);

    // At desktop width the Refresh button shows its "Refresh" label.
    const refreshLabel = page.getByText(/^refresh$/i);
    await expect(refreshLabel).toBeVisible();

    // The back arrow navigates back to the discovery index.
    await backButton.click();
    await page.waitForURL(/\/discovery$/);
    await expect(
      page.getByRole("heading", { name: /network discovery/i }),
    ).toBeVisible();
  });

  test("detail header refresh button is icon-only on mobile widths", async ({ page }) => {
    // Narrow the viewport below the Tailwind `sm` (640px) breakpoint.
    await page.setViewportSize({ width: 390, height: 800 });

    await page.goto(`/dash0/orgs/test/discovery/${SEEDED_SCAN_UID}`);
    await expect(page.getByRole("heading", { name: /scan details/i })).toBeVisible();

    // The Refresh button is still present (accessible via its aria-label) but
    // its text label is hidden at mobile width.
    await expect(
      page.getByRole("button", { name: /^refresh$/i }),
    ).toBeVisible();
    await expect(page.getByText(/^refresh$/i)).toBeHidden();
  });

  // The seeded scan renders its suggested checks GROUPED under 127.0.0.1, with a
  // group header carrying the source badge and per-check rows beneath.
  test("scan detail renders discovered checks grouped by host", async ({ page }) => {
    await page.goto(`/dash0/orgs/test/discovery/${SEEDED_SCAN_UID}`);
    await expect(page.getByRole("heading", { name: /scan details/i })).toBeVisible();

    // A group card for 127.0.0.1 is shown.
    const group = page.getByTestId("discovery-group").filter({ hasText: "127.0.0.1" });
    await expect(group.first()).toBeVisible();

    // It contains per-check rows (the seed has a TCP and an ICMP suggestion).
    const checkRows = group.first().getByTestId("discovery-check-row");
    await expect(checkRows.first()).toBeVisible();
    expect(await checkRows.count()).toBeGreaterThanOrEqual(2);
  });

  // The group header offers "select all in group", which arms the Promote button.
  test("selecting a whole group enables the Promote action", async ({ page }) => {
    await page.goto(`/dash0/orgs/test/discovery/${SEEDED_SCAN_UID}`);
    await expect(page.getByRole("heading", { name: /scan details/i })).toBeVisible();

    const promoteButton = page.getByRole("button", { name: /promote selected/i });
    await expect(promoteButton).toBeDisabled();

    // Tick the group's "select all" checkbox.
    await page
      .getByTestId("discovery-group")
      .filter({ hasText: "127.0.0.1" })
      .first()
      .getByRole("checkbox", { name: /select all in group/i })
      .check();

    await expect(promoteButton).toBeEnabled();
  });

  // Container discovery: the registry-driven method picker offers "Containers",
  // and selecting it reveals the Docker-endpoint textarea prefilled with the
  // local socket. The test org has no Docker dependency for this form-level check.
  test("container method is offered and reveals the host textarea", async ({ page }) => {
    await page.goto("/dash0/orgs/test/discovery/new");
    await expect(page.getByRole("combobox", { name: /scan method/i })).toBeVisible();

    // Open the method select and pick Containers.
    await page.getByRole("combobox", { name: /scan method/i }).click();
    const containerOption = page.getByRole("option", { name: /containers/i });
    await expect(containerOption).toBeVisible();
    await containerOption.click();

    // The container host textarea is shown, prefilled with the local socket.
    const hostsField = page.getByLabel(/container host/i);
    await expect(hostsField).toBeVisible();
    await expect(hostsField).toHaveValue(/unix:\/\/\/var\/run\/docker\.sock/);
  });

  test("container scan start button arms only after confirmation", async ({ page }) => {
    await page.goto("/dash0/orgs/test/discovery/new");
    await page.getByRole("combobox", { name: /scan method/i }).click();
    await page.getByRole("option", { name: /containers/i }).click();

    // Hosts are prefilled, but confirmation is still required.
    await expect(page.getByRole("button", { name: /start scan/i })).toBeDisabled();
    await page.getByRole("checkbox").check();
    await expect(page.getByRole("button", { name: /start scan/i })).toBeEnabled();
  });

  test("Kubernetes method option is hidden when no cluster connection exists", async ({
    page,
  }) => {
    await page.goto("/dash0/orgs/test/discovery/new");
    // Open the scan-method select; the test org has no kubernetes cluster
    // connection, so the Kubernetes option is not offered (capability-gated).
    await page.getByRole("combobox", { name: /scan method/i }).click();
    await expect(
      page.getByRole("option", { name: /IP range|LAN/i }),
    ).toBeVisible();
    await expect(
      page.getByRole("option", { name: /^kubernetes$/i }),
    ).toHaveCount(0);
  });

  test("source filter includes the registry sources on the scans list", async ({
    page,
  }) => {
    await page.goto("/dash0/orgs/test/discovery");
    // The source filter is registry-driven; opening it shows the kubernetes
    // source (registered by the kubernetes discovery type) alongside the rest.
    await page.getByRole("combobox", { name: /filter by source/i }).click();
    await expect(
      page.getByRole("option", { name: /^kubernetes$/i }),
    ).toBeVisible();
  });

  // The index "Start new scan" button mirrors every other primary "New X"
  // button: icon-only below the Tailwind `sm` (640px) breakpoint, full label at
  // and above it. Its accessible name ("Start new scan") survives at every width
  // via the aria-label on the link.
  test("index Start-new-scan button is icon-only on mobile and labelled on desktop", async ({
    page,
  }) => {
    // Mobile width: the button is present (by accessible name) but its text label
    // is hidden.
    await page.setViewportSize({ width: 390, height: 800 });
    await page.goto("/dash0/orgs/test/discovery");
    await expect(
      page.getByRole("heading", { name: /network discovery/i }),
    ).toBeVisible();
    await expect(
      page.getByRole("link", { name: /start new scan/i }),
    ).toBeVisible();
    await expect(page.getByText(/start new scan/i)).toBeHidden();

    // Desktop width: the text label is revealed.
    await page.setViewportSize({ width: 1280, height: 800 });
    await expect(page.getByText(/start new scan/i)).toBeVisible();
  });

  // Clicking anywhere in a scan row navigates to that scan's detail page (the
  // row is the link target; there is no trailing "View checks" cell anymore).
  test("clicking a scan row navigates to the scan detail page", async ({
    page,
  }) => {
    // Seed a row by creating a scan (navigates to the detail page on success).
    await page.goto("/dash0/orgs/test/discovery/new");
    await page.fill("textarea", "127.0.0.1/32");
    await page.getByRole("checkbox").check();
    await page.getByRole("button", { name: /start scan/i }).click();
    await page.waitForURL(/\/discovery\/[0-9a-f-]{36}$/);

    // Back on the index, click the first body row — the whole row is clickable.
    await page.goto("/dash0/orgs/test/discovery");
    await expect(page.getByRole("table")).toBeVisible();
    const firstRow = page.locator("tbody tr").first();
    await expect(firstRow).toBeVisible();
    await firstRow.click();

    await page.waitForURL(/\/discovery\/[0-9a-f-]{36}$/);
    await expect(
      page.getByRole("heading", { name: /scan details/i }),
    ).toBeVisible();
  });

  // The redundant "View checks" link and the LAN-only "CIDRs" column header are
  // both gone — superseded by the clickable row and the generic "Details" column.
  test("scan list has no View-checks link and no CIDRs column header", async ({
    page,
  }) => {
    await page.goto("/dash0/orgs/test/discovery");
    await expect(
      page.getByRole("heading", { name: /network discovery/i }),
    ).toBeVisible();

    await expect(
      page.getByRole("link", { name: /view checks/i }),
    ).toHaveCount(0);

    const headerRow = page.locator("thead tr");
    if (await headerRow.count()) {
      await expect(headerRow.getByText(/^CIDRs$/i)).toHaveCount(0);
      // The generic "Details" header replaces it.
      await expect(headerRow.getByText(/^Details$/i)).toBeVisible();
    }
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
