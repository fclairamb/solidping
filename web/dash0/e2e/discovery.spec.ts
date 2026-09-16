import { test, expect, type Page } from "@playwright/test";
import { API_BASE, DASH_BASE, uniqueStamp } from "./fixtures";

// The backend serializes discovery scans per org (409 DISCOVERY_ALREADY_RUNNING
// while any plan or chunk job is live), so a test that starts a scan must wait
// for the previous tests' scans to drain first — the scans LIST only shows plan
// jobs, so poll the jobs API where chunk children are visible too. Stopping a
// scan now cancels its running chunks (worker cancellation watcher), so this
// settles in seconds.
async function waitForScanQuiescence(page: Page) {
  const login = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  const { accessToken, organization } = await login.json();

  await expect
    .poll(
      async () => {
        const r = await page.request.get(
          `${API_BASE}/api/v1/orgs/${organization.uid}/jobs`,
          { headers: { Authorization: `Bearer ${accessToken}` } },
        );
        const jobs = ((await r.json()).data ?? []) as Array<{
          type: string;
          status: string;
        }>;
        return jobs.filter(
          (j) =>
            (j.type === "network_discovery" ||
              j.type === "network_discovery_plan") &&
            (j.status === "pending" || j.status === "running"),
        ).length;
      },
      {
        timeout: 30000,
        message: "waiting for prior discovery scans to drain",
      },
    )
    .toBe(0);
}

