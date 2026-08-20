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
const FIXTURE_UID = "00000000-0000-0000-0000-000000000023";
// The check the fixture objective is scoped to (server/test/testdata/testdata.go,
// createTestSLOData) — its name, never its uid, must appear in the scope picker.
const FIXTURE_CHECK_NAME = "SLO API";

// A picker trigger must never leak the raw uid it resolves — spec
// 2026-08-20-05. Every check/group uid in this suite's fixtures is a v4-style
// uuid, so this is a reliable negative-control pattern.
const UUID_PATTERN = /[0-9a-f]{8}-[0-9a-f]{4}-/i;

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

async function createGroupViaApi(
  page: Page,
  token: string,
  name: string,
): Promise<{ uid: string; name: string; slug: string }> {
  const resp = await page.request.post(
    `${API_BASE}/api/v1/orgs/test/check-groups`,
    {
      headers: { Authorization: `Bearer ${token}` },
      data: { name },
    },
  );
  expect(resp.status()).toBe(201);
  return resp.json();
}

async function deleteSloViaApi(page: Page, token: string, uid: string) {
  await page.request.delete(`${API_BASE}/api/v1/orgs/test/slos/${uid}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}

async function deleteGroupViaApi(page: Page, token: string, uid: string) {
  await page.request.delete(`${API_BASE}/api/v1/orgs/test/check-groups/${uid}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
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

  test("opens the detail page with budget and history, no inline form", async ({
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

    await page.waitForURL(/\/slos\/[0-9a-f-]{36}$/, { timeout: 10000 });

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

    // Editing is a dedicated route, never an inline form on the detail page
    // (repo convention: "editing always changes the route").
    await expect(page.getByTestId("slo-form")).toHaveCount(0);
    await expect(page.getByTestId("slo-detail-edit-button")).toBeVisible();
  });

  test("edit route: pre-filled from the objective, scope shows the check name not a uid, cancel returns to detail", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto(`orgs/test/slos/${FIXTURE_UID}`);
    await page.waitForLoadState("networkidle");

    await page.getByTestId("slo-detail-edit-button").click();
    await page.waitForURL(new RegExp(`/slos/${FIXTURE_UID}/edit$`), {
      timeout: 10000,
    });

    await expect(page.getByTestId("slo-form")).toBeVisible();
    await expect(page.getByTestId("slo-name")).toHaveValue(FIXTURE_NAME);
    await expect(page.getByTestId("slo-target")).toHaveValue("99.9");

    // The scope picker's initial value comes from the loaded objective, not a
    // user select() — this exercises the resolve-by-uid path, not the
    // set-on-select path.
    const scopeTrigger = page.getByTestId("slo-check-select");
    await expect(scopeTrigger).toHaveText(FIXTURE_CHECK_NAME, { timeout: 10000 });
    await expect(scopeTrigger).not.toHaveText(UUID_PATTERN);

    await page.getByRole("button", { name: /cancel/i }).click();
    await page.waitForURL(new RegExp(`/slos/${FIXTURE_UID}$`), { timeout: 10000 });
    await expect(page.getByTestId("slo-form")).toHaveCount(0);
  });

  test("editing an objective saves and returns to the detail page with the change visible", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const name = `E2E Edit ${Date.now()}`;

    const created = await page.request.post(
      `${API_BASE}/api/v1/orgs/test/slos`,
      {
        headers: { Authorization: `Bearer ${token}` },
        data: { name, checkUid: "00000000-0000-0000-0000-000000000012", targetPct: 99 },
      },
    );
    expect(created.status()).toBe(201);
    const slo = await created.json();

    try {
      await page.goto(`orgs/test/slos/${slo.uid}/edit`);
      await page.waitForLoadState("networkidle");

      const newName = `${name} (renamed)`;
      await page.getByTestId("slo-name").fill(newName);
      await page.getByTestId("slo-submit").click();

      await page.waitForURL(new RegExp(`/slos/${slo.uid}$`), { timeout: 10000 });
      await expect(page.getByTestId("slo-form")).toHaveCount(0);
      // PageHeader title and the status card both echo the SLO's name.
      await expect(page.getByRole("heading", { name: newName })).toBeVisible();
    } finally {
      await deleteSloViaApi(page, token, slo.uid);
    }
  });

  test("creates an objective through the dedicated route, with slug autofill and a self-resolving picker label", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const stamp = Date.now();
    const name = `E2E Objective ${stamp}`;

    await page.goto("orgs/test/slos");
    await page.waitForLoadState("networkidle");
    await page.getByTestId("slo-new").click();
    await page.waitForURL(/\/slos\/new/, { timeout: 10000 });

    await page.getByTestId("slo-name").fill(name);
    // Slug auto-generates from the name until the operator edits it by hand.
    await expect(page.getByTestId("slo-slug")).toHaveValue(`e2e-objective-${stamp}`);

    await page.getByTestId("slo-slug").fill("my-own-slug");
    // A further name edit must NOT clobber the manually-set slug.
    await page.getByTestId("slo-name").fill(`${name} renamed`);
    await expect(page.getByTestId("slo-slug")).toHaveValue("my-own-slug");

    await page.getByTestId("slo-target").fill("99.5");

    // The scope picker is required: submit stays disabled until a check is
    // chosen, because an unscoped objective has no defensible denominator.
    await expect(page.getByTestId("slo-submit")).toBeDisabled();

    // Live-search picker: open, type, pick — same flow as the badges page.
    await page.getByTestId("slo-check-select").click();
    await page.getByPlaceholder("Search checks").fill("notified-check");
    await page.getByTestId("check-picker-option-notified-check").click();

    // The trigger must show the check's name, never its raw uid (spec
    // 2026-08-20-05, fix 1).
    const scopeTrigger = page.getByTestId("slo-check-select");
    await expect(scopeTrigger).toHaveText("Notified Check");
    await expect(scopeTrigger).not.toHaveText(UUID_PATTERN);

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
    expect(created.name).toBe(`${name} renamed`);
    expect(created.slug).toBe("my-own-slug");
    expect(created.targetPct).toBe(99.5);
    // Exactly one scope, enforced by the schema as well as the form.
    expect(Boolean(created.checkUid) !== Boolean(created.checkGroupUid)).toBe(
      true,
    );

    // Clean up so a repeat run does not accumulate objectives.
    await deleteSloViaApi(page, token, uid);
  });

  test("check-group scope also resolves to the group's name, not a uid", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const stamp = Date.now();
    const group = await createGroupViaApi(page, token, `E2E SLO Group ${stamp}`);

    try {
      await page.goto("orgs/test/slos/new");
      await page.waitForLoadState("networkidle");

      await page.getByTestId("slo-name").fill(`E2E Group Objective ${stamp}`);
      await page.getByTestId("slo-scope-group").click();

      await page.getByTestId("slo-group-select").click();
      await page.getByPlaceholder(/search groups/i).fill(group.name);
      await page.getByTestId(`check-group-picker-option-${group.slug}`).click();

      const scopeTrigger = page.getByTestId("slo-group-select");
      await expect(scopeTrigger).toHaveText(group.name);
      await expect(scopeTrigger).not.toHaveText(UUID_PATTERN);

      await page.getByTestId("slo-submit").click();
      await page.waitForURL(/\/slos\/[0-9a-f-]{36}/, { timeout: 10000 });
      const uid = page.url().match(/\/slos\/([0-9a-f-]{36})/)![1];

      const resp = await page.request.get(
        `${API_BASE}/api/v1/orgs/test/slos/${uid}`,
        { headers: { Authorization: `Bearer ${token}` } },
      );
      const created = await resp.json();
      expect(created.checkGroupUid).toBe(group.uid);

      await deleteSloViaApi(page, token, uid);
    } finally {
      await deleteGroupViaApi(page, token, group.uid);
    }
  });

  test("breadcrumbs show the SLOs section on the list, new, and detail routes", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/slos");
    await page.waitForLoadState("networkidle");
    let header = page.locator("header").first();
    // Section root on its own index page renders as plain (non-link) text.
    await expect(header.getByText("SLOs", { exact: true })).toBeVisible();
    await expect(
      header.getByRole("link", { name: "SLOs" }),
    ).toHaveCount(0);

    await page.goto("orgs/test/slos/new");
    await page.waitForLoadState("networkidle");
    header = page.locator("header").first();
    await expect(header.getByRole("link", { name: "SLOs" })).toBeVisible();
    await expect(header.getByText("New", { exact: true })).toBeVisible();

    await page.goto(`orgs/test/slos/${FIXTURE_UID}`);
    await page.waitForLoadState("networkidle");
    header = page.locator("header").first();
    await expect(header.getByRole("link", { name: "SLOs" })).toBeVisible();
    await expect(header.getByText(FIXTURE_NAME, { exact: true })).toBeVisible();
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
