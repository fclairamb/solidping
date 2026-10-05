import http from "node:http";
import type { AddressInfo } from "node:net";
import { test, expect, API_BASE, type Page } from "./fixtures";

// Coverage for spec 2026-10-03-05 (health endpoint check): the form creates a
// health check with its defaults implicit, a 503 answering a Spring-style
// document is judged by the body, and the check page lists one row per
// component with the failing one's message.

const SPRING_BODY = {
  status: "DOWN",
  components: {
    db: { status: "UP", details: { database: "PostgreSQL" } },
    mail: { status: "DOWN", details: { error: "connection refused" } },
  },
};

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

test.describe("Application health check", () => {
  let server: http.Server;
  let healthUrl: string;

  test.beforeAll(async () => {
    server = http.createServer((_req, res) => {
      res.writeHead(503, { "Content-Type": "application/json" });
      res.end(JSON.stringify(SPRING_BODY));
    });
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    healthUrl = `http://127.0.0.1:${(server.address() as AddressInfo).port}/actuator/health`;
  });

  test.afterAll(async () => {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  });

  test("creates a health check and shows its components", async ({ authenticatedPage }) => {
    test.setTimeout(90_000);
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const headers = { Authorization: `Bearer ${token}` };

    await page.goto("orgs/test/checks/new?checkType=health");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    // The request line is the http check's; the response assertions are not offered.
    await expect(page.getByTestId("check-url-input")).toBeVisible();
    await expect(page.getByTestId("check-expected-status-codes")).toHaveCount(0);
    await expect(page.getByTestId("check-health-format-select")).toBeVisible();

    // The URL is required.
    await page.getByTestId("check-name-input").fill(`E2E health ${Date.now()}`);
    await page.getByTestId("check-submit-button").click();
    await expect(page.getByText("URL is required").first()).toBeVisible();

    // A bad max age blocks the save.
    await page.getByTestId("check-url-input").fill(healthUrl);
    await page.getByTestId("check-health-max-age-input").fill("soon");
    await page.getByTestId("check-submit-button").click();
    await expect(page.getByText(/Max age must be a duration/).first()).toBeVisible();
    await page.getByTestId("check-health-max-age-input").fill("");

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 15000 });
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    const created = await (
      await page.request.get(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, { headers })
    ).json();
    expect(created.type).toBe("health");
    expect(created.config.url).toBe(healthUrl);
    expect(created.config, "defaults stay implicit").not.toHaveProperty("format");
    expect(created.config).not.toHaveProperty("maxAge");

    // The first run reports down, the failing component named in the error.
    await expect
      .poll(
        async () => {
          const check = await (
            await page.request.get(`${API_BASE}/api/v1/orgs/test/checks/${uid}?with=last_result`, {
              headers,
            })
          ).json();
          return check.lastResult?.output?.error ?? "";
        },
        { timeout: 60_000, intervals: [2_000] },
      )
      .toContain("mail: connection refused");

    const detail = await (
      await page.request.get(`${API_BASE}/api/v1/orgs/test/checks/${uid}?with=last_result`, { headers })
    ).json();
    expect(detail.lastResult.status).toBe("down");
    expect(detail.lastResult.output.format).toBe("spring");
    expect(detail.lastResult.output.failed).toEqual(["mail"]);

    // The check page lists one row per component.
    await page.goto(`orgs/test/checks/${uid}`);
    await expect(page.getByTestId("health-card")).toBeVisible();
    await expect(page.getByTestId("health-component-db")).toHaveAttribute("data-status", "ok");
    const mail = page.getByTestId("health-component-mail");
    await expect(mail).toHaveAttribute("data-status", "failed");
    await expect(mail).toContainText("connection refused");
    await expect(page.getByTestId("health-timeline-mail")).toBeVisible();

    // The edit form suggests the component names found in the last result.
    await page.goto(`orgs/test/checks/${uid}/edit`);
    await expect(page.getByTestId("check-health-suggestions")).toBeVisible();
    await page.getByTestId("check-health-warn-mail").click();
    await expect(page.getByTestId("check-health-warn-only-input")).toHaveValue("mail");
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-[0-9a-f-]{27}$/, { timeout: 15000 });

    const edited = await (
      await page.request.get(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, { headers })
    ).json();
    expect(edited.config.components).toEqual({ mail: { onFailed: "warning" } });

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, { headers });
  });

  test("refuses body assertions through the API", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    const resp = await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
      headers: { Authorization: `Bearer ${token}` },
      data: {
        type: "health",
        name: `E2E health refused ${Date.now()}`,
        config: { url: healthUrl, body_expect: "ok" },
      },
    });
    expect(resp.status()).toBe(422);
    expect(JSON.stringify(await resp.json())).toContain("body_expect");
  });
});
