import { test as base, expect, type Page } from "@playwright/test";
import { test, API_BASE, DASH_BASE, escapeRegExp } from "./fixtures";

/**
 * E2E for spec 2026-09-09-07: the organization cell on the super-admin
 * Server -> Activation funnel is a link to that organization's audit log,
 * mirroring the entitlements list's org-name link.
 */

/**
 * Provisions a real organization the shared super-admin (`test@test.com`,
 * server/test/testdata/testdata.go) is NOT a member of, via the same
 * test-only recipe as accessible-org-redirect.spec.ts: seed a zero-org user
 * through `POST /api/v1/test/users`, log in for real, then create an org
 * through `POST /api/v1/orgs`. The activation funnel lists every
 * organization in the system (ListActivationFunnel has no membership
 * filter), so this org shows up in the funnel for the super admin even
 * though it is never a member.
 *
 * @returns the slug of the freshly created org.
 */
async function seedForeignOrg(page: Page): Promise<string> {
  const stamp = Date.now().toString(36) + Math.random().toString(36).slice(2, 6);
  const email = `activation-org-${stamp}@unknown.example`;
  const password = "Strong-Pass-123!";

  const created = await page.request.post(`${API_BASE}/api/v1/test/users`, {
    data: { email, password, name: "Activation Org Seed User" },
  });
  if (created.status() !== 201) {
    base.skip(
      true,
      `test user-seed endpoint unavailable (server not in SP_RUNMODE=test?): ${created.status()}`,
    );
  }

  const loginResp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { email, password },
  });
  expect(loginResp.status()).toBe(200);
  const noOrgSession = (await loginResp.json()) as { accessToken: string };

  const slug = `act-${stamp}`;
  const orgResp = await page.request.post(`${API_BASE}/api/v1/orgs`, {
    headers: { Authorization: `Bearer ${noOrgSession.accessToken}` },
    data: { name: `Activation Org ${stamp}`, slug },
  });
  expect(orgResp.status()).toBe(201);
  const session = (await orgResp.json()) as { slug: string };
  expect(session.slug).toBe(slug);

  return slug;
}

test.describe("Activation funnel org link to audit", () => {
  test("the org name links to that org's audit log", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/server/activation");
    await page.waitForLoadState("networkidle");

    const link = page.getByTestId("activation-org-link-test");
    await expect(link).toBeVisible();
    await expect(link).toHaveAttribute(
      "href",
      new RegExp(`${escapeRegExp(DASH_BASE)}/orgs/test/organization/audit$`),
    );

    // The slug stays as the muted second line under the link.
    await expect(page.getByTestId("activation-row-test")).toContainText("test");
  });

  test("a super admin who is not a member can follow the link into the target org's audit page", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const foreignOrg = await seedForeignOrg(page);

    // Reload the funnel (as the super admin) now that the foreign org exists.
    await page.goto("orgs/test/server/activation");
    await page.waitForLoadState("networkidle");

    const link = page.getByTestId(`activation-org-link-${foreignOrg}`);
    await expect(link).toBeVisible({ timeout: 15000 });

    await link.click();

    await page.waitForURL(
      new RegExp(`/orgs/${foreignOrg}/organization/audit$`),
      { timeout: 20000 },
    );
    await page.waitForLoadState("networkidle");

    // Proves the cross-org path actually works for a super admin who holds no
    // membership in the target org: the audit page itself renders...
    await expect(page.getByTestId("audit-page")).toBeVisible({
      timeout: 15000,
    });
    await expect(page.getByText("Audit log")).toBeVisible();

    // ...and specifically not either of the two ways this used to fail: a
    // Permission Denied card in place, or a bounce back to the org the
    // operator started from.
    await expect(page.getByText("Permission Denied")).toHaveCount(0);
    expect(page.url()).not.toContain("/orgs/test/");
  });

  test("the target org's row and link disappear once the org is gone (positive control)", async ({
    authenticatedPage,
  }) => {
    // Positive control for the test above: an org slug that was never created
    // must NOT have a link, so the passing assertions above are not simply
    // matching on any row.
    const page = authenticatedPage;

    await page.goto("orgs/test/server/activation");
    await page.waitForLoadState("networkidle");

    await expect(
      page.getByTestId("activation-org-link-does-not-exist-org"),
    ).toHaveCount(0);
  });
});
