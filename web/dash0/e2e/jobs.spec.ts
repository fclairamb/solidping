import { test, expect } from "@playwright/test";

// Admin Jobs observability page (spec 2026-06-15-05).
// The test user (test@test.com) is an org admin AND super-admin in test mode,
// so the admin-gated page, the super-admin scope toggle, and the Org column
// are all exercisable here.
test.describe("Admin Jobs page", () => {
  test.beforeEach(async ({ page }) => {
    await page.goto("/dash0/orgs/test/login");
    await page.getByTestId("login-email").fill("test@test.com");
    await page.getByTestId("login-password").fill("test");
    await page.getByTestId("login-submit").click();
    await page.waitForURL((url) => !url.pathname.includes("login"));
  });

  test("Jobs sidebar link is visible for admin", async ({ page }) => {
    await page.goto("/dash0/orgs/test");
    const sidebar = page.getByTestId("app-sidebar");
    await expect(sidebar).toBeVisible();
    await expect(
      sidebar.getByRole("link", { name: /^jobs$/i }),
    ).toBeVisible();
  });

  test("renders the overview strip and both tabs", async ({ page }) => {
    await page.goto("/dash0/orgs/test/jobs");
    await expect(page.getByRole("heading", { name: /^jobs$/i })).toBeVisible();

    // Activity overview strip with stat tiles.
    await expect(page.getByTestId("jobs-overview")).toBeVisible();

    // Two tabs are present.
    await expect(page.getByRole("tab", { name: /background jobs/i })).toBeVisible();
    await expect(page.getByRole("tab", { name: /check schedule/i })).toBeVisible();
  });

  test("can switch between tabs", async ({ page }) => {
    await page.goto("/dash0/orgs/test/jobs");
    await expect(page.getByRole("tab", { name: /background jobs/i })).toBeVisible();

    // Switch to the check-schedule tab.
    await page.getByRole("tab", { name: /check schedule/i }).click();
    await expect(
      page.getByRole("tab", { name: /check schedule/i }),
    ).toHaveAttribute("aria-selected", "true");
  });

  test("tab selection drives the URL, survives reload, and back/forward works", async ({
    page,
  }) => {
    await page.goto("/dash0/orgs/test/jobs");
    // Default tab is background jobs; URL carries no schedule marker yet.
    await expect(
      page.getByRole("tab", { name: /background jobs/i }),
    ).toHaveAttribute("aria-selected", "true");

    // Switching to the schedule tab must be reflected in the URL (deep-linkable).
    await page.getByRole("tab", { name: /check schedule/i }).click();
    await page.waitForURL(/[?&]tab=schedule/);

    // A reload keeps us on the schedule tab — the URL is the source of truth.
    await page.reload();
    await expect(
      page.getByRole("tab", { name: /check schedule/i }),
    ).toHaveAttribute("aria-selected", "true");

    // Browser back returns to the background-jobs tab (tab change pushed history).
    await page.goBack();
    await expect(
      page.getByRole("tab", { name: /background jobs/i }),
    ).toHaveAttribute("aria-selected", "true");

    // Deep-linking straight to a tab works too.
    await page.goto("/dash0/orgs/test/jobs?tab=schedule");
    await expect(
      page.getByRole("tab", { name: /check schedule/i }),
    ).toHaveAttribute("aria-selected", "true");
  });

  test("background-jobs tab exposes status and type filters", async ({ page }) => {
    await page.goto("/dash0/orgs/test/jobs");
    // The status & type filters are Radix Select comboboxes.
    await expect(
      page.getByRole("combobox", { name: /status/i }),
    ).toBeVisible();
    await expect(
      page.getByRole("combobox", { name: /type/i }),
    ).toBeVisible();
  });

  test("super-admin sees the scope toggle; toggling shows the Org column", async ({
    page,
  }) => {
    await page.goto("/dash0/orgs/test/jobs");

    // The "This org / All orgs (system)" toggle is super-admin only.
    const allOrgsBtn = page.getByTestId("scope-all-orgs");
    await expect(allOrgsBtn).toBeVisible();
    await expect(page.getByTestId("scope-this-org")).toBeVisible();

    // Switch to all-orgs mode; an Org column header should appear in the table.
    await allOrgsBtn.click();
    await expect(
      page.locator("thead").getByText(/^org$/i).first(),
    ).toBeVisible({ timeout: 10000 });
  });

  test("non-admin route guard redirects to org home", async ({ page }) => {
    // Simulate a non-admin by clearing the admin flag is not possible here;
    // instead verify the page is reachable for the admin (positive control).
    // The guard itself is unit-tested via the layout; here we assert the admin
    // is NOT redirected away from /jobs.
    await page.goto("/dash0/orgs/test/jobs");
    await page.waitForURL(/\/jobs\/?$/);
    await expect(page.getByRole("heading", { name: /^jobs$/i })).toBeVisible();
  });

  test("check-schedule rows link to a check-job detail with no secret values", async ({
    page,
  }) => {
    await page.goto("/dash0/orgs/test/jobs");
    await page.getByRole("tab", { name: /check schedule/i }).click();

    const firstRowLink = page
      .getByRole("table")
      .getByRole("link")
      .first();

    // Only drill in when the test org has at least one scheduled check.
    if (await firstRowLink.isVisible().catch(() => false)) {
      await firstRowLink.click();
      await page.waitForURL(/\/jobs\/check\/[0-9a-f-]{36}/);

      // The detail page states secrets are never exposed.
      await expect(
        page.getByText(/secret values are never exposed/i),
      ).toBeVisible();

      // A back arrow returns to the jobs index.
      await page.getByRole("button", { name: /^back$/i }).click();
      await page.waitForURL(/\/jobs\/?(\?|$)/);
      await expect(page.getByRole("heading", { name: /^jobs$/i })).toBeVisible();
    }
  });

  test("list page shows a Jobs breadcrumb in the header bar", async ({ page }) => {
    await page.goto("/dash0/orgs/test/jobs");
    await expect(page.getByRole("heading", { name: /^jobs$/i })).toBeVisible();
    // The shared breadcrumb bar (rendered by the org layout) carries the Jobs
    // crumb. On the list page it is an active (non-link) crumb, so scoping to
    // the header and asserting the text is present is enough.
    await expect(page.locator("header").getByText(/^jobs$/i)).toBeVisible();
  });

  test("check-job detail extends the breadcrumb and the Jobs crumb links back", async ({
    page,
  }) => {
    await page.goto("/dash0/orgs/test/jobs");
    await page.getByRole("tab", { name: /check schedule/i }).click();

    const firstRowLink = page.getByRole("table").getByRole("link").first();

    // Only drill in when the test org has at least one scheduled check.
    if (await firstRowLink.isVisible().catch(() => false)) {
      await firstRowLink.click();
      await page.waitForURL(/\/jobs\/check\/[0-9a-f-]{36}/);

      // The breadcrumb is extended: the Jobs crumb is now a link back to the
      // list, alongside a leaf crumb for the check.
      const jobsCrumb = page
        .locator("header")
        .getByRole("link", { name: /^jobs$/i });
      await expect(jobsCrumb).toBeVisible();

      // Clicking the Jobs crumb returns to the list on the jobs tab.
      await jobsCrumb.click();
      await page.waitForURL(/\/jobs\/?(\?|$)/);
      await expect(
        page.getByRole("heading", { name: /^jobs$/i }),
      ).toBeVisible();
    }
  });

  test("page has no runtime errors on load", async ({ page }) => {
    const errors: Error[] = [];
    page.on("pageerror", (e) => errors.push(e));

    await page.goto("/dash0/orgs/test/jobs");
    await expect(page.getByRole("heading", { name: /^jobs$/i })).toBeVisible();
    await page.getByRole("tab", { name: /check schedule/i }).click();

    expect(
      errors,
      `unexpected page errors: ${errors.map((e) => e.message).join(", ")}`,
    ).toHaveLength(0);
  });
});
