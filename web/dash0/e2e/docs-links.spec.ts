import { test, expect, API_BASE, mockSloCoverage, type Page } from "./fixtures";

// The DocsLink primitive (web/dash0/src/components/shared/docs-link.tsx) is
// wired into PageHeader (and a few standalone card headers) only where a
// genuinely relevant docs page exists — never a forced link to /docs/intro.
//
// Which page links where is a hard-coded href, pinned without a browser by
// src/docs-links.test.ts (per-page hrefs, the unmapped Jobs page, and every
// target being a real docs page) and src/components/shared/page-header.test.tsx
// (PageHeader renders `docsHref` as the link, and nothing without it). What
// stays here is the one link computed at runtime: the per-type anchor on the
// check detail page.

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

async function createCheck(
  page: Page,
  token: string,
  type: string,
  name: string,
  config: Record<string, unknown>,
): Promise<{ uid: string }> {
  const resp = await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { type, name, config, period: "00:05:00" },
  });
  expect(resp.status()).toBe(201);
  return await resp.json();
}

test.describe("Docs links", () => {
  // The check detail page's docs link is per-type, not the generic
  // check-types page: whichever protocol a check monitors, its reference
  // section is one click away from the check itself.
  test("check detail links to the docs section for that check's type", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await mockSloCoverage(page);
    const token = await getAuthToken(page);

    const stamp = Date.now();
    const tcp = await createCheck(page, token, "tcp", `E2E Docs TCP ${stamp}`, {
      host: "example.com",
      port: 443,
    });
    const dns = await createCheck(page, token, "dns", `E2E Docs DNS ${stamp}`, {
      host: "example.com",
      record_type: "A",
    });

    await page.goto(`orgs/test/checks/${tcp.uid}`);
    await expect(page.getByTestId("check-detail-header")).toBeVisible();

    // The docs link lives in the ⋯ overflow menu, labelled per type.
    await page.getByRole("button", { name: "More actions" }).click();
    const tcpDocsLink = page.getByRole("menuitem", { name: "TCP check docs" });
    await expect(tcpDocsLink).toBeVisible();
    await expect(tcpDocsLink).toHaveAttribute(
      "href",
      "/docs/features/check-types#tcp",
    );
    await expect(tcpDocsLink).toHaveAttribute("target", "_blank");
    await expect(tcpDocsLink).toHaveAttribute("rel", "noopener");

    // A different type on the same page must move the anchor — otherwise the
    // link is the generic page wearing a per-type costume.
    await page.goto(`orgs/test/checks/${dns.uid}`);
    await expect(page.getByTestId("check-detail-header")).toBeVisible();

    await page.getByRole("button", { name: "More actions" }).click();
    await expect(page.getByRole("menuitem", { name: "DNS check docs" })).toHaveAttribute(
      "href",
      "/docs/features/check-types#dns",
    );
  });
});
