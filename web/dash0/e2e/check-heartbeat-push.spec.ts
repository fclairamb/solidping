import { test, expect, mockSloCoverage, type Page } from "./fixtures";

// Embedded TCP/UDP heartbeat push transports (spec 2026-09-01-06).
//
// The listeners are off by default and opening their ports is a deployment
// decision, so the dashboard must render this block ONLY when the server says
// a listener is up. The feature response is stubbed here rather than requiring
// the dev server to bind port 4001.

test.beforeEach(async ({ authenticatedPage }) => {
  await mockSloCoverage(authenticatedPage);
});

/**
 * Stub GET /api/v1/features with a given heartbeatPush section.
 *
 * The reload matters: useFeatures caches for five minutes, and the
 * authenticated fixture has already fetched it by the time a test installs
 * this route. Without the reload every assertion below would run against the
 * real (listener-less) response and silently test nothing.
 */
async function mockFeatures(
  page: Page,
  heartbeatPush: Record<string, unknown> | undefined,
): Promise<void> {
  await page.route("**/api/v1/features", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ bugReport: false, heartbeatPush }),
    }),
  );

  await page.reload();
  await page.waitForLoadState("networkidle");
}

/** Create a heartbeat check through the UI and land on its detail page. */
async function createHeartbeatCheck(page: Page, name: string): Promise<void> {
  await page
    .getByTestId("app-sidebar")
    .getByRole("link", { name: "Checks" })
    .click();
  await page.waitForURL(/\/checks/);
  await page.waitForLoadState("networkidle");
  await page.getByTestId("new-check-button").click();
  await page.waitForURL(/\/checks\/new/);
  await page.waitForLoadState("networkidle");

  await expect(page.getByTestId("check-name-input")).toBeVisible();
  await page.getByTestId("check-type-select").click();
  await page.getByRole("option", { name: /Heartbeat/i }).click();
  await page.getByTestId("check-name-input").fill(name);
  await page.getByTestId("check-submit-button").click();

  await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
  await page.waitForLoadState("networkidle");
}

test.describe("Heartbeat push transports", () => {
  test("hidden when no listener is enabled", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    await mockFeatures(page, {
      tcpEnabled: false,
      udpEnabled: false,
      host: "",
      tcpPort: 0,
      udpPort: 0,
    });

    await createHeartbeatCheck(page, `E2E HB Push Off ${Date.now()}`);

    // The HTTPS endpoint is still there — only the push block is absent.
    await expect(page.getByText("Heartbeat Endpoint")).toBeVisible();
    await expect(page.getByTestId("heartbeat-push")).toHaveCount(0);
  });

  test("shows copy-paste examples and the require_hmac toggle", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await mockFeatures(page, {
      tcpEnabled: true,
      udpEnabled: true,
      host: "beats.example.com",
      tcpPort: 4001,
      udpPort: 4001,
    });

    await createHeartbeatCheck(page, `E2E HB Push On ${Date.now()}`);

    await expect(page.getByTestId("heartbeat-push")).toBeVisible();

    // Both one-liners name the advertised host and port and carry the SP1
    // prefix a device actually sends.
    const tcp = page.getByTestId("heartbeat-push-tcp");
    await expect(tcp).toContainText("SP1 ");
    await expect(tcp).toContainText("beats.example.com 4001");

    const udp = page.getByTestId("heartbeat-push-udp");
    await expect(udp).toContainText("nc -u");
    await expect(udp).toContainText("beats.example.com 4001");

    // The rotate-token nudge is a consequence of the toggle, so it must not be
    // shouting at a check that has not turned signing on.
    await expect(page.getByTestId("heartbeat-rotate-nudge")).toHaveCount(0);

    // Capture the ping URL before the toggle: the public side of a config
    // PATCH is REPLACE, so a toggle that sent only its own key would silently
    // destroy the token and break every existing sender.
    const pingUrlBefore = await page
      .locator(".font-mono.break-all span")
      .first()
      .textContent();
    expect(pingUrlBefore).toContain("token=");

    const toggle = page.getByTestId("heartbeat-require-hmac");
    await expect(toggle).toBeVisible();
    await toggle.click();

    // Turning it on surfaces the rotation nudge: the token may already have
    // been sniffed, and it is also the signing key.
    await expect(page.getByTestId("heartbeat-rotate-nudge")).toBeVisible();

    // And the setting survives a reload, i.e. it was actually persisted.
    await page.reload();
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("heartbeat-require-hmac")).toHaveAttribute(
      "data-state",
      "checked",
    );
    await expect(page.getByTestId("heartbeat-rotate-nudge")).toBeVisible();

    const pingUrlAfter = await page
      .locator(".font-mono.break-all span")
      .first()
      .textContent();
    expect(pingUrlAfter).toBe(pingUrlBefore);
  });

  test("shows only the enabled transport", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    await mockFeatures(page, {
      tcpEnabled: false,
      udpEnabled: true,
      host: "beats.example.com",
      tcpPort: 0,
      udpPort: 4001,
    });

    await createHeartbeatCheck(page, `E2E HB Push UDP ${Date.now()}`);

    await expect(page.getByTestId("heartbeat-push")).toBeVisible();
    await expect(page.getByTestId("heartbeat-push-udp")).toBeVisible();
    await expect(page.getByTestId("heartbeat-push-tcp")).toHaveCount(0);
  });
});
