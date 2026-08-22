import { test, expect, API_BASE } from "./fixtures";

/**
 * Org audit log (spec 2026-08-21-09).
 *
 * The load-bearing legs here are the ones a unit test cannot reach: that an
 * ordinary dashboard action really lands in the trail through the whole stack
 * (service emission → events table → filtered API → rendered row), and that
 * the trail carries an actor rather than an anonymous "system" for something a
 * person did.
 */
test.describe("Organization audit log", () => {
  test("a config change performed in the dashboard shows up attributed in the audit log", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // A real, audited mutation through the real API — an escalation policy,
    // because it is the cheapest resource to create and delete without side
    // effects on other specs sharing this org.
    const name = `audit-e2e-${Date.now()}`;
    const created = await page.request.post(
      `${API_BASE}/api/v1/orgs/test/escalation-policies`,
      { data: { name, steps: [] } },
    );
    expect(created.status()).toBe(201);
    const policy = await created.json();

    await page.goto("orgs/test/organization/audit");
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("audit-page")).toBeVisible();

    // Narrow to the escalation-policy family so the assertion does not depend
    // on how busy the shared org's trail is.
    await page.getByTestId("audit-family-filter").click();
    await page.getByRole("option", { name: /escalation polic/i }).click();
    await page.waitForLoadState("networkidle");

    const row = page.getByTestId("audit-row").filter({ hasText: name });
    await expect(row).toBeVisible({ timeout: 10000 });

    // Attributed to the logged-in operator, not to "System" — this is the
    // whole point of the actor plumbing.
    await expect(row).toContainText(/test@test\.com|Test/i);

    await page.request.delete(
      `${API_BASE}/api/v1/orgs/test/escalation-policies/${policy.uid}`,
    );
  });

  test("the family filter lives in the URL and survives a reload", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/organization/audit");
    await page.waitForLoadState("networkidle");

    await page.getByTestId("audit-family-filter").click();
    await page.getByRole("option", { name: /^Membership$/i }).click();

    await expect(page).toHaveURL(/family=member/);

    await page.reload();
    await page.waitForLoadState("networkidle");

    // The filter is restored from the URL rather than reset to "all".
    await expect(page.getByTestId("audit-family-filter")).toContainText(
      /membership/i,
    );
  });

  test("the audit request asks the API for the family, not for everything", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/organization/audit");
    await page.waitForLoadState("networkidle");

    const requestPromise = page.waitForRequest(
      (req) =>
        req.url().includes("/api/v1/orgs/test/events") &&
        req.url().includes("type=auth"),
    );

    await page.getByTestId("audit-family-filter").click();
    await page.getByRole("option", { name: /^Authentication$/i }).click();

    const request = await requestPromise;
    // Server-side filtering, not a client-side slice of the whole trail: the
    // auth family is admin-gated in the API and a client-side filter would
    // both leak and scale badly.
    expect(request.url()).toContain("type=auth");
  });
});
