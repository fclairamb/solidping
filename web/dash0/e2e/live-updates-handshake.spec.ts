import { test, expect, type Page, API_BASE } from "./fixtures";

// Regression coverage for the "live updates silently unavailable" incident
// (spec 2026-07-10-03), updated for spec 2026-07-14-04 which moved WS auth to
// the HTTP level: the realtime WebSocket authenticates via the access_token
// cookie at handshake time (no in-band `auth` frame). A wrong-org cookie still
// upgrades and is closed 4403; a wrong-org page parks on the "Live updates
// unavailable" badge with no `hello`, no `subscribe`, and no events, while the
// rest of the page keeps rendering from cached REST data.
//
// Two guarantees are locked in here:
//   1. Happy path — the socket completes the handshake on the real wire (the
//      first client frame is `subscribe`, NOT `auth`; `hello` is received
//      before `subscribe`; `subscribed` acks it) AND a server-side change (a
//      heartbeat) reaches the UI live, without a reload.
//   2. Regression — a valid cookie scoped to a DIFFERENT org is closed by the
//      server (no `hello`), and the sidebar live-status dot lands on the
//      terminal "disabled" state (the badge the incident showed), never "live".

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
 * order, via a monotonic sequence counter — the first frame the client sends
 * and the first `hello`/`subscribe`/`subscribed` milestones, so a test can
 * assert both that each frame appeared and that they appeared in the correct
 * order. With HTTP-level auth there is no in-band `auth` frame, so the first
 * client frame must be `subscribe`. Attaches to whichever socket opens first
 * for `/events/ws` (there is exactly one per org layout).
 */
