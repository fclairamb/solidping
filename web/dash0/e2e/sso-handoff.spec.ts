import { createServer, type Server } from "node:http";
import { createSign, generateKeyPairSync, type KeyObject } from "node:crypto";
import { test, expect, type Page } from "@playwright/test";
import { API_BASE, DASH_BASE, escapeRegExp, fetchAuthCapabilities, uniqueStamp } from "./fixtures";

/**
 * Spec 2026-09-25-12: a federated login never puts the session in a URL.
 *
 * The provider callback stores the session under a single-use handoff code and
 * redirects to `/d/auth/complete?code=…`; the dashboard redeems the code with
 * a POST. These tests watch every navigation (redirect hops included) and fail
 * on any URL carrying a token.
 *
 * The full OIDC round trip needs the server to trust the fake identity
 * provider below, which only works when the server was STARTED with
 *
 *   SP_OIDC_ENABLED=true
 *   SP_OIDC_ISSUER_URL=http://127.0.0.1:<E2E_FAKE_OIDC_PORT>
 *   SP_OIDC_CLIENT_ID=e2e-client  SP_OIDC_CLIENT_SECRET=e2e-secret
 *
 * and E2E_FAKE_OIDC_PORT is set for the test run. CI does this on its side-car
 * server (.github/workflows/ci.yml). Elsewhere the two OIDC tests skip with a
 * reason; the other two run against any server.
 */

const FAKE_OIDC_PORT = process.env.E2E_FAKE_OIDC_PORT
  ? parseInt(process.env.E2E_FAKE_OIDC_PORT, 10)
  : undefined;
const FAKE_OIDC_CLIENT_ID = process.env.E2E_FAKE_OIDC_CLIENT_ID ?? "e2e-client";
const TOKEN_PARAM = /[?&#](access_token|refresh_token|expires_in)=/;

/** Records every navigation URL, redirect hops included. */
function recordNavigations(page: Page): string[] {
  const urls: string[] = [];
  page.on("request", (request) => {
    if (request.isNavigationRequest()) urls.push(request.url());
  });
  page.on("framenavigated", (frame) => {
    if (frame === page.mainFrame()) urls.push(frame.url());
  });
  return urls;
}

function base64url(input: Buffer | string): string {
  return Buffer.from(input).toString("base64url");
}

/** Who the fake identity provider signs in. */
interface FakeIdentity {
  sub: string;
  email: string;
}

/**
 * Provisions a REAL ordinary member of a fresh `acc-*` org who is NOT a member
 * of `test` (the seeded `test@test.com` is a superadmin, which never takes the
 * paths under test). Same recipe as accessible-org-redirect.spec.ts: the
 * test-only user-seed endpoint, a real login, then POST /api/v1/orgs.
 *
 * @returns the member's email and the slug of its single org.
 */
async function seedMemberOfOwnOrg(page: Page, stamp: string): Promise<{ email: string; slug: string }> {
  const email = `sso-member-${stamp}@elsewhere.example`;
  const password = "Strong-Pass-123!";

  const created = await page.request.post(`${API_BASE}/api/v1/test/users`, {
    data: { email, password, name: "SSO Member" },
  });
  test.skip(
    created.status() !== 201,
    `test user-seed endpoint unavailable (server not in SP_RUNMODE=test?): ${created.status()}`,
  );

  const login = await page.request.post(`${API_BASE}/api/v1/auth/login`, { data: { email, password } });
  expect(login.status()).toBe(200);
  const { accessToken } = (await login.json()) as { accessToken: string };

  const slug = `acc-${stamp}`;
  const org = await page.request.post(`${API_BASE}/api/v1/orgs`, {
    headers: { Authorization: `Bearer ${accessToken}` },
    data: { name: `SSO Member Org ${stamp}`, slug },
  });
  expect(org.status()).toBe(201);

  return { email, slug };
}

/** The seeded test-mode user, a member of org `test`. */
const TEST_MEMBER: FakeIdentity = { sub: "e2e-oidc-test-user", email: "test@test.com" };

/**
 * Just enough of an OpenID provider for the server's go-oidc connector:
 * discovery, an authorize endpoint that approves immediately, a token endpoint
 * returning an RS256 ID token for `identity()`, and the JWKS to verify it.
 */
function startFakeOIDCProvider(port: number, identity: () => FakeIdentity): Promise<Server> {
  const issuer = `http://127.0.0.1:${port}`;
  const { privateKey, publicKey } = generateKeyPairSync("rsa", { modulusLength: 2048 });
  const jwk = { ...publicKey.export({ format: "jwk" }), kid: "e2e-key", alg: "RS256", use: "sig" };

  const idToken = (key: KeyObject): string => {
    const now = Math.floor(Date.now() / 1000);
    const header = base64url(JSON.stringify({ alg: "RS256", typ: "JWT", kid: "e2e-key" }));
    const payload = base64url(
      JSON.stringify({
        iss: issuer,
        aud: FAKE_OIDC_CLIENT_ID,
        sub: identity().sub,
        email: identity().email,
        email_verified: true,
        name: "Test User",
        iat: now,
        exp: now + 300,
      }),
    );
    const signature = createSign("RSA-SHA256").update(`${header}.${payload}`).sign(key);
    return `${header}.${payload}.${base64url(signature)}`;
  };

  const server = createServer((req, res) => {
    const url = new URL(req.url ?? "/", issuer);
    const json = (body: unknown) => {
      res.writeHead(200, { "Content-Type": "application/json" });
      res.end(JSON.stringify(body));
    };

    switch (url.pathname) {
      case "/.well-known/openid-configuration":
        return json({
          issuer,
          authorization_endpoint: `${issuer}/authorize`,
          token_endpoint: `${issuer}/token`,
          jwks_uri: `${issuer}/jwks`,
          response_types_supported: ["code"],
          subject_types_supported: ["public"],
          id_token_signing_alg_values_supported: ["RS256"],
        });
      case "/jwks":
        return json({ keys: [jwk] });
      case "/authorize": {
        const back = new URL(url.searchParams.get("redirect_uri") ?? "");
        back.searchParams.set("code", "e2e-provider-code");
        back.searchParams.set("state", url.searchParams.get("state") ?? "");
        res.writeHead(302, { Location: back.toString() });
        return res.end();
      }
      case "/token":
        // Drain the form body before answering.
        req.resume();
        req.on("end", () =>
          json({
            access_token: "e2e-provider-access-token",
            token_type: "Bearer",
            expires_in: 3600,
            id_token: idToken(privateKey),
          }),
        );
        return;
      default:
        res.writeHead(404);
        res.end();
    }
  });

  return new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(port, "127.0.0.1", () => resolve(server));
  });
}

