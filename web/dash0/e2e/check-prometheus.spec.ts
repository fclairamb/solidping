import { test, expect, API_BASE, type Page } from "./fixtures";

// Coverage for spec 2026-08-15-13: the `prometheus` check type — create/edit
// round-trip for BOTH modes (scrape and promql), the docs anchor, and the
// threshold trap (a `warningValue` of 0 must persist as a real threshold, not
// be swallowed as "unset").

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

async function getCheck(page: Page, token: string, uid: string) {
  const resp = await page.request.get(
    `${API_BASE}/api/v1/orgs/test/checks/${uid}`,
    { headers: { Authorization: `Bearer ${token}` } },
  );
  expect(resp.status()).toBe(200);
  return await resp.json();
}

async function deleteCheck(page: Page, token: string, uid: string) {
  await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}

test.describe("Prometheus check", () => {
  test("docs link points at the prometheus-metric anchor", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/checks/new?checkType=prometheus");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-prometheus-url-input")).toBeVisible();

    await expect(page.getByTestId("docs-link")).toHaveAttribute(
      "href",
      "/docs/features/check-types#prometheus-metric",
    );
  });

  test("scrape mode round-trips through save and reload", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    await page.goto("orgs/test/checks/new?checkType=prometheus");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-prometheus-url-input")).toBeVisible();

    const checkName = `E2E Prometheus Scrape ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page
      .getByTestId("check-prometheus-url-input")
      .fill("https://app.example.test/metrics");
    await page
      .getByTestId("check-prometheus-metric-input")
      .fill("process_open_fds");

    await page.getByTestId("check-prometheus-label-add").click();
    await page.getByTestId("check-prometheus-label-key-0").fill("instance");
    await page.getByTestId("check-prometheus-label-value-0").fill("app-1");

    await page.getByTestId("check-prometheus-warning-input").fill("800");
    await page.getByTestId("check-prometheus-critical-input").fill("1000");

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    const created = await getCheck(page, token, uid);
    expect(created.type).toBe("prometheus");
    expect(created.config.mode).toBe("scrape");
    expect(created.config.metric).toBe("process_open_fds");
    expect(created.config.labels).toEqual({ instance: "app-1" });
    expect(created.config.operator).toBe(">");
    expect(created.config.warningValue).toBe(800);
    expect(created.config.criticalValue).toBe(1000);

    await page.goto(`orgs/test/checks/${uid}/edit`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-prometheus-url-input")).toHaveValue(
      "https://app.example.test/metrics",
    );
    await expect(page.getByTestId("check-prometheus-metric-input")).toHaveValue(
      "process_open_fds",
    );
    await expect(page.getByTestId("check-prometheus-label-key-0")).toHaveValue(
      "instance",
    );
    await expect(
      page.getByTestId("check-prometheus-warning-input"),
    ).toHaveValue("800");
    await expect(
      page.getByTestId("check-prometheus-critical-input"),
    ).toHaveValue("1000");

    await deleteCheck(page, token, uid);
  });

  test("promql mode round-trips and swaps the selector fields", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    await page.goto("orgs/test/checks/new?checkType=prometheus");
    await page.waitForLoadState("networkidle");

    await page.getByTestId("check-prometheus-mode-select").click();
    await page.getByRole("option", { name: /PromQL query against/ }).click();

    // Switching modes must replace the scrape selector with the query field.
    await expect(
      page.getByTestId("check-prometheus-query-input"),
    ).toBeVisible();
    await expect(page.getByTestId("check-prometheus-metric-input")).toHaveCount(
      0,
    );

    const checkName = `E2E Prometheus PromQL ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page
      .getByTestId("check-prometheus-url-input")
      .fill("https://prometheus.example.test");
    await page
      .getByTestId("check-prometheus-query-input")
      .fill('sum(rate(http_requests_total{code=~"5.."}[5m]))');
    await page.getByTestId("check-prometheus-critical-input").fill("10");

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    const created = await getCheck(page, token, uid);
    expect(created.config.mode).toBe("promql");
    expect(created.config.query).toBe(
      'sum(rate(http_requests_total{code=~"5.."}[5m]))',
    );
    expect(created.config.metric).toBeUndefined();
    expect(created.config.criticalValue).toBe(10);

    await page.goto(`orgs/test/checks/${uid}/edit`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-prometheus-query-input")).toHaveValue(
      'sum(rate(http_requests_total{code=~"5.."}[5m]))',
    );
    await expect(
      page.getByTestId("check-prometheus-critical-input"),
    ).toHaveValue("10");

    await deleteCheck(page, token, uid);
  });

  // The pointer-presence trap end to end: 0 is a legal threshold, so a
  // warning-only check with `warningValue: 0` must persist and come back as 0
  // rather than being dropped as "unset".
  test("a zero warning threshold persists as a real threshold", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    await page.goto("orgs/test/checks/new?checkType=prometheus");
    await page.waitForLoadState("networkidle");

    await page
      .getByTestId("check-name-input")
      .fill(`E2E Prom Zero ${Date.now()}`);
    await page
      .getByTestId("check-prometheus-url-input")
      .fill("https://app.example.test/metrics");
    await page.getByTestId("check-prometheus-metric-input").fill("free_slots");

    await page.getByTestId("check-prometheus-operator-select").click();
    await page.getByRole("option", { name: /at most/ }).click();

    await page.getByTestId("check-prometheus-warning-input").fill("0");

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    const created = await getCheck(page, token, uid);
    expect(created.config.warningValue).toBe(0);
    expect(created.config.criticalValue).toBeUndefined();
    expect(created.config.operator).toBe("<=");

    await page.goto(`orgs/test/checks/${uid}/edit`);
    await page.waitForLoadState("networkidle");
    await expect(
      page.getByTestId("check-prometheus-warning-input"),
    ).toHaveValue("0");

    await deleteCheck(page, token, uid);
  });
});
