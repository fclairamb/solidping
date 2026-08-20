import { test, expect, API_BASE, type Page } from "./fixtures";

// Coverage for spec 2026-08-20-01: the SLO list and detail happy path.
//
// The seeded fixture (server/test/testdata/testdata.go, createTestSLOData) is an
// objective named "API availability" over a check carrying a month rollup of
// 9995/10000 — 99.950% against a 99.9% target, so it must read healthy with a
// non-null attainment. 99.950% is deliberately not 100%: a "no data renders as
// perfect" regression would show 100.000%, which this test would catch.

const FIXTURE_NAME = "API availability";
const FIXTURE_ATTAINMENT = "99.950%";

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

test.describe("SLOs", () => {
  test("lists the seeded objective with a real attainment and budget", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/slos");
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("slo-table")).toBeVisible();

    const row = page
      .getByTestId("slo-row")
      .filter({ hasText: FIXTURE_NAME })
      .first();
    await expect(row).toBeVisible();

    // Positive control before the negative one below: the row really is
    // rendering the seeded objective's numbers.
    await expect(row.getByTestId("slo-row-attainment")).toHaveText(
      FIXTURE_ATTAINMENT,
      { timeout: 10000 },
    );
    await expect(row.getByTestId("slo-row-state")).toHaveText("Healthy");
    // No data must never be rendered as a perfect month.
    await expect(row.getByTestId("slo-row-attainment")).not.toHaveText(
      "100.000%",
    );
  });

  test("opens the detail page with budget, history and the edit form", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/slos");
    await page.waitForLoadState("networkidle");

    await page
      .getByTestId("slo-row")
      .filter({ hasText: FIXTURE_NAME })
      .first()
      .getByTestId("slo-row-name")
      .click();

    await page.waitForURL(/\/slos\/[0-9a-f-]{36}/, { timeout: 10000 });

    await expect(page.getByTestId("slo-status-card")).toBeVisible();
    await expect(page.getByTestId("slo-state")).toHaveText("Healthy");
    await expect(page.getByTestId("slo-budget-bar")).toBeVisible();

    // The burn-down chart. Count `circle:has(title)` rather than every
    // `circle`: recharts' hover activeDot is an extra, nondeterministic circle
    // with no <title>, so a plain count flakes.
    const burndown = page.getByTestId("slo-burndown-card");
    await expect(burndown).toBeVisible();
    await expect
      .poll(async () => burndown.locator("circle:has(title)").count(), { timeout: 10000 })
      .toBeGreaterThan(0);

    await expect(page.getByTestId("slo-history-table")).toBeVisible();

    // Editing happens on a dedicated route, never in a modal — the form is
    // part of the detail page and prefilled from the objective.
    await expect(page.getByTestId("slo-form")).toBeVisible();
    await expect(page.getByTestId("slo-name")).toHaveValue(FIXTURE_NAME);
    await expect(page.getByTestId("slo-target")).toHaveValue("99.9");
  });

  test("creates an objective through the dedicated route", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const name = `E2E Objective ${Date.now()}`;

    await page.goto("orgs/test/slos");
    await page.waitForLoadState("networkidle");
    await page.getByTestId("slo-new").click();
    await page.waitForURL(/\/slos\/new/, { timeout: 10000 });

    await page.getByTestId("slo-name").fill(name);
    await page.getByTestId("slo-target").fill("99.5");

    // The scope picker is required: submit stays disabled until a check is
    // chosen, because an unscoped objective has no defensible denominator.
    await expect(page.getByTestId("slo-submit")).toBeDisabled();

    // Live-search picker: open, type, pick — same flow as the badges page.
    await page.getByTestId("slo-check-select").click();
    await page.getByPlaceholder("Search checks").fill("notified-check");
    await page.getByTestId("check-picker-option-notified-check").click();

    await expect(page.getByTestId("slo-submit")).toBeEnabled();
    await page.getByTestId("slo-submit").click();

    await page.waitForURL(/\/slos\/[0-9a-f-]{36}/, { timeout: 10000 });
    const uid = page.url().match(/\/slos\/([0-9a-f-]{36})/)![1];

    const resp = await page.request.get(
      `${API_BASE}/api/v1/orgs/test/slos/${uid}`,
      { headers: { Authorization: `Bearer ${token}` } },
    );
    expect(resp.status()).toBe(200);
    const created = await resp.json();
    expect(created.name).toBe(name);
    expect(created.targetPct).toBe(99.5);
    // Exactly one scope, enforced by the schema as well as the form.
    expect(Boolean(created.checkUid) !== Boolean(created.checkGroupUid)).toBe(
      true,
    );

    // Clean up so a repeat run does not accumulate objectives.
    await page.request.delete(`${API_BASE}/api/v1/orgs/test/slos/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  });

  test("the covered check shows an SLO chip on its detail page", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/checks/00000000-0000-0000-0000-000000000022");
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("check-detail-header")).toBeVisible();
    await expect(page.getByTestId("check-slo-chip")).toBeVisible();

    // Negative control with a positive one attached: a check nobody set an
    // objective over must NOT get the chip, and the header must still render
    // (otherwise "chip absent" would be vacuously true on a blank page).
    await page.goto("orgs/test/checks/00000000-0000-0000-0000-000000000012");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-detail-header")).toBeVisible();
    await expect(page.getByTestId("check-slo-chip")).toHaveCount(0);
  });
});
