import { test, expect, API_BASE, type Page } from "./fixtures";
import { expandSection } from "./section-helpers";

// The ICMP burst settings (spec 2026-09-21-02): `count`, `interval`,
// `packet_size` and `ttl` were accepted by the checker but unreachable from
// the form — a user concluded one ping per period was the product's floor,
// deleted his checks and left. This spec proves the new "Ping burst & packet
// options" section reaches the stored config and — the contract the spec
// calls out explicitly — that a burst configured over the API survives a
// later edit in the dashboard, since owning these keys means "omit means
// clear" and a module that fails to re-emit them would silently delete the
// burst on the first unrelated save.
//
// What only an end-to-end test reaches:
//   - the four inputs, the disabled-at-count-1 interval gating and the live
//     burst explainer line, through the real form;
//   - a full create → PATCH → re-read round-trip against the real server,
//     where the replace-semantics config merge is what punishes a lost key.

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

async function createIcmpCheck(
  page: Page,
  token: string,
  config: Record<string, unknown>,
): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
    headers: { Authorization: `Bearer ${token}` },
    data: {
      name: `E2E ICMP burst ${Date.now()}`,
      type: "icmp",
      period: "10s",
      // example.com answers pings from the runner in the CI suite; the
      // assertions here are about stored config, not about packet loss.
      config,
    },
  });
  expect(resp.status()).toBe(201);
  return (await resp.json()).uid;
}

async function getCheck(page: Page, token: string, uid: string) {
  const resp = await page.request.get(
    `${API_BASE}/api/v1/orgs/test/checks/${uid}`,
    { headers: { Authorization: `Bearer ${token}` } },
  );
  expect(resp.status()).toBe(200);
  return await resp.json();
}

test.describe("ICMP burst form fields", () => {
  test("creating a check with count and interval persists both", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    await page.goto("orgs/test/checks/new?checkType=icmp");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    await page.getByTestId("check-name-input").fill(`E2E burst form ${Date.now()}`);
    await page.getByTestId("check-host-input").fill("example.com");

    // Collapsed by default: the section header shows the single-ping summary
    // until it is opened.
    const section = page.getByTestId("check-icmp-burst-section");
    await expect(section).toContainText("single ping");
    await expandSection(page, "check-icmp-burst-section");

    // The explainer is live-computed: "N packets, I apart, every run".
    await page.getByTestId("check-icmp-count-input").fill("10");
    await expect(page.getByTestId("icmp-burst-line")).toContainText(
      "10 packets",
    );

    // Interval is meaningless at count 1; it enables once count > 1. The
    // field is denominated in ms: a bare number is milliseconds.
    await expect(page.getByTestId("check-icmp-interval-input")).toBeEnabled();
    await page.getByTestId("check-icmp-interval-input").fill("100");
    await expect(page.getByTestId("icmp-burst-line")).toContainText("100ms");

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 15000 });
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    const created = await getCheck(page, token, uid);
    expect(created.config.count).toBe(10);
    expect(created.config.interval).toBe("100ms");

    // Edit route reopens on the stored values (the section auto-expands
    // because it holds non-defaults) — displayed back in ms.
    await page.goto(`orgs/test/checks/${uid}/edit`);
    await page.waitForLoadState("networkidle");
    await expandSection(page, "check-icmp-burst-section");
    await expect(page.getByTestId("check-icmp-count-input")).toHaveValue("10");
    await expect(page.getByTestId("check-icmp-interval-input")).toHaveValue(
      "100",
    );

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  });

  test("an API-configured burst survives an unrelated dashboard edit", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    const uid = await createIcmpCheck(page, token, {
      host: "example.com",
      count: 10,
      interval: "100ms",
      ttl: 64,
    });

    try {
      // Open the edit page, touch NOTHING in the burst section, change only
      // the name, save. The four burst keys are owned (omit-to-clear), so
      // this is exactly the save the spec warns about: a module that failed
      // to re-emit them would drop them and the burst would silently die.
      await page.goto(`orgs/test/checks/${uid}/edit`);
      await page.waitForLoadState("networkidle");
      await expect(page.getByTestId("check-name-input")).toBeVisible();
      await page.getByTestId("check-name-input").fill(`E2E burst rename ${Date.now()}`);

      const patched = page.waitForResponse(
        (res) =>
          res.url().includes(`/api/v1/orgs/test/checks/${uid}`) &&
          res.request().method() === "PATCH" &&
          res.status() < 400,
        { timeout: 15000 },
      );
      await page.getByTestId("check-submit-button").click();
      await patched;
      await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 15000 });

      const after = await getCheck(page, token, uid);
      expect(after.config.count).toBe(10);
      expect(after.config.interval).toBe("100ms");
      expect(after.config.ttl).toBe(64);
    } finally {
      await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
        headers: { Authorization: `Bearer ${token}` },
      });
    }
  });

  test("clearing a burst field deletes the stored key", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    const uid = await createIcmpCheck(page, token, {
      host: "example.com",
      count: 10,
      interval: "100ms",
    });

    try {
      await page.goto(`orgs/test/checks/${uid}/edit`);
      await page.waitForLoadState("networkidle");
      await expandSection(page, "check-icmp-burst-section");
      await expect(page.getByTestId("check-icmp-count-input")).toHaveValue("10");

      await page.getByTestId("check-icmp-count-input").fill("");
      // Interval greys out again the moment the count drops to the single
      // ping default.
      await expect(page.getByTestId("check-icmp-interval-input")).toBeDisabled();

      const patched = page.waitForResponse(
        (res) =>
          res.url().includes(`/api/v1/orgs/test/checks/${uid}`) &&
          res.request().method() === "PATCH" &&
          res.status() < 400,
        { timeout: 15000 },
      );
      await page.getByTestId("check-submit-button").click();
      await patched;
      await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 15000 });

      const after = await getCheck(page, token, uid);
      expect(after.config.count).toBeUndefined();
      expect(after.config.interval).toBeUndefined();
    } finally {
      await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
        headers: { Authorization: `Bearer ${token}` },
      });
    }
  });
});
