import { test, expect, type Page } from "./fixtures";

// Regression coverage for the "live updates silently unavailable" incident
// (spec 2026-07-10-03): the realtime WebSocket connected (101 Switching
// Protocols) but never got past auth — the client sent one `auth` frame
// carrying a token for the WRONG org, the server closed it 4403, and the UI
// parked on the "Live updates unavailable" badge with no `hello`, no
// `subscribe`, and no events, while the rest of the page kept rendering from
// cached REST data.
//
// Two guarantees are locked in here:
//   1. Happy path — the socket completes the FULL handshake on the real
//      wire (`auth` sent -> `hello` received -> `subscribe` sent ->
//      `subscribed` ack) AND a server-side change (a heartbeat) reaches the
//      UI live, without a reload. Nothing else asserts the whole frame
//      sequence explicitly.
//   2. Regression — a valid token scoped to a DIFFERENT org on the auth frame
//      is closed by the server (no `hello`), and the sidebar live-status dot
//      lands on the terminal "disabled" state (the badge the incident showed),
//      never "live".

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
  const hbToken = `e2e-hs-${Date.now()}-${Math.floor(Math.random() * 1e6)}`;
  const resp = await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
    headers: { Authorization: `Bearer ${token}` },
    data: {
      name,
      type: "heartbeat",
      config: { token: hbToken },
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

/**
 * Frame-level view of the realtime `/events/ws` socket. Records — in wire
 * order, via a monotonic sequence counter — the first `auth` frame the client
 * sends and the first `hello`/`subscribe`/`subscribed` milestones, so a test
 * can assert both that each frame appeared and that they appeared in the
 * correct order. Attaches to whichever socket opens first for `/events/ws`
 * (there is exactly one per org layout).
 */
function watchHandshake(page: Page) {
  let seq = 0;
  const hs = {
    firstSentType: null as string | null,
    authSentSeq: 0,
    helloSeq: 0,
    subscribeSentSeq: 0,
    subscribedSeq: 0,
    closed: false,
    get authSent() {
      return this.authSentSeq > 0;
    },
    get helloReceived() {
      return this.helloSeq > 0;
    },
    get subscribeSent() {
      return this.subscribeSentSeq > 0;
    },
    get subscribedReceived() {
      return this.subscribedSeq > 0;
    },
  };

  const typeOf = (payload: string | Buffer): string => {
    const text = typeof payload === "string" ? payload : "";
    const match = text.match(/"type"\s*:\s*"([^"]+)"/);
    return match ? match[1] : "";
  };

  page.on("websocket", (ws) => {
    if (!ws.url().includes("/events/ws")) return;

    ws.on("framesent", (frame) => {
      const type = typeOf(frame.payload);
      if (hs.firstSentType === null) hs.firstSentType = type;
      if (type === "auth" && hs.authSentSeq === 0) hs.authSentSeq = ++seq;
      if (type === "subscribe" && hs.subscribeSentSeq === 0) {
        hs.subscribeSentSeq = ++seq;
      }
    });

    ws.on("framereceived", (frame) => {
      const type = typeOf(frame.payload);
      if (type === "hello" && hs.helloSeq === 0) hs.helloSeq = ++seq;
      if (type === "subscribed" && hs.subscribedSeq === 0) {
        hs.subscribedSeq = ++seq;
      }
    });

    ws.on("close", () => {
      hs.closed = true;
    });
  });

  return hs;
}