test.describe("Federated login handoff", () => {
  test.describe("OIDC round trip", () => {
    let idp: Server | undefined;
    let identity: FakeIdentity = TEST_MEMBER;

    test.beforeAll(async () => {
      if (!FAKE_OIDC_PORT) return;
      const { providers } = await fetchAuthCapabilities(API_BASE);
      if (!providers.some((p) => p.type === "oidc")) return;
      idp = await startFakeOIDCProvider(FAKE_OIDC_PORT, () => identity);
    });

    test.afterAll(async () => {
      await new Promise<void>((resolve) => (idp ? idp.close(() => resolve()) : resolve()));
    });

    test.beforeEach(() => {
      test.skip(
        !idp,
        "requires a server started with SP_OIDC_ISSUER_URL pointing at the fake IdP " +
          "and E2E_FAKE_OIDC_PORT set — see file header",
      );
      identity = TEST_MEMBER;
    });

    test("lands on the dashboard signed in, with no token in any URL", async ({ page }) => {
      const urls = recordNavigations(page);
      const returnTo = `${DASH_BASE}/orgs/test/checks`;

      // What the login page's OIDC button does (lib/login-destination.ts).
      await page.goto(
        `${API_BASE}/api/v1/auth/oidc/login?org=test&redirect_uri=${encodeURIComponent(returnTo)}`,
      );

      await page.waitForURL(new RegExp(`${escapeRegExp(returnTo)}$`), { timeout: 20000 });
      await expect(page.getByTestId("app-sidebar")).toBeVisible({ timeout: 15000 });

      // The session is stored, and it is a real one.
      const stored = await page.evaluate(() => ({
        token: localStorage.getItem("solidping_session_token"),
        refresh: localStorage.getItem("solidping_refresh_token"),
      }));
      expect(stored.token).toBeTruthy();
      expect(stored.refresh).toBeTruthy();

      // It went through the handoff route with a code...
      expect(urls.some((u) => /\/d\/auth\/complete\?code=/.test(u))).toBe(true);
      // ...and not a single hop carried a token.
      const leaks = urls.filter((u) => TOKEN_PARAM.test(u));
      expect(leaks).toEqual([]);
      for (const u of urls) {
        expect(u).not.toContain(stored.token as string);
        expect(u).not.toContain(stored.refresh as string);
      }
    });

    test("an account the org does not admit lands on /no-org with an org-less session", async ({
      page,
    }) => {
      const stamp = uniqueStamp();
      identity = { sub: `e2e-outsider-${stamp}`, email: `outsider-${stamp}@elsewhere.example` };
      const urls = recordNavigations(page);

      await page.goto(
        `${API_BASE}/api/v1/auth/oidc/login?org=test&redirect_uri=${encodeURIComponent(`${DASH_BASE}/orgs/test`)}`,
      );

      await page.waitForURL(/\/d\/no-org\?membershipPending=test$/, { timeout: 20000 });

      const stored = await page.evaluate(() => ({
        token: localStorage.getItem("solidping_session_token"),
        refresh: localStorage.getItem("solidping_refresh_token"),
      }));
      expect(stored.token).toBeTruthy();
      expect(stored.refresh, "an org-less session has no refresh token").toBeNull();

      expect(urls.some((u) => /\/d\/auth\/complete\?code=[^&]+&membershipPending=test/.test(u))).toBe(
        true,
      );
      expect(urls.filter((u) => TOKEN_PARAM.test(u))).toEqual([]);
      for (const u of urls) {
        expect(u).not.toContain(stored.token as string);
      }
    });

    // Spec 2026-09-25-15. The login is started from ANOTHER org's login page
    // (`test`), which refuses it; the account is a real member of `acc-*`. It
    // used to get an org-less session, be sent to its own org, 403 there, fail
    // the socket's refresh (no refresh token) and land on the login page as
    // "session expired". It now lands on its own org with a full session, and
    // the join request to `test` is still sent and still named.
    test.describe("a member of another org", () => {
      test("lands on their own org with a full session, not 'session expired'", async ({ page }) => {
        const stamp = uniqueStamp();
        const member = await seedMemberOfOwnOrg(page, stamp);
        identity = { sub: `e2e-member-${stamp}`, email: member.email };

        const urls = recordNavigations(page);
        const consoleErrors: string[] = [];
        page.on("console", (msg) => {
          if (msg.type() === "error") consoleErrors.push(msg.text());
        });
        const forbidden: string[] = [];
        page.on("response", (response) => {
          if (response.status() === 403) forbidden.push(response.url());
        });

        await page.goto(
          `${API_BASE}/api/v1/auth/oidc/login?org=test&redirect_uri=${encodeURIComponent(`${DASH_BASE}/orgs/test`)}`,
        );

        await page.waitForURL(new RegExp(`${escapeRegExp(`${DASH_BASE}/orgs/${member.slug}`)}(/|$)`), {
          timeout: 20000,
        });

        // Told why they are not on `test`.
        await expect(page.getByText(new RegExp(`signed in to ${escapeRegExp(member.slug)}`))).toBeVisible({
          timeout: 10000,
        });

        await expect(page.getByTestId("app-sidebar")).toBeVisible({ timeout: 15000 });

        // A full session: the refresh token is what an org-less one lacked.
        const stored = await page.evaluate(() => ({
          token: localStorage.getItem("solidping_session_token"),
          refresh: localStorage.getItem("solidping_refresh_token"),
        }));
        expect(stored.token).toBeTruthy();
        expect(stored.refresh, "a session on the user's own org carries a refresh token").toBeTruthy();

        // Through the handoff, still naming the org that has the request.
        expect(
          urls.some((u) => /\/d\/auth\/complete\?code=[^&]+&membershipPending=test/.test(u)),
        ).toBe(true);
        expect(urls.filter((u) => TOKEN_PARAM.test(u))).toEqual([]);

        // Settle, then prove the kick-out never started.
        await page.waitForLoadState("networkidle");
        expect(urls.filter((u) => u.includes("session_expired=true"))).toEqual([]);
        // Not even a detour: the fresh session must not be bounced through a
        // login page (the handoff route used to navigate before the router saw
        // the stored session).
        expect(urls.filter((u) => /\/orgs\/[^/?]+\/login/.test(u))).toEqual([]);
        expect(consoleErrors.filter((m) => m.includes("token refresh failed"))).toEqual([]);
        expect(forbidden, "no org-scoped request may be refused on the member's own org").toEqual([]);
        await expect(page).toHaveURL(new RegExp(`${escapeRegExp(`${DASH_BASE}/orgs/${member.slug}`)}(/|$)`));
      });
    });
  });

  test("a code that cannot be redeemed shows an error, not a session", async ({ page }) => {
    const urls = recordNavigations(page);

    await page.goto("auth/complete?code=bogus-code-that-was-never-issued");

    await expect(page.getByTestId("auth-complete-back-to-login")).toBeVisible({ timeout: 10000 });
    // The code left the address bar as soon as the page read it.
    await expect(page).toHaveURL(new RegExp(`${escapeRegExp(DASH_BASE)}/auth/complete$`));
    expect(await page.evaluate(() => localStorage.getItem("solidping_session_token"))).toBeNull();
    expect(urls.filter((u) => TOKEN_PARAM.test(u))).toEqual([]);
  });

  // TODO(remove after next release): spec 2026-09-25-12
  // oauth-callback-one-time-code-exchange. A callback answered by a pod still
  // on the previous release redirects with the tokens in the query string;
  // the dashboard keeps accepting that for one release.
  test("legacy ?access_token= redirect still signs in", async ({ page }) => {
    const login = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { org: "test", email: "test@test.com", password: "test" },
    });
    expect(login.ok()).toBe(true);
    const session = (await login.json()) as {
      accessToken: string;
      refreshToken: string;
      expiresIn: number;
    };

    const query = new URLSearchParams({
      access_token: session.accessToken,
      refresh_token: session.refreshToken,
      expires_in: String(session.expiresIn),
      org: "test",
    });
    await page.goto(`orgs/test/checks?${query.toString()}`);

    await expect(page.getByTestId("app-sidebar")).toBeVisible({ timeout: 15000 });
    // Stripped before the router mounted, deep path kept.
    await expect(page).toHaveURL(new RegExp(`${escapeRegExp(DASH_BASE)}/orgs/test/checks$`));
    expect(await page.evaluate(() => localStorage.getItem("solidping_refresh_token"))).toBe(
      session.refreshToken,
    );
  });
});
