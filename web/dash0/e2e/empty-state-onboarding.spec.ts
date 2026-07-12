import { test, expect } from "./fixtures";

// Covers the empty-state onboarding hero (EmptyStateOnboarding, rendered on
// /orgs/$org when the org has zero checks) and specifically the 2026-07-11
// "MCP / AI path" addition: alongside the HTTP/Ping/SSL quick-create chips
// and the full-editor link, the hero now offers a secondary "let AI set
// everything up" sub-card linking to the per-client MCP setup page under
// Account.
//
// The shared `test` org accumulates checks from other suites (and parallel
// workers), so instead of mutating shared state to empty it, the checks list
// endpoint is stubbed to `{"data":[]}` — `isEmptyOrg` in dashboard-page.tsx
// only depends on the checks query, so this deterministically renders the
// hero without touching the backend. The click-through to the MCP page then
// runs against the real backend (the MCP page needs no checks and no PAT —
// exactly the brand-new-org situation the spec calls out).
test.describe("Empty-state onboarding (zero-checks dashboard hero)", () => {
  async function gotoEmptyDashboard(page: import("./fixtures").Page) {
    await page.route("**/api/v1/orgs/test/checks*", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ data: [] }),
      }),
    );
    await page.goto("orgs/test");
    await page.waitForLoadState("networkidle");
  }

  test("offers quick-create, the MCP / AI path, and the full editor", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await gotoEmptyDashboard(page);

    // Primary path: the three quick-start chips and the one-field form.
    await expect(page.getByTestId("quick-start-http")).toBeVisible();
    await expect(page.getByTestId("quick-start-icmp")).toBeVisible();
    await expect(page.getByTestId("quick-start-ssl")).toBeVisible();
    await expect(page.getByTestId("quick-start-input")).toBeVisible();
    await expect(page.getByTestId("quick-start-submit")).toBeVisible();

    // Secondary path: the MCP / AI sub-card links to the Account MCP page.
    const mcpLink = page.getByTestId("quick-start-mcp-link");
    await expect(mcpLink).toBeVisible();
    const href = await mcpLink.getAttribute("href");
    expect(href).toBeTruthy();
    expect(new URL(href!, page.url()).pathname).toBe(
      "/dash0/orgs/test/account/mcp",
    );

    // Tertiary path: the full check editor hint is still there.
    await expect(
      page.locator('a[href="/dash0/orgs/test/checks/new"]'),
    ).toBeVisible();
  });

  test("MCP CTA click-through lands on a working MCP setup page", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await gotoEmptyDashboard(page);

    await page.getByTestId("quick-start-mcp-link").click();
    await page.waitForURL(/\/orgs\/test\/account\/mcp/);

    // The MCP page renders fully for an org with zero checks and no PAT:
    // heading plus the instance MCP URL derived from the origin.
    await expect(
      page.getByRole("heading", { name: /ai assistants/i }),
    ).toBeVisible();
    const origin = await page.evaluate(() => window.location.origin);
    await expect(page.getByText(`${origin}/api/v1/mcp`).first()).toBeVisible();
  });

  test("hero stays usable at mobile width, quick-create first", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.setViewportSize({ width: 375, height: 812 });
    await gotoEmptyDashboard(page);

    await expect(page.getByTestId("quick-start-input")).toBeVisible();
    await expect(page.getByTestId("quick-start-mcp-link")).toBeVisible();

    // No fixed-width element may force the page wider than the viewport.
    const hasHorizontalOverflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth,
    );
    expect(hasHorizontalOverflow).toBe(false);

    // Hierarchy: the quick-create form renders above the MCP sub-card.
    const formBox = await page.getByTestId("quick-start-input").boundingBox();
    const mcpBox = await page.getByTestId("quick-start-mcp-link").boundingBox();
    expect(formBox).toBeTruthy();
    expect(mcpBox).toBeTruthy();
    expect(formBox!.y).toBeLessThan(mcpBox!.y);
  });
});
