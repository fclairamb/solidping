import { test, expect, type Page } from "./fixtures";

// Live dashboard updates via org-scoped hint events (SSE).
//
// These tests drive a heartbeat check through the public heartbeat endpoint
// and assert the dashboard reflects the change without any reload, within a
// couple of seconds — far quicker than the 30s/60s fallback polls, so a pass
// can only come from the live hint path.

const API_BASE = (
  process.env.E2E_BASE_URL ?? "http://localhost:4000/dash0/"
).replace(/\/dash0\/?$/, "");

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  const body = await resp.json();
  return body.accessToken;
}

interface HeartbeatCheck {
  uid: string;
  name: string;
  hbToken: string;
}

async function createHeartbeatCheck(
  page: Page,
  token: string,
  name: string,
): Promise<HeartbeatCheck> {
  const hbToken = `e2e-live-${Date.now()}-${Math.floor(Math.random() * 1e6)}`;
  const resp = await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
    headers: { Authorization: `Bearer ${token}` },
    data: {
      name,
      type: "heartbeat",
      config: { token: hbToken },
      // Open/resolve incidents on the first failing/passing heartbeat so the
      // live-update latency is the only delay under test.
      confirmationPeriodSeconds: 0,
      recoveryPeriodSeconds: 0,
    },
  });
  expect(resp.status()).toBe(201);
  const body = await resp.json();
  return { uid: body.uid, name, hbToken };
}

async function sendHeartbeat(
  page: Page,
  check: HeartbeatCheck,
  status: "up" | "down",
): Promise<void> {
  const resp = await page.request.get(
    `${API_BASE}/api/v1/heartbeat/test/${check.uid}?token=${check.hbToken}&status=${status}`,
  );
  expect(resp.status()).toBe(200);
}

async function deleteCheck(
  page: Page,
  token: string,
  uid: string,
): Promise<void> {
  await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}

test.describe("Live dashboard updates", () => {
  test("an incident opened by a failing heartbeat appears on the dashboard within ~2s", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const check = await createHeartbeatCheck(
      page,
      token,
      `E2E Live Incident ${Date.now()}`,
    );

    try {
      await sendHeartbeat(page, check, "up");

      // Load the dashboard and wait for the live stream to be established.
      const streamConnected = page.waitForResponse(
        (resp) =>
          resp.url().includes("/events/stream") && resp.status() === 200,
        { timeout: 15000 },
      );
      await page.goto("orgs/test");
      await streamConnected;
      await expect(page.getByTestId("kpi-tile-incidents")).toBeVisible();

      // Fail the check: with a zero confirmation window the incident opens
      // on this heartbeat. The dashboard must show it live — well under the
      // 30s incident poll (stretched to minutes while the stream is live).
      await sendHeartbeat(page, check, "down");

      await expect(
        page.getByTestId("active-incidents").getByText(check.name),
      ).toBeVisible({ timeout: 4000 });

      // Recovery closes the incident live too.
      await sendHeartbeat(page, check, "up");
      await expect(
        page.getByTestId("active-incidents").getByText(check.name),
      ).toBeHidden({ timeout: 4000 });
    } finally {
      await deleteCheck(page, token, check.uid);
    }
  });

  test("a new check's first result appears live on the detail page without the hot poll", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const check = await createHeartbeatCheck(
      page,
      token,
      `E2E Live First Result ${Date.now()}`,
    );

    try {
      // Open the detail page before any result exists.
      const streamConnected = page.waitForResponse(
        (resp) =>
          resp.url().includes("/events/stream") && resp.status() === 200,
        { timeout: 15000 },
      );
      await page.goto(`orgs/test/checks/${check.uid}`);
      await streamConnected;
      await expect(page.getByTestId("check-detail-header")).toBeVisible();

      // While live, the 1.5s first-result hot poll is disabled: count the
      // check-detail refetches over an idle window. A 1.5s poll would fire
      // at least twice; the live path fires none.
      let detailFetches = 0;
      const onRequest = (req: { url: () => string; method: () => string }) => {
        if (
          req.method() === "GET" &&
          req.url().includes(`/api/v1/orgs/test/checks/${check.uid}?`)
        ) {
          detailFetches += 1;
        }
      };
      page.on("request", onRequest);
      await page.waitForTimeout(3500);
      const idleFetches = detailFetches;
      expect(
        idleFetches,
        "no 1.5s hot poll may run while the live stream is connected",
      ).toBeLessThan(2);

      // First real result: must appear live, driven by the hint. The summary
      // card flips from the pending fallback to "Currently up for …".
      await sendHeartbeat(page, check, "up");
      await expect(page.getByText("Currently up for")).toBeVisible({
        timeout: 4000,
      });
      page.off("request", onRequest);
    } finally {
      await deleteCheck(page, token, check.uid);
    }
  });
});
