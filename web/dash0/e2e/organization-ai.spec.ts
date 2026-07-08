import { test, expect } from "./fixtures";

// Covers the "AI assistants" page (organization/ai): the MCP connector
// setup surface added for one-click Cursor/VS Code installs and copy-paste
// instructions for Claude / Claude Code / generic clients. No backend
// mutation is involved — the page is derived entirely from
// window.location.origin, so these tests only assert rendering + the
// exact URL encoded into each control. Deep-link anchors are asserted by
// decoding their href, never clicked — the cursor:// / vscode: protocol
// handlers aren't available in CI.
test.describe("AI assistants (MCP connector setup)", () => {
  test("renders for an org member with the instance MCP URL", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.goto("orgs/test/organization/ai");
    await page.waitForLoadState("networkidle");

    await expect(
      page.getByRole("heading", { name: /ai assistants/i }),
    ).toBeVisible();

    const origin = await page.evaluate(() => window.location.origin);
    const expectedUrl = `${origin}/api/v1/mcp`;

    // The MCP URL is rendered as visible text (CopyableCode) at least twice:
    // the top-level intro block and inside the Claude card.
    await expect(page.getByText(expectedUrl).first()).toBeVisible();
  });

  test("Cursor deep link encodes the exact instance MCP URL", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.goto("orgs/test/organization/ai");
    await page.waitForLoadState("networkidle");

    const origin = await page.evaluate(() => window.location.origin);
    const expectedUrl = `${origin}/api/v1/mcp`;

    const href = await page.getByTestId("cursor-install-link").getAttribute("href");
    expect(href).toBeTruthy();

    const url = new URL(href!);
    expect(url.protocol).toBe("cursor:");
    expect(url.searchParams.get("name")).toBe("solidping");

    const configBase64 = url.searchParams.get("config");
    expect(configBase64).toBeTruthy();
    const config = JSON.parse(Buffer.from(configBase64!, "base64").toString("utf-8"));
    expect(config.url).toBe(expectedUrl);
  });

  test("VS Code deep link (stable + Insiders) encodes the exact instance MCP URL", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.goto("orgs/test/organization/ai");
    await page.waitForLoadState("networkidle");

    const origin = await page.evaluate(() => window.location.origin);
    const expectedUrl = `${origin}/api/v1/mcp`;

    const href = await page.getByTestId("vscode-install-link").getAttribute("href");
    expect(href).toBeTruthy();
    // "vscode:mcp/install?<json>" — no "//" authority, so the WHATWG URL
    // parser treats everything after the scheme as opaque; split manually.
    const [scheme, rest] = href!.split(":mcp/install?");
    expect(scheme).toBe("vscode");
    const config = JSON.parse(decodeURIComponent(rest));
    expect(config.name).toBe("solidping");
    expect(config.type).toBe("http");
    expect(config.url).toBe(expectedUrl);

    const insidersHref = await page
      .getByTestId("vscode-insiders-install-link")
      .getAttribute("href");
    expect(insidersHref).toBeTruthy();
    const [insidersScheme, insidersRest] = insidersHref!.split(":mcp/install?");
    expect(insidersScheme).toBe("vscode-insiders");
    const insidersConfig = JSON.parse(decodeURIComponent(insidersRest));
    expect(insidersConfig.url).toBe(expectedUrl);
  });

  test("renders without horizontal overflow at mobile width", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    // Narrow the viewport below the Tailwind `sm` (640px) breakpoint so the
    // client-card grid collapses to a single column.
    await page.setViewportSize({ width: 375, height: 812 });

    await page.goto("orgs/test/organization/ai");
    await page.waitForLoadState("networkidle");

    await expect(
      page.getByRole("heading", { name: /ai assistants/i }),
    ).toBeVisible();

    // No fixed-width element should force the page wider than the viewport.
    const hasHorizontalOverflow = await page.evaluate(
      () => document.documentElement.scrollWidth > document.documentElement.clientWidth,
    );
    expect(hasHorizontalOverflow).toBe(false);

    // The deep-link buttons stay visible/tappable at mobile width.
    await expect(page.getByTestId("cursor-install-link")).toBeVisible();
    await expect(page.getByTestId("vscode-install-link")).toBeVisible();
  });
});