test.describe("Network Discovery", () => {
  test.beforeEach(async ({ page }) => {
    // Log in with test credentials.
    await page.goto(`${DASH_BASE}/orgs/test/login`);
    await page.getByTestId("login-email").fill("test@test.com");
    await page.getByTestId("login-password").fill("test");
    await page.getByTestId("login-submit").click();
    await page.waitForURL((url) => !url.pathname.includes("login"));
  });

  test("discovery tab is visible under Organization for admin", async ({ page }) => {
    // Discovery lives under the Organization tab row now (spec
    // 2026-09-16-09), guarded by the same admin-only layout as every other
    // organization tab — the test user is an admin of "test", so it's
    // reachable the same way as the other tabs.
    await page.goto(`${DASH_BASE}/orgs/test/organization`);
    const tabNav = page.getByTestId("tab-nav");
    await expect(tabNav).toBeVisible();
    const discoveryTab = tabNav.getByRole("link", { name: /discovery/i });
    await expect(discoveryTab).toBeVisible();
  });

  test("can navigate to discovery index", async ({ page }) => {
    await page.goto(`${DASH_BASE}/orgs/test/organization/discovery`);
    await expect(page.getByRole("heading", { name: /network discovery/i })).toBeVisible();
    await expect(page.getByRole("link", { name: /new scan|start new scan/i })).toBeVisible();
  });

  test("the discover-via-Freebox dropdown is removed from the index", async ({ page }) => {
    await page.goto(`${DASH_BASE}/orgs/test/organization/discovery`);
    await expect(page.getByRole("heading", { name: /network discovery/i })).toBeVisible();
    // The standalone Freebox launcher dropdown no longer exists; the unified
    // "Start new scan" flow owns the Freebox path now.
    await expect(
      page.getByRole("button", { name: /discover via freebox/i }),
    ).toHaveCount(0);
  });

  test("source filter is visible on the scans list", async ({ page }) => {
    await page.goto(`${DASH_BASE}/orgs/test/organization/discovery`);
    // The source filter is a combobox (Radix Select) labelled "Filter by source".
    await expect(
      page.getByRole("combobox", { name: /filter by source/i }),
    ).toBeVisible();
  });

  test("can navigate to new scan form", async ({ page }) => {
    await page.goto(`${DASH_BASE}/orgs/test/organization/discovery/new`);
    await expect(page.getByLabel(/cidr/i)).toBeVisible();
    await expect(page.getByRole("checkbox")).toBeVisible();
    await expect(page.getByRole("button", { name: /start scan/i })).toBeDisabled();
  });

  test("new scan form defaults to the LAN method with CIDR fields visible", async ({ page }) => {
    await page.goto(`${DASH_BASE}/orgs/test/organization/discovery/new`);
    // The scan-method select defaults to LAN.
    const methodSelect = page.getByRole("combobox", { name: /scan method/i });
    await expect(methodSelect).toBeVisible();
    await expect(methodSelect).toHaveText(/IP range|LAN/i);
    // LAN fields (CIDR textarea) are shown by default.
    await expect(page.getByLabel(/cidr/i)).toBeVisible();
  });

  test("Freebox method option is hidden when no granted channel exists", async ({ page }) => {
    await page.goto(`${DASH_BASE}/orgs/test/organization/discovery/new`);
    // Open the scan-method select; the test org has no granted Freebox channel,
    // so only the LAN option is offered.
    await page.getByRole("combobox", { name: /scan method/i }).click();
    await expect(page.getByRole("option", { name: /IP range|LAN/i })).toBeVisible();
    await expect(page.getByRole("option", { name: /^freebox$/i })).toHaveCount(0);
  });

  test("start scan button is disabled without confirmation", async ({ page }) => {
    await page.goto(`${DASH_BASE}/orgs/test/organization/discovery/new`);
    await page.fill("textarea", "127.0.0.1/32");
    // Confirmation not checked — submit should be disabled.
    await expect(page.getByRole("button", { name: /start scan/i })).toBeDisabled();
  });

  test("start scan button enables after confirmation", async ({ page }) => {
    await page.goto(`${DASH_BASE}/orgs/test/organization/discovery/new`);
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
    await waitForScanQuiescence(page);
    await page.goto(`${DASH_BASE}/orgs/test/organization/discovery/new`);
    await page.fill("textarea", "127.0.0.1/32");
    await page.getByRole("checkbox").check();
    await page.getByRole("button", { name: /start scan/i }).click();

    await page.waitForURL(/\/discovery\/[0-9a-f-]{36}$/);
    const jobUid = page.url().split("/").pop() as string;
    await expect(page.getByRole("heading", { name: /scan details/i })).toBeVisible();
    // The uid is rendered on the detail page — blank if the API used the wrong casing.
    await expect(page.getByText(jobUid)).toBeVisible();

    // Back on the index, the table must render without throwing.
    await page.goto(`${DASH_BASE}/orgs/test/organization/discovery`);
    await expect(page.getByRole("heading", { name: /network discovery/i })).toBeVisible();
    const table = page.getByRole("table");
    await expect(table).toBeVisible();

    expect(
      pageErrors,
      `unexpected page errors: ${pageErrors.map((e) => e.message).join(", ")}`,
    ).toHaveLength(0);
  });

  test("scan list no longer shows the bogus IP Address column", async ({ page }) => {
    await page.goto(`${DASH_BASE}/orgs/test/organization/discovery`);
    await expect(page.getByRole("heading", { name: /network discovery/i })).toBeVisible();
    // The first column header used to be "IP Address" while rendering the scan
    // UID — it has been removed. The header row should not contain it.
    const headerRow = page.locator("thead tr");
    if (await headerRow.count()) {
      await expect(headerRow.getByText(/IP Address/i)).toHaveCount(0);
    }
  });

  test("discovery breadcrumb reads Organization > Discovery", async ({ page }) => {
    await page.goto(`${DASH_BASE}/orgs/test/organization/discovery`);
    await expect(
      page.getByRole("heading", { name: /network discovery/i }),
    ).toBeVisible();
    // The breadcrumb (in the header bar) now reads Organization > Discovery —
    // discovery is a tab under Organization (spec 2026-09-16-09), not its own
    // top-level breadcrumb branch.
    const header = page.locator("header");
    await expect(header.getByText(/organization/i)).toBeVisible();
    await expect(header.getByText(/discovery/i)).toBeVisible();
  });

  // Fan-out: a range larger than a /20 (here a /18 → 4 bounded chunks) is now
  // accepted (no DISCOVERY_RANGE_TOO_LARGE), creates a plan scan, and the detail
  // page renders the chunk-progress indicator.
  test("large range fans out into chunks and can be stopped mid-scan", async ({ page }) => {
    await waitForScanQuiescence(page);
    await page.goto(`${DASH_BASE}/orgs/test/organization/discovery/new`);
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
    await page.goto(`${DASH_BASE}/orgs/test/organization/discovery/new`);
    await page.fill("textarea", "127.0.0.1/32");
    await page.getByRole("checkbox").check();
    await expect(page.getByRole("button", { name: /start scan/i })).toBeEnabled();
  });

  test("entering a /8 CIDR shows the large-range warning with host and chunk estimate", async ({ page }) => {
    await page.goto(`${DASH_BASE}/orgs/test/organization/discovery/new`);
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
    await page.goto(`${DASH_BASE}/orgs/test/organization/discovery/${SEEDED_SCAN_UID}`);
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

    await page.goto(`${DASH_BASE}/orgs/test/organization/discovery/${SEEDED_SCAN_UID}`);
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
    await page.goto(`${DASH_BASE}/orgs/test/organization/discovery/${SEEDED_SCAN_UID}`);
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
    await page.goto(`${DASH_BASE}/orgs/test/organization/discovery/${SEEDED_SCAN_UID}`);
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
    await page.goto(`${DASH_BASE}/orgs/test/organization/discovery/new`);
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
    await page.goto(`${DASH_BASE}/orgs/test/organization/discovery/new`);
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
    await page.goto(`${DASH_BASE}/orgs/test/organization/discovery/new`);
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
    await page.goto(`${DASH_BASE}/orgs/test/organization/discovery`);
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
    await page.goto(`${DASH_BASE}/orgs/test/organization/discovery`);
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
    // The backend serializes scans per org, so drain earlier tests' scans
    // first — this test used to flake with 409 DISCOVERY_ALREADY_RUNNING while
    // the stopped /18 fan-out's chunks were still draining.
    await waitForScanQuiescence(page);
    await page.goto(`${DASH_BASE}/orgs/test/organization/discovery/new`);
    await page.fill("textarea", "127.0.0.1/32");
    await page.getByRole("checkbox").check();
    await page.getByRole("button", { name: /start scan/i }).click();
    await page.waitForURL(/\/discovery\/[0-9a-f-]{36}$/);

    // Back on the index, click the first body row — the whole row is clickable.
    await page.goto(`${DASH_BASE}/orgs/test/organization/discovery`);
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
    await page.goto(`${DASH_BASE}/orgs/test/organization/discovery`);
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
    await page.goto(`${DASH_BASE}/orgs/test/me/notifications`);
    await expect(page.getByTestId("my-notifications-page")).toBeVisible();
    await expect(
      page.getByRole("heading", { name: /my pages/i }),
    ).toBeVisible();
    // Breadcrumb mirrors the page title.
    await expect(page.locator("header").getByText(/my pages/i)).toBeVisible();
  });
});

