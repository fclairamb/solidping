import { test, expect, type Page } from "./fixtures";

/**
 * E2E for the superadmin entitlements editor (spec 2026-08-26-06).
 *
 * The API is stubbed with `page.route()`: what has to hold here is the UI's
 * behaviour — that a null cap renders as the word "Unlimited" rather than a
 * blank box, that an org over its execution cap is flagged in the list, that
 * saving restates the org slug and the diff before it writes, and that the
 * request actually sent carries `null` for the caps switched to unlimited. The
 * precedence rule itself is covered by the Go tests.
 */

const OVER_ORG = {
  organizationUid: "org-over",
  slug: "acmetech",
  name: "Acme Tech",
  limits: { maxChecks: 100, maxUsers: 5, maxChecksPerMinute: 10 },
  source: "default",
  stale: false,
  checksPerMinute: { demand: 42, limit: 10, skippedToday: 613 },
  overCheckRate: true,
};

const CALM_ORG = {
  organizationUid: "org-calm",
  slug: "acme",
  name: "Acme",
  limits: { maxChecks: null, maxUsers: 30, maxChecksPerMinute: null },
  source: "admin",
  stale: false,
  checksPerMinute: { demand: 2, limit: null, skippedToday: 0 },
  overCheckRate: false,
  adminOverrideSince: new Date(Date.now() - 3600_000).toISOString(),
};

const DETAIL = {
  ...OVER_ORG,
  defaults: { maxChecks: 100, maxUsers: 5, maxChecksPerMinute: 10 },
  audits: [
    {
      uid: "audit-1",
      organizationUid: "org-over",
      source: "billing-service:suppressed",
      actor: "service:entitlements",
      afterSnapshot: {},
      reason: "An admin override holds this organization's entitlements",
      createdAt: new Date(Date.now() - 600_000).toISOString(),
    },
  ],
};

/** Captures the body of the PUT so the test can assert what was written. */
async function stubEntitlementsApi(page: Page): Promise<{ put: () => unknown }> {
  let putBody: unknown = null;

  await page.route("**/api/v1/system/entitlements?*", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: [CALM_ORG, OVER_ORG], total: 2 }),
    });
  });

  await page.route("**/api/v1/system/entitlements", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: [CALM_ORG, OVER_ORG], total: 2 }),
    });
  });

  await page.route("**/api/v1/system/entitlements/acmetech", async (route) => {
    if (route.request().method() === "PUT") {
      putBody = route.request().postDataJSON();
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          limits: DETAIL.limits,
          source: "admin",
          stale: false,
          applied: true,
        }),
      });

      return;
    }

    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(DETAIL),
    });
  });

  return { put: () => putBody };
}

test.describe("Superadmin entitlements editor", () => {
  test("lists organizations and flags the ones over their execution cap", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await stubEntitlementsApi(page);

    await page.goto("orgs/test/server/entitlements");
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("entitlements-row-acmetech")).toBeVisible();
    await expect(page.getByTestId("entitlements-over-acmetech")).toBeVisible();

    // Positive control: the org inside its cap is NOT flagged, so the badge
    // above is not simply rendering on every row.
    await expect(page.getByTestId("entitlements-over-acme")).toHaveCount(0);

    // A null cap must read as the word, never as an empty cell.
    await expect(page.getByTestId("entitlements-row-acme")).toContainText(
      "Unlimited",
    );
  });

  test("saving restates the slug and the diff, and writes null for unlimited", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const captured = await stubEntitlementsApi(page);

    await page.goto("orgs/test/server/entitlements/acmetech");
    await page.waitForLoadState("networkidle");

    // Pre-filled from the resolved values, not blank.
    await expect(page.getByTestId("entitlements-input-maxChecks")).toHaveValue(
      "100",
    );

    // Raise one cap and lift another to unlimited.
    await page.getByTestId("entitlements-input-maxChecks").fill("5000");
    await page.getByTestId("entitlements-unlimited-maxChecksPerMinute").click();

    await page.getByTestId("entitlements-reason").fill("incident bump");
    await page.getByTestId("entitlements-save").click();

    // The confirmation restates the org and the actual changes — that is what
    // makes the extra click worth asking for.
    const dialog = page.getByRole("alertdialog");
    await expect(dialog).toContainText("acmetech");
    await expect(page.getByTestId("entitlements-diff")).toContainText("5000");

    await page.getByTestId("entitlements-save-confirm").click();

    await expect
      .poll(() => captured.put(), { timeout: 10_000 })
      .toMatchObject({
        limits: {
          maxChecks: 5000,
          // The switched-to-unlimited cap must travel as null, which is what
          // "unlimited" means on this API — not an omitted key, not a 0.
          maxChecksPerMinute: null,
        },
      });
  });

  test("shows a suppressed billing push in the audit trail", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await stubEntitlementsApi(page);

    await page.goto("orgs/test/server/entitlements/acmetech");
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("audit-suppressed")).toBeVisible();
  });

  test("a non-superadmin never reaches the editor", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await stubEntitlementsApi(page);

    await page.route("**/api/v1/auth/me", async (route) => {
      const response = await route.fetch();
      const body = await response.json();

      // isSuperAdmin is derived from role === "superadmin" (AuthContext),
      // so downgrading the role is what actually drops the capability.
      if (body?.user) {
        body.user.role = "admin";
      }

      await route.fulfill({
        status: response.status(),
        contentType: "application/json",
        body: JSON.stringify(body),
      });
    });

    await page.goto("orgs/test/server/entitlements");
    await page.waitForLoadState("networkidle");

    // The Server layout's own gate keeps the whole area away from a
    // non-superadmin; what matters here is that the editor never renders.
    await expect(page.getByTestId("entitlements-search")).toHaveCount(0);
    expect(page.url()).not.toContain("/login");
  });
});
