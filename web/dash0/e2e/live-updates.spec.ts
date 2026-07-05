import type { WebSocket } from "@playwright/test";
import { test, expect, type Page } from "./fixtures";

// Live dashboard updates via org-scoped hint events (WebSocket v2).
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

/** Waits for the realtime v2 socket to open and reach the `subscribed`
 * state for at least one scope — the client-side signal that live updates
 * are flowing, equivalent to v1's "stream connected" 200 response wait. */
async function waitForLiveSubscribed(page: Page): Promise<WebSocket> {
  const ws = await page.waitForEvent("websocket", {
    predicate: (socket) => socket.url().includes("/events/ws"),
    timeout: 15000,
  });
  await new Promise<void>((resolve, reject) => {
    const timer = setTimeout(
      () => reject(new Error("timed out waiting for a subscribed ack")),
      15000,
    );
    ws.on("framereceived", (frame) => {
      const text = typeof frame.payload === "string" ? frame.payload : "";
      if (text.includes('"type":"subscribed"')) {
        clearTimeout(timer);
        resolve();
      }
    });
  });
  return ws;
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

      // Load the dashboard and wait for at least one scope to be
      // subscribed-and-acked before driving the test scenario.
      const subscribedPromise = waitForLiveSubscribed(page);
      await page.goto("orgs/test");
      await subscribedPromise;
      await expect(page.getByTestId("kpi-tile-incidents")).toBeVisible();

      // Fail the check: with a zero confirmation window the incident opens
      // on this heartbeat. The dashboard must show it live — well under the
      // 30s incident poll (stretched to minutes while the scope is live).
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
      const subscribedPromise = waitForLiveSubscribed(page);
      await page.goto(`orgs/test/checks/${check.uid}`);
      await subscribedPromise;
      await expect(page.getByTestId("check-detail-header")).toBeVisible();

      // While the check scope is live, the 1.5s first-result hot poll is
      // disabled: count the check-detail refetches over an idle window. A
      // 1.5s poll would fire at least twice; the live path fires none.
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
        "no 1.5s hot poll may run while the check's live subscription is acked",
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

  test("reconnect after a server-initiated close (4401-style) replays subscriptions and stays live", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const check = await createHeartbeatCheck(
      page,
      token,
      `E2E Live Reconnect ${Date.now()}`,
    );

    try {
      await sendHeartbeat(page, check, "up");

      // Route the realtime socket through a pass-through proxy so the test
      // can force-close the *first* connection the same way an expired
      // access token would server-side (close 4401), without touching real
      // token expiry timing. connectToServer() keeps the real backend doing
      // the actual auth/subscribe/hint work; this proxy only intercepts
      // frames to detect the first `subscribed` ack and then kill that one
      // connection — every subsequent connection (the reconnect) passes
      // through untouched.
      let connections = 0;
      let firstConnectionClosed = false;
      await page.routeWebSocket(/\/events\/ws/, (clientWS) => {
        const connectionIndex = connections++;
        const serverWS = clientWS.connectToServer();

        serverWS.onMessage((message) => {
          clientWS.send(message); // manual forward (onMessage disables auto-forward)
          const text = typeof message === "string" ? message : "";
          if (connectionIndex === 0 && text.includes('"type":"subscribed"')) {
            firstConnectionClosed = true;
            void clientWS.close({ code: 4401, reason: "e2e forced reconnect" });
          }
        });
        clientWS.onMessage((message) => serverWS.send(message)); // manual forward the other direction
      });

      const firstSubscribed = waitForLiveSubscribed(page);
      await page.goto("orgs/test");
      await firstSubscribed;
      await expect(page.getByTestId("kpi-tile-incidents")).toBeVisible();

      // A fresh socket must open, authenticate, and resubscribe — same
      // scopes, replayed automatically by the registry after reconnect.
      const secondSubscribed = waitForLiveSubscribed(page);
      await expect
        .poll(() => firstConnectionClosed, { timeout: 5000 })
        .toBe(true);
      await secondSubscribed;
      expect(connections).toBeGreaterThanOrEqual(2);

      // Live updates keep working after the reconnect: no manual reload
      // needed, the replayed `checks`/`incidents` subscriptions still route
      // hints to the dashboard.
      await sendHeartbeat(page, check, "down");
      await expect(
        page.getByTestId("active-incidents").getByText(check.name),
      ).toBeVisible({ timeout: 4000 });
    } finally {
      await deleteCheck(page, token, check.uid);
    }
  });

  test("sidebar live-status dot shows green once the dashboard is streaming", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    const subscribedPromise = waitForLiveSubscribed(page);
    await page.goto("orgs/test");
    await subscribedPromise;

    const dot = page.getByTestId("live-status-dot");
    await expect(dot).toBeVisible();
    await expect(dot).toHaveAttribute("data-status", "live", { timeout: 4000 });
  });

  test("sidebar live-status dot turns red on drop and green again after the automatic reconnect", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Same pass-through proxy pattern as the reconnect-replay test above:
    // let the real backend do all the auth/subscribe work, only intercept
    // frames to force-close the first connection right after it's live.
    // Each connection is only ever force-closed once (guarded by
    // `closed`) — the backend acks all three dashboard scopes
    // (checks/incidents/events) in quick succession, and closing on the
    // very first ack must not re-trigger on the next two.
    let connections = 0;
    let closed = false;
    await page.routeWebSocket(/\/events\/ws/, (clientWS) => {
      const connectionIndex = connections++;
      const serverWS = clientWS.connectToServer();

      serverWS.onMessage((message) => {
        clientWS.send(message);
        const text = typeof message === "string" ? message : "";
        if (connectionIndex === 0 && !closed && text.includes('"type":"subscribed"')) {
          closed = true;
          void clientWS.close({ code: 4401, reason: "e2e forced reconnect" });
        }
      });
      clientWS.onMessage((message) => serverWS.send(message));
    });

    const dot = page.getByTestId("live-status-dot");

    // Arm the "reconnecting" watch before navigating, in parallel with (not
    // after) the initial connect/close/reconnect cycle: on localhost the
    // whole cycle — forced close, ~1-2.6s backoff, redial, hello, ack — can
    // complete in well under a second, faster than a sequential
    // wait-for-live-then-poll-for-red could ever observe. waitForFunction
    // evaluates immediately and keeps re-checking, so it catches the
    // transient red state no matter how quickly it passes; a sequential
    // check armed only after confirming "live" first would already be too
    // late to see it on a fast enough machine.
    const sawReconnecting = page.waitForFunction(
      () =>
        document.querySelector('[data-testid="live-status-dot"]')?.getAttribute("data-status") ===
        "reconnecting",
      { timeout: 8000 },
    );

    const firstSubscribed = waitForLiveSubscribed(page);
    await page.goto("orgs/test");
    await firstSubscribed;

    // The forced close must flip the dot to red (reconnecting) before the
    // automatic reconnect brings it back to green — never straight back to
    // gray, which would hide that something actually broke.
    await sawReconnecting;

    // The automatic reconnect brings it back to green.
    await expect(dot).toHaveAttribute("data-status", "live", { timeout: 10000 });
    expect(connections).toBeGreaterThanOrEqual(2);
  });
});
