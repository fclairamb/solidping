import { test, expect } from "./fixtures";

// The DocsLink primitive (web/dash0/src/components/shared/docs-link.tsx) is
// wired into PageHeader (and a few standalone card headers) only where a
// genuinely relevant docs page exists — never a forced link to /docs/intro.
// This spec asserts both sides of that contract: representative mapped pages
// render the link with the expected href, and an unmapped page renders none.
test.describe("Docs links", () => {
  test("checks list renders a docs link to check-types", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
    await page.waitForURL(/\/checks/);
    await page.waitForLoadState("networkidle");

    const docsLink = page.getByTestId("docs-link");
    await expect(docsLink).toBeVisible();
    await expect(docsLink).toHaveAttribute("href", "/docs/features/check-types");
    await expect(docsLink).toHaveAttribute("target", "_blank");
    await expect(docsLink).toHaveAttribute("rel", "noopener");
  });

  test("status pages list renders a docs link to status-pages", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    await page.getByTestId("app-sidebar").getByRole("link", { name: "Status Pages" }).click();
    await page.waitForURL(/\/status-pages/);
    await page.waitForLoadState("networkidle");

    const docsLink = page.getByTestId("docs-link");
    await expect(docsLink).toBeVisible();
    await expect(docsLink).toHaveAttribute("href", "/docs/features/status-pages");
  });

  test("SLOs list renders a docs link to slos", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    await page.getByTestId("app-sidebar").getByRole("link", { name: "SLOs" }).click();
    await page.waitForURL(/\/slos/);
    await page.waitForLoadState("networkidle");

    const docsLink = page.getByTestId("docs-link");
    await expect(docsLink).toBeVisible();
    await expect(docsLink).toHaveAttribute("href", "/docs/features/slos");
  });

  test("badges page renders a docs link to status-badges", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    await page.getByTestId("app-sidebar").getByRole("link", { name: "Badges" }).click();
    await page.waitForURL(/\/badges/);
    await page.waitForLoadState("networkidle");

    const docsLink = page.getByTestId("docs-link");
    await expect(docsLink).toBeVisible();
    await expect(docsLink).toHaveAttribute("href", "/docs/features/status-badges");
  });

  test("discovery list renders a docs link to discovery", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    // Discovery is only in the sidebar for org admins; the test user is an
    // admin of the "test" org (server/test/testdata/testdata.go), so it's
    // reachable the same way as the other sidebar-driven cases here.
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Discovery" }).click();
    await page.waitForURL(/\/discovery/);
    await page.waitForLoadState("networkidle");

    const docsLink = page.getByTestId("docs-link");
    await expect(docsLink).toBeVisible();
    await expect(docsLink).toHaveAttribute("href", "/docs/features/discovery");
  });

  test("events list renders a docs link to events", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    await page.getByTestId("app-sidebar").getByRole("link", { name: "Events" }).click();
    await page.waitForURL(/\/events/);
    await page.waitForLoadState("networkidle");

    const docsLink = page.getByTestId("docs-link");
    await expect(docsLink).toBeVisible();
    await expect(docsLink).toHaveAttribute("href", "/docs/features/events");
  });

  test("dependencies graph renders a docs link to the incidents grouping section", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.getByTestId("app-sidebar").getByRole("link", { name: "Dependencies" }).click();
    await page.waitForURL(/\/dependencies/);
    await page.waitForLoadState("networkidle");

    const docsLink = page.getByTestId("docs-link");
    await expect(docsLink).toBeVisible();
    await expect(docsLink).toHaveAttribute(
      "href",
      "/docs/features/incidents#group-incidents-correlated-outages",
    );
  });

  test("a page with no docs mapping renders no docs link", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    // Jobs has no matching docs page (spec: "Pages with no matching docs ...
    // simply don't pass docsHref") — its layout carries no docsHref prop, so
    // no DocsLink should render anywhere on the page.
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Jobs" }).click();
    await page.waitForURL(/\/jobs/);
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("docs-link")).toHaveCount(0);
  });
});
