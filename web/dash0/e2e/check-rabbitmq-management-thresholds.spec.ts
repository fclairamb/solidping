import { test, expect, API_BASE, type Page } from "./fixtures";

// Coverage for spec 2026-09-15-08: RabbitMQ management-mode memory/disk
// thresholds round-trip through the form (mode select, management port, and
// all four threshold inputs).

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

test.describe("RabbitMQ check — management mode thresholds", () => {
  test("switching to management mode hides Queue and shows the threshold inputs", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/checks/new?checkType=rabbitmq");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-host-input")).toBeVisible();
    await expect(page.getByTestId("check-queue-input")).toBeVisible();

    await page.getByTestId("check-rabbitmq-mode-select").click();
    await page.getByRole("option", { name: "Management API" }).click();

    await expect(page.getByTestId("check-queue-input")).not.toBeVisible();
    await expect(
      page.getByTestId("check-rabbitmq-management-port-input"),
    ).toBeVisible();
    await expect(
      page.getByTestId("check-rabbitmq-memory-warning-input"),
    ).toBeVisible();
    await expect(
      page.getByTestId("check-rabbitmq-memory-critical-input"),
    ).toBeVisible();
    await expect(
      page.getByTestId("check-rabbitmq-disk-warning-input"),
    ).toBeVisible();
    await expect(
      page.getByTestId("check-rabbitmq-disk-critical-input"),
    ).toBeVisible();
  });

  test("management mode with all four thresholds round-trips through save and reload", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    await page.goto("orgs/test/checks/new?checkType=rabbitmq");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-host-input")).toBeVisible();

    const checkName = `E2E RabbitMQ Thresholds ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page
      .getByTestId("check-host-input")
      .fill("rabbitmq-thresholds.test");
    await page.getByTestId("check-username-input").fill("guest");

    await page.getByTestId("check-rabbitmq-mode-select").click();
    await page.getByRole("option", { name: "Management API" }).click();

    await page
      .getByTestId("check-rabbitmq-management-port-input")
      .fill("15672");
    await page.getByTestId("check-rabbitmq-memory-warning-input").fill("70%");
    await page
      .getByTestId("check-rabbitmq-memory-critical-input")
      .fill("90%");
    await page.getByTestId("check-rabbitmq-disk-warning-input").fill("20GiB");
    await page.getByTestId("check-rabbitmq-disk-critical-input").fill("5GiB");

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    const created = await getCheck(page, token, uid);
    expect(created.config.mode).toBe("management");
    expect(created.config.managementPort).toBe(15672);
    expect(created.config.memoryUsedWarning).toBe("70%");
    expect(created.config.memoryUsedCritical).toBe("90%");
    expect(created.config.diskFreeWarning).toBe("20GiB");
    expect(created.config.diskFreeCritical).toBe("5GiB");
    expect(created.config.queue).toBeUndefined();

    // Reload the edit page — values must reflect what was actually persisted.
    await page.goto(`orgs/test/checks/${uid}/edit`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-rabbitmq-mode-select")).toContainText(
      "Management API",
    );
    await expect(
      page.getByTestId("check-rabbitmq-management-port-input"),
    ).toHaveValue("15672");
    await expect(
      page.getByTestId("check-rabbitmq-memory-warning-input"),
    ).toHaveValue("70%");
    await expect(
      page.getByTestId("check-rabbitmq-memory-critical-input"),
    ).toHaveValue("90%");
    await expect(
      page.getByTestId("check-rabbitmq-disk-warning-input"),
    ).toHaveValue("20GiB");
    await expect(
      page.getByTestId("check-rabbitmq-disk-critical-input"),
    ).toHaveValue("5GiB");

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  });
});