// Spec 2026-09-16-09: discovery moved from a top-level /orgs/$org/discovery
// route to /orgs/$org/organization/discovery, which also gave it the admin
// route guard it never had (the sidebar merely hid the link; a non-admin
// typing the URL used to get the full page). This block covers both halves:
// the legacy URLs still work (redirect, search params preserved), and the
// new URL genuinely refuses a non-admin — not just "the sidebar doesn't show
// it".
test.describe("Discovery legacy redirects and the organization route guard", () => {
  const SEEDED_SCAN_UID = "00000000-0000-0000-0000-000000000007";

  test.beforeEach(async ({ page }) => {
    await page.goto(`${DASH_BASE}/orgs/test/login`);
    await page.getByTestId("login-email").fill("test@test.com");
    await page.getByTestId("login-password").fill("test");
    await page.getByTestId("login-submit").click();
    await page.waitForURL((url) => !url.pathname.includes("login"));
  });

  test("legacy /discovery redirects to /organization/discovery", async ({ page }) => {
    await page.goto(`${DASH_BASE}/orgs/test/discovery`);
    await page.waitForURL(/\/organization\/discovery$/);
    await expect(
      page.getByRole("heading", { name: /network discovery/i }),
    ).toBeVisible();
  });

  test("legacy /discovery/new redirects and preserves ?method=kubernetes", async ({
    page,
  }) => {
    await page.goto(`${DASH_BASE}/orgs/test/discovery/new?method=kubernetes`);
    await page.waitForURL(/\/organization\/discovery\/new\?method=kubernetes$/);
    // The test org has no kubernetes cluster connection, so "Kubernetes" is
    // not a selectable option in the scan-method dropdown (see "Kubernetes
    // method option is hidden..." above) — the trigger renders blank. The
    // kubernetes sub-form is driven directly by local state seeded from the
    // URL, though, so its cluster picker still renders: that proves the
    // ?method= param survived the redirect, not just the base path.
    await expect(
      page.getByLabel(/select kubernetes cluster/i),
    ).toBeVisible();
  });

  test("legacy /discovery/$jobUid redirects to /organization/discovery/$jobUid", async ({
    page,
  }) => {
    await page.goto(`${DASH_BASE}/orgs/test/discovery/${SEEDED_SCAN_UID}`);
    await page.waitForURL(
      new RegExp(`/organization/discovery/${SEEDED_SCAN_UID}$`),
    );
    await expect(
      page.getByRole("heading", { name: /scan details/i }),
    ).toBeVisible();
  });
});

