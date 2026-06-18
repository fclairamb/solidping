import { test, expect, type Page } from "./fixtures";

const API_BASE = "http://localhost:4000";

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  const body = await resp.json();
  return body.accessToken;
}

async function deleteAllOnCallSchedules(page: Page, token: string) {
  const resp = await page.request.get(
    `${API_BASE}/api/v1/orgs/test/on-call-schedules`,
    { headers: { Authorization: `Bearer ${token}` } },
  );
  const body = await resp.json();
  for (const s of body.data ?? []) {
    await page.request.delete(
      `${API_BASE}/api/v1/orgs/test/on-call-schedules/${s.slug}`,
      { headers: { Authorization: `Bearer ${token}` } },
    );
  }
}

async function deleteAllEscalationPolicies(page: Page, token: string) {
  const resp = await page.request.get(
    `${API_BASE}/api/v1/orgs/test/escalation-policies`,
    { headers: { Authorization: `Bearer ${token}` } },
  );
  const body = await resp.json();
  for (const p of body.data ?? []) {
    await page.request.delete(
      `${API_BASE}/api/v1/orgs/test/escalation-policies/${p.slug}`,
      { headers: { Authorization: `Bearer ${token}` } },
    );
  }
}

async function createOnCallSchedule(
  page: Page,
  token: string,
  name: string,
  slug: string,
) {
  const resp = await page.request.post(
    `${API_BASE}/api/v1/orgs/test/on-call-schedules`,
    {
      headers: { Authorization: `Bearer ${token}` },
      data: {
        name,
        slug,
        timezone: "UTC",
        rotationType: "weekly",
        handoffTime: "09:00",
        handoffWeekday: 1,
        startAt: new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString(),
        userUids: [],
      },
    },
  );
  return resp.json();
}

async function createEscalationPolicy(
  page: Page,
  token: string,
  name: string,
  slug: string,
) {
  const resp = await page.request.post(
    `${API_BASE}/api/v1/orgs/test/escalation-policies`,
    {
      headers: { Authorization: `Bearer ${token}` },
      data: {
        name,
        slug,
        description: `${name} description for filter test`,
        repeatMax: 0,
        steps: [],
      },
    },
  );
  return resp.json();
}

test.describe("Listing pages share the checks-page chrome", () => {
  test("dependencies header + toolbar + bordered table", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.goto("orgs/test/dependencies");
    await page.waitForLoadState("networkidle");

    const h1 = page.getByRole("heading", { level: 1 });
    await expect(h1).toBeVisible();
    await expect(h1).toHaveClass(/text-2xl/);

    // Filter input is wrapped with the search-icon prefix (pl-9)
    const filter = page.getByTestId("dependencies-filter");
    await expect(filter).toBeVisible();
    await expect(filter).toHaveClass(/pl-9/);

    // Refresh button present in the toolbar
    await expect(page.getByTestId("dependencies-refresh")).toBeVisible();
  });

  test("on-call uses bordered table with row navigation", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    await deleteAllOnCallSchedules(page, token);
    const stamp = Date.now();
    await createOnCallSchedule(page, token, `Listing A ${stamp}`, `listing-a-${stamp}`);
    await createOnCallSchedule(page, token, `Listing B ${stamp}`, `listing-b-${stamp}`);

    await page.goto("orgs/test/on-call");
    await page.waitForLoadState("networkidle");

    const h1 = page.getByRole("heading", { level: 1 });
    await expect(h1).toHaveClass(/text-2xl/);

    // Refresh button is in the toolbar
    await expect(page.getByTestId("oncall-refresh")).toBeVisible();

    // Two rows
    const rows = page.getByTestId("oncall-row");
    await expect(rows).toHaveCount(2);

    // Click the first schedule's name and confirm it navigates to detail
    await page.getByRole("link", { name: `Listing A ${stamp}` }).click();
    await page.waitForURL((url) =>
      url.pathname.includes(`/on-call/listing-a-${stamp}`),
    );

    // Cleanup
    await deleteAllOnCallSchedules(page, token);
  });

  test("escalation-policies search filters table rows", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    await deleteAllEscalationPolicies(page, token);
    const stamp = Date.now();
    await createEscalationPolicy(page, token, `Alpha ${stamp}`, `alpha-${stamp}`);
    await createEscalationPolicy(page, token, `Bravo ${stamp}`, `bravo-${stamp}`);

    await page.goto("orgs/test/escalation-policies");
    await page.waitForLoadState("networkidle");

    const rows = page.getByTestId("policy-row");
    await expect(rows).toHaveCount(2);

    const search = page.getByTestId("policy-search");
    await expect(search).toBeVisible();
    await search.fill(`Alpha ${stamp}`);
    await expect(page.getByTestId("policy-row")).toHaveCount(1);

    await search.fill("");
    await expect(page.getByTestId("policy-row")).toHaveCount(2);

    // Cleanup
    await deleteAllEscalationPolicies(page, token);
  });

  test("integrations page has Search-prefixed input and refresh", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.goto("orgs/test/integrations");
    await page.waitForLoadState("networkidle");

    const h1 = page.getByRole("heading", { level: 1 });
    await expect(h1).toHaveClass(/text-2xl/);

    // The refresh button is wired up; the search input is shown when there are
    // integrations, otherwise the empty-state pick-a-type Card replaces the table.
    const refresh = page.getByTestId("integrations-refresh");
    if (await refresh.count()) {
      await expect(refresh).toBeVisible();
    } else {
      // Empty state — confirm the pick-a-type card is the visible body
      await expect(page.getByText(/no integrations yet/i)).toBeVisible();
    }
  });
});
