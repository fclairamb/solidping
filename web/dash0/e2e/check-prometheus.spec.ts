import { test, expect, API_BASE, type Page } from "./fixtures";

// Coverage for spec 2026-08-15-13: the `prometheus` check type, through the
// real form and API. The promql mode and the zero-threshold trap are covered
// below this layer: src/components/checks/form/types/prometheus.test.ts and
// prometheus-fields.test.tsx (form), and
// server/internal/checkers/checkprometheus/config (config decoding).

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
});