test.describe("A non-admin member cannot reach organization/discovery", () => {
  // seedOwnedOrg creates a user, logs them in, and creates an org through the
  // real POST /api/v1/orgs — same technique as org-members-admin-gate.spec.ts.
  async function seedOwnedOrg(page: Page) {
    const stamp = uniqueStamp();
    const email = `disco-owner-${stamp}@unknown.example`;
    const password = "Strong-Pass-123!";

    const createUserResp = await page.request.post(
      `${API_BASE}/api/v1/test/users`,
      { data: { email, password, name: "Disco Owner" } },
    );
    if (createUserResp.status() !== 201) {
      test.skip(
        true,
        `test user-seed endpoint unavailable (server not in SP_RUNMODE=test?): ${createUserResp.status()}`,
      );
    }

    const loginResp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { email, password },
    });
    expect(loginResp.status()).toBe(200);
    const session = (await loginResp.json()) as { accessToken: string };

    const orgSlug = `disco-${stamp}`;
    const createOrgResp = await page.request.post(`${API_BASE}/api/v1/orgs`, {
      headers: { Authorization: `Bearer ${session.accessToken}` },
      data: { name: `Disco Co ${stamp}`, slug: orgSlug },
    });
    expect(createOrgResp.status()).toBe(201);
    const org = (await createOrgResp.json()) as {
      slug: string;
      accessToken: string;
    };

    return { orgSlug, ownerToken: org.accessToken };
  }

  // seedNonAdmin adds a plain "user" role member (NOT admin/owner, and NOT a
  // viewer either — the point of this spec is that discovery is closed to
  // every non-admin, not merely locked to writes) and returns their session.
  async function seedNonAdmin(page: Page, orgSlug: string, ownerToken: string) {
    const stamp = uniqueStamp();
    const email = `disco-user-${stamp}@unknown.example`;
    const password = "Strong-Pass-123!";

    const createResp = await page.request.post(`${API_BASE}/api/v1/test/users`, {
      data: { email, password, name: "Disco User" },
    });
    if (createResp.status() !== 201) {
      test.skip(true, "test user-seed endpoint unavailable");
    }

    const addResp = await page.request.post(
      `${API_BASE}/api/v1/orgs/${orgSlug}/members`,
      {
        headers: { Authorization: `Bearer ${ownerToken}` },
        data: { email, role: "user" },
      },
    );
    expect(addResp.status()).toBe(201);

    const login = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { org: orgSlug, email, password },
    });
    expect(login.status()).toBe(200);
    const session = (await login.json()) as {
      accessToken: string;
      refreshToken?: string;
      expiresIn?: number;
    };

    return session;
  }

  test("a non-admin member hitting /organization/discovery directly is redirected to the org home", async ({
    page,
  }) => {
    const { orgSlug, ownerToken } = await seedOwnedOrg(page);
    const session = await seedNonAdmin(page, orgSlug, ownerToken);

    // Swap the browser session to the non-admin — same technique as
    // org-members-admin-gate.spec.ts's cross-role check.
    await page.addInitScript(
      ({ accessToken, refreshToken, expiresIn, slug }) => {
        localStorage.setItem("solidping_session_token", accessToken as string);
        if (refreshToken) {
          localStorage.setItem(
            "solidping_refresh_token",
            refreshToken as string,
          );
        }
        if (expiresIn) {
          localStorage.setItem(
            "solidping_expires_at",
            String(Date.now() + Number(expiresIn) * 1000),
          );
          localStorage.setItem("solidping_expires_in", String(expiresIn));
        }
        localStorage.setItem("solidping_org", slug as string);
      },
      {
        accessToken: session.accessToken,
        refreshToken: session.refreshToken ?? "",
        expiresIn: session.expiresIn ?? 0,
        slug: orgSlug,
      },
    );

    // This is the guard that did not exist before spec 2026-09-16-09: the old
    // top-level /orgs/:org/discovery route rendered unconditionally for any
    // member (only the write actions 403'd server-side). Reaching the new URL
    // directly — bypassing any nav entirely — must bounce out of /organization.
    await page.goto(`orgs/${orgSlug}/organization/discovery`);
    await page.waitForLoadState("networkidle");

    await expect(page).toHaveURL(
      new RegExp(`/orgs/${orgSlug}(?!/organization)(/)?$`),
    );
    await expect(
      page.getByRole("heading", { name: /network discovery/i }),
    ).toHaveCount(0);

    // Positive control: the redirect is about the role, not a broken page.
    await expect(page.getByRole("heading", { name: "Dashboard" })).toBeVisible();
  });
});
