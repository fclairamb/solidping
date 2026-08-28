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

  // The detail the GET serves. A PUT REPLACES it, the way the real server
  // does: `admin` rows resolve whole-row, so a cap saved as null comes back as
  // null rather than being refilled with the deployment default. That round
  // trip is the thing worth testing — asserting only the request body would
  // pass just as happily on the bug where the toggle flips itself back off.
  let detail: Record<string, unknown> = { ...DETAIL };

  await page.route("**/api/v1/system/entitlements/acmetech", async (route) => {
    if (route.request().method() === "PUT") {
      putBody = route.request().postDataJSON();
      const sent = putBody as { limits: Record<string, unknown> };

      detail = {
        ...DETAIL,
        limits: sent.limits,
        source: "admin",
        stored: {
          source: "admin",
          limits: sent.limits,
          createdAt: new Date().toISOString(),
          updatedAt: new Date().toISOString(),
        },
      };

      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          limits: sent.limits,
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
      body: JSON.stringify(detail),
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

    // And it must SURVIVE the refetch. This is the half that caught the real
    // bug: on SaaS every default is a real number, so a resolver that refilled
    // the stored null would hand the form the default back, the toggle would
    // silently flip itself off, and the org would stay capped while the UI
    // reported the number it had just failed to change.
    const stillUnlimited = page.getByTestId(
      "entitlements-unlimited-maxChecksPerMinute",
    );
    await expect(stillUnlimited).toHaveAttribute("data-state", "checked");
    await expect(
      page.getByTestId("entitlements-input-maxChecksPerMinute"),
    ).toBeDisabled();

    // Positive control: the cap that was given a NUMBER came back as that
    // number, so the assertion above is not passing on a form that reset.
    await expect(page.getByTestId("entitlements-input-maxChecks")).toHaveValue(
      "5000",
    );
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

  test("an org with no audit trail renders the editor, not an error boundary", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // `audits: null` is exactly what the server sent for an organization that
    // had never been edited — which, on a fresh install, is every single one.
    // The editor read `.length` off it and threw, so the whole page came back
    // as "Something went wrong". The server no longer sends null, but a single
    // missing array must not be able to blank the page either.
    await page.route("**/api/v1/system/entitlements/acmetech", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ ...DETAIL, audits: null }),
      });
    });

    await page.goto("orgs/test/server/entitlements/acmetech");
    await page.waitForLoadState("networkidle");

    await expect(page.getByText("Something went wrong")).toHaveCount(0);

    // The editor is really there — not merely the absence of the error.
    await expect(page.getByTestId("entitlements-input-maxChecks")).toHaveValue(
      "100",
    );
    // And the trail reads as empty rather than missing.
    await expect(
      page.getByText("No entitlement change recorded yet."),
    ).toBeVisible();
  });

  test("an API 403 renders Permission Denied in place, never a redirect", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // The client still believes it is a superadmin (the layout gate passes);
    // the SERVER says no. That combination is the one that used to loop.
    await page.route("**/api/v1/system/entitlements*", async (route) => {
      await route.fulfill({
        status: 403,
        contentType: "application/json",
        body: JSON.stringify({
          title: "Super admin access required",
          code: "FORBIDDEN",
        }),
      });
    });

    await page.goto("orgs/test/server/entitlements");
    await page.waitForLoadState("networkidle");

    await expect(page.getByText("Permission Denied")).toBeVisible();
    expect(page.url()).toContain("/server/entitlements");
    expect(page.url()).not.toContain("/login");
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