test.describe("Live updates handshake", () => {
  test("the check detail socket completes auth -> hello -> subscribe -> subscribed and delivers a heartbeat live without a reload", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const check = await createHeartbeatCheck(
      page,
      token,
      `E2E Handshake ${Date.now()}`,
    );

    try {
      // Watch frames from before the socket exists so nothing is missed.
      const hs = watchHandshake(page);

      await page.goto(`orgs/test/checks/${check.uid}`);
      await expect(page.getByTestId("check-detail-header")).toBeVisible();

      // The whole handshake must complete on the real socket.
      await expect.poll(() => hs.authSent, { timeout: 15000 }).toBe(true);
      await expect.poll(() => hs.helloReceived, { timeout: 15000 }).toBe(true);
      await expect.poll(() => hs.subscribeSent, { timeout: 15000 }).toBe(true);
      await expect.poll(() => hs.subscribedReceived, { timeout: 15000 }).toBe(true);

      // ...and in the right order: the very first frame the client sends is
      // `auth`, the server acks it with `hello` before any `subscribe` goes
      // out, and the `subscribed` ack follows the `subscribe`.
      expect(hs.firstSentType, "the first client frame must be auth").toBe(
        "auth",
      );
      expect(hs.authSentSeq).toBeLessThan(hs.helloSeq);
      expect(hs.helloSeq).toBeLessThan(hs.subscribeSentSeq);
      expect(hs.subscribeSentSeq).toBeLessThan(hs.subscribedSeq);

      // A server-side change (the first heartbeat) reaches the UI live — the
      // summary card flips to "Currently up for …" within a couple of
      // seconds, far under the fallback poll, so only the live hint path can
      // have produced it. No reload.
      await sendHeartbeat(page, check, "up");
      await expect(page.getByText("Currently up for")).toBeVisible({
        timeout: 4000,
      });
    } finally {
      await deleteCheck(page, token, check.uid);
    }
  });

  test("a valid token for a DIFFERENT org is closed by the server with no hello and lands on the disabled live badge", async ({
    page,
  }) => {
    // Reproduce the incident's exact credential shape: a *fresh, valid*
    // non-super-admin token scoped to another org. The seeded test user is a
    // super-admin (claims role "superadmin"), which bypasses the WS org check,
    // so it cannot reproduce the 4403 — mint a regular user + their own org
    // instead. `POST /api/v1/test/users` is SP_RUNMODE=test only; skip cleanly
    // when it is unavailable, mirroring create-org.spec.ts.
    const stamp = Date.now();
    const email = `wrong-org-${stamp}@unknown.example`;
    const password = "Strong-Pass-123!";

    const createUserResp = await page.request.post(
      `${API_BASE}/api/v1/test/users`,
      { data: { email, password, name: "Wrong Org User" } },
    );
    if (createUserResp.status() !== 201) {
      test.skip(
        true,
        `test user-seed endpoint unavailable (server not in SP_RUNMODE=test?): ${createUserResp.status()}`,
      );
    }

    // Log in (zero org), then create the user's own org — the create-org
    // response mints a fresh org-scoped token with role "admin" (NOT
    // super-admin), whose orgSlug claim is this new org.
    const loginResp = await page.request.post(
      `${API_BASE}/api/v1/auth/login`,
      { data: { email, password } },
    );
    expect(loginResp.status()).toBe(200);
    const session = (await loginResp.json()) as { accessToken: string };

    const orgSlug = `wrongorg-${stamp.toString(36)}`;
    const createOrgResp = await page.request.post(`${API_BASE}/api/v1/orgs`, {
      headers: { Authorization: `Bearer ${session.accessToken}` },
      data: { name: `Wrong Org Co ${stamp}`, slug: orgSlug },
    });
    expect(createOrgResp.status()).toBe(201);
    const orgSession = (await createOrgResp.json()) as {
      accessToken: string;
      refreshToken?: string;
      expiresIn?: number;
    };
    expect(orgSession.accessToken).toBeTruthy();

    // Seed the wrong-org token before the app loads, then browse the `test`
    // org — the socket will dial /orgs/test/events/ws and send this org-B
    // token on the auth frame.
    await page.addInitScript(
      ({ accessToken, refreshToken, expiresIn }) => {
        localStorage.setItem("solidping_session_token", accessToken as string);
        if (refreshToken) {
          localStorage.setItem("solidping_refresh_token", refreshToken as string);
        }
        if (expiresIn) {
          localStorage.setItem(
            "solidping_expires_at",
            String(Date.now() + Number(expiresIn) * 1000),
          );
          localStorage.setItem("solidping_expires_in", String(expiresIn));
        }
      },
      {
        accessToken: orgSession.accessToken,
        refreshToken: orgSession.refreshToken ?? "",
        expiresIn: orgSession.expiresIn ?? 0,
      },
    );

    const hs = watchHandshake(page);
    await page.goto("orgs/test/checks");

    // The sidebar live-status dot must reach the terminal "disabled" state:
    // that is the client's reaction to a 4403 (or 4404) permanent close only —
    // a generic/network close would be "reconnecting", and a successful auth
    // would be "live". Reaching "disabled" here is the "Live updates
    // unavailable" badge the incident reported.
    const dot = page.getByTestId("live-status-dot");
    await expect(dot).toHaveAttribute("data-status", "disabled", {
      timeout: 20000,
    });

    // The socket dialed and tried to authenticate, the server rejected it, and
    // no `hello` ever arrived — the deny path, not a subscribe-time rejection.
    expect(hs.authSent, "the client must have sent an auth frame").toBe(true);
    expect(
      hs.helloReceived,
      "the server must never send hello on a wrong-org token",
    ).toBe(false);
    expect(
      hs.subscribedReceived,
      "no scope can be subscribed when auth is rejected",
    ).toBe(false);
  });
});