function watchHandshake(page: Page) {
  let seq = 0;
  const hs = {
    firstSentType: null as string | null,
    helloSeq: 0,
    subscribeSentSeq: 0,
    subscribedSeq: 0,
    closed: false,
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
  test("the check detail socket completes hello -> subscribe -> subscribed (no in-band auth) and delivers a heartbeat live without a reload", async ({
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
      await expect.poll(() => hs.helloReceived, { timeout: 15000 }).toBe(true);
      await expect.poll(() => hs.subscribeSent, { timeout: 15000 }).toBe(true);
      await expect.poll(() => hs.subscribedReceived, { timeout: 15000 }).toBe(true);

      // ...and in the right order: with HTTP-level (cookie) auth the client
      // sends NO in-band `auth` frame — the very first frame it sends is
      // `subscribe`, the server has already sent `hello` before that, and the
      // `subscribed` ack follows the `subscribe`.
      expect(
        hs.firstSentType,
        "the first client frame must be subscribe, not auth",
      ).toBe("subscribe");
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

  test("a valid cookie for a DIFFERENT org is closed by the server with no hello and lands on the disabled live badge", async ({
    page,
  }) => {
    // Reproduce the incident's exact credential shape — a *fresh, valid*
    // non-super-admin token scoped to another org, on a page the app parks you
    // on — under two constraints:
    //
    //   1. The seeded test user is a super-admin (claims role "superadmin"),
    //      which bypasses the WS org check, so it cannot produce a 4403 at all.
    //      Mint a regular user instead. `POST /api/v1/test/users` is
    //      SP_RUNMODE=test only; skip cleanly when it is unavailable, mirroring
    //      create-org.spec.ts.
    //   2. Parking a NON-member on a foreign org — this test's original vehicle
    //      — is no longer a reachable state: spec 2026-09-08-01 §C redirects
    //      that session to an org it can use, so the socket never dials with
    //      the wrong-org token. The incident's state survives where it is still
    //      genuinely reachable, and where it always was the more likely cause
    //      in production: a *member* of the URL's org whose session token is
    //      scoped to ANOTHER of their orgs, for whom the automatic re-mint
    //      (`switchOrg`) fails. `$org.tsx` deliberately drops its loading gate
    //      in that case ("better to render and let per-request 403s surface
    //      than to hang"), so the page mounts with a token scoped to org B
    //      while the URL says org A — precisely the (token-org × target-org)
    //      mismatch the server answers with a 4403, and the shape the backend
    //      pins down in realtimews' TestServe_TwoOrgMemberMatchesREST.
    //
    // So: one regular user, TWO orgs of their own, a token for the second, a
    // failing switch-org, and a navigation to the first.
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

    // Log in (zero org), then create the user's two orgs. Each create-org
    // response mints a fresh org-scoped token with role "admin" (NOT
    // super-admin), whose orgSlug claim is that new org.
    const loginResp = await page.request.post(
      `${API_BASE}/api/v1/auth/login`,
      { data: { email, password } },
    );
    expect(loginResp.status()).toBe(200);
    const session = (await loginResp.json()) as { accessToken: string };

    const createOrg = async (slug: string, name: string) => {
      const resp = await page.request.post(`${API_BASE}/api/v1/orgs`, {
        headers: { Authorization: `Bearer ${session.accessToken}` },
        data: { name, slug },
      });
      expect(resp.status()).toBe(201);
      return (await resp.json()) as {
        accessToken: string;
        refreshToken?: string;
        expiresIn?: number;
      };
    };

    // Org A is the one the URL will name; org B is the one the token is scoped
    // to. The user is a genuine member (owner) of both.
    const orgA = `visitedorg-${stamp.toString(36)}`;
    const orgB = `tokenorg-${stamp.toString(36)}`;
    await createOrg(orgA, `Visited Org ${stamp}`);
    const orgBSession = await createOrg(orgB, `Token Org ${stamp}`);
    expect(orgBSession.accessToken).toBeTruthy();

    // The org layout auto-re-mints the session for the URL's org
    // (`needsOrgSwitch` → `auth.switchOrg`). Fail that call the way a transient
    // backend hiccup would: the layout records the failure and renders anyway,
    // leaving the page on org A holding org B's token. Without this the switch
    // succeeds and the socket connects — so count the calls and assert the
    // vehicle actually engaged.
    let switchOrgAttempts = 0;
    await page.route("**/api/v1/auth/switch-org", async (route) => {
      switchOrgAttempts++;
      await route.fulfill({
        status: 503,
        contentType: "application/json",
        body: JSON.stringify({
          title: "Service unavailable",
          code: "INTERNAL_ERROR",
          detail: "switch-org is unavailable (simulated)",
        }),
      });
    });

    // Seed the wrong-org session before the app loads: localStorage gates the
    // run() loop's dial (getToken must be non-null) and its pre-dial expiry
    // check, while the access_token COOKIE is what actually authenticates the
    // handshake now (HTTP-level auth). Both carry the org-B token so the socket
    // dials /orgs/<orgA>/events/ws and is upgraded, then closed 4403.
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
        accessToken: orgBSession.accessToken,
        refreshToken: orgBSession.refreshToken ?? "",
        expiresIn: orgBSession.expiresIn ?? 0,
      },
    );
    // The browser attaches this cookie to the same-origin WS handshake — it is
    // the credential the server validates before upgrading.
    await page.context().addCookies([
      { name: "access_token", value: orgBSession.accessToken, url: API_BASE },
    ]);

    const hs = watchHandshake(page);
    await page.goto(`orgs/${orgA}/checks`);

    // The sidebar live-status dot must reach the terminal "disabled" state:
    // that is the client's reaction to a 4403 (or 4404) permanent close only —
    // a generic/network close would be "reconnecting", and a successful auth
    // would be "live". Reaching "disabled" here is the "Live updates
    // unavailable" badge the incident reported.
    const dot = page.getByTestId("live-status-dot");
    await expect(dot).toHaveAttribute("data-status", "disabled", {
      timeout: 20000,
    });

    // The vehicle really did what it claims: the app tried to re-mint for org
    // A, was refused, and stayed put rather than redirecting elsewhere. If a
    // future change makes the app navigate away (or makes the switch succeed),
    // these fail loudly instead of letting the assertion above pass vacuously
    // on a page that never held a wrong-org token.
    expect(
      switchOrgAttempts,
      "the layout must have attempted (and failed) the org re-mint",
    ).toBeGreaterThan(0);
    expect(page.url()).toContain(`/orgs/${orgA}`);
    expect(page.url()).not.toContain(`/orgs/${orgB}`);

    // The server rejected the wrong-org socket after the upgrade (close 4403):
    // no `hello` ever arrived and no scope was subscribed. With HTTP-level auth
    // there is no in-band auth frame, so the client sent nothing on the wire.
    expect(
      hs.helloReceived,
      "the server must never send hello on a wrong-org cookie",
    ).toBe(false);
    expect(
      hs.subscribedReceived,
      "no scope can be subscribed when auth is rejected",
    ).toBe(false);
  });
});
