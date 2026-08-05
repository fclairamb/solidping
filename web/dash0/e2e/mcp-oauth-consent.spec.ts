import { expect, type Page } from "@playwright/test";
import { createHash, randomBytes } from "node:crypto";
import { createServer, type Server } from "node:http";
import { test, API_BASE, freshLogin } from "./fixtures";

/**
 * MCP OAuth 2.1 consent flow (spec 2026-06-20-03) — the browser legs an MCP
 * client (e.g. claude.ai) drives when the user clicks "Connect":
 *
 *   GET /api/v1/oauth/authorize  →  (no session cookie) → /dash0/login
 *   → login page resumes returnTo → authorize → consent screen → Approve
 *   → code lands on the client's redirect_uri → PKCE token exchange
 *   → Bearer POST /api/v1/mcp.
 *
 * The regression this file guards: the login bounce used to dead-end on the
 * dashboard. The generic /login route dropped returnTo, the open-redirect
 * guard rejected the (then-absolute) authorize returnTo, and SSO/idle
 * sessions had no `access_token` cookie for /authorize to accept — the
 * browser "came back to solidping" and the MCP client never got its code.
 */

const CALLBACK_ORIGIN = "http://127.0.0.1:39777";
const CALLBACK_URI = `${CALLBACK_ORIGIN}/callback`;

interface PkcePair {
  verifier: string;
  challenge: string;
}

function newPkcePair(): PkcePair {
  const verifier = randomBytes(48).toString("base64url");
  const challenge = createHash("sha256").update(verifier).digest("base64url");
  return { verifier, challenge };
}

async function registerClient(page: Page): Promise<string> {
  const res = await page.request.post(`${API_BASE}/api/v1/oauth/register`, {
    data: {
      client_name: "e2e-consent",
      redirect_uris: [CALLBACK_URI],
      grant_types: ["authorization_code"],
      token_endpoint_auth_method: "none",
    },
  });
  expect(res.status()).toBe(201);
  const body = await res.json();
  return body.client_id as string;
}

function authorizeUrl(clientId: string, pkce: PkcePair, state: string): string {
  const params = new URLSearchParams({
    client_id: clientId,
    redirect_uri: CALLBACK_URI,
    response_type: "code",
    scope: "mcp",
    state,
    code_challenge: pkce.challenge,
    code_challenge_method: "S256",
  });
  return `${API_BASE}/api/v1/oauth/authorize?${params.toString()}`;
}

// A real loopback listener for the client's redirect_uri — the browser must
// actually land there to prove the server's 302 carries the code. (page.route
// interception is unreliable for cross-origin top-level navigations out of a
// form-POST redirect chain, so we serve the callback for real.)
let callbackServer: Server;

test.beforeAll(async () => {
  callbackServer = createServer((_req, res) => {
    res.writeHead(200, { "Content-Type": "text/html" });
    res.end("<title>callback</title>ok");
  });
  await new Promise<void>((resolve) =>
    callbackServer.listen(39777, "127.0.0.1", resolve),
  );
});

test.afterAll(async () => {
  await new Promise<void>((resolve, reject) =>
    callbackServer.close((err) => (err ? reject(err) : resolve())),
  );
});

/** Approve the consent screen and return the code+state from the callback. */
async function approveConsent(
  page: Page,
): Promise<{ code: string; state: string }> {
  await expect(
    page.getByRole("heading", { name: "Authorize MCP access" }),
  ).toBeVisible({ timeout: 15000 });
  await page.getByRole("button", { name: "Approve" }).click();
  await page.waitForURL(`${CALLBACK_ORIGIN}/**`, { timeout: 15000 });
  const url = new URL(page.url());
  return {
    code: url.searchParams.get("code") ?? "",
    state: url.searchParams.get("state") ?? "",
  };
}

async function exchangeAndCallMCP(
  page: Page,
  clientId: string,
  code: string,
  verifier: string,
): Promise<void> {
  const tokenRes = await page.request.post(`${API_BASE}/api/v1/oauth/token`, {
    form: {
      grant_type: "authorization_code",
      code,
      client_id: clientId,
      redirect_uri: CALLBACK_URI,
      code_verifier: verifier,
    },
  });
  expect(tokenRes.status()).toBe(200);
  const tok = await tokenRes.json();
  expect(tok.access_token).toBeTruthy();

  const mcpRes = await page.request.post(`${API_BASE}/api/v1/mcp`, {
    headers: {
      Authorization: `Bearer ${tok.access_token}`,
      "Content-Type": "application/json",
    },
    data: {
      jsonrpc: "2.0",
      id: 1,
      method: "initialize",
      params: {
        protocolVersion: "2025-03-26",
        clientInfo: { name: "e2e-consent", version: "1.0" },
      },
    },
  });
  expect(mcpRes.status()).toBe(200);
  const rpc = await mcpRes.json();
  expect(rpc.result?.protocolVersion).toBeTruthy();
}

test.describe("MCP OAuth consent flow", () => {
  test("authenticated session with a valid cookie goes straight to consent", async ({
    page,
  }) => {
    // Dedicated login rather than the shared worker session: this test
    // exercises the real cookie a fresh login sets, and (like ":156" below)
    // the shared session can be evicted by unrelated login churn elsewhere
    // in the suite well before this file runs — see spec 2026-08-05-02.
    await freshLogin(page);

    const clientId = await registerClient(page);
    const pkce = newPkcePair();

    await page.goto(authorizeUrl(clientId, pkce, "state-cookie"));

    const { code, state } = await approveConsent(page);
    expect(code).toBeTruthy();
    expect(state).toBe("state-cookie");

    await exchangeAndCallMCP(page, clientId, code, pkce.verifier);
  });

  test("SPA session without the access_token cookie resumes via the login bounce", async ({
    page,
  }) => {
    // THE regression case: localStorage holds a live session (as after an SSO
    // login or once the short-lived cookie lapsed on an idle tab) but the
    // `access_token` cookie /authorize authenticates with is gone. The flow
    // must bounce through /dash0/login, which refreshes the cookie and
    // resumes the authorize returnTo — instead of dead-ending on the
    // dashboard.
    //
    // Dedicated login rather than the shared worker session: this bounce
    // needs a working `/auth/refresh`, which requires the session's
    // refresh-token row to still exist. The shared session can be evicted
    // by unrelated login churn elsewhere in the suite well before this file
    // runs — see spec 2026-08-05-02.
    await freshLogin(page);

    const clientId = await registerClient(page);
    const pkce = newPkcePair();

    await page.context().clearCookies();

    await page.goto(authorizeUrl(clientId, pkce, "state-bounce"));

    const { code, state } = await approveConsent(page);
    expect(code).toBeTruthy();
    expect(state).toBe("state-bounce");

    await exchangeAndCallMCP(page, clientId, code, pkce.verifier);
  });

  test("logged-out browser goes through the password login and resumes consent", async ({
    page,
  }) => {
    const clientId = await registerClient(page);
    const pkce = newPkcePair();

    await page.goto(authorizeUrl(clientId, pkce, "state-login"));

    // Bounced to the login page with returnTo preserved.
    await expect(page.getByTestId("login-title")).toBeVisible({
      timeout: 15000,
    });
    expect(page.url()).toContain("returnTo=");

    await page.getByTestId("login-email").fill("test@test.com");
    await page.getByTestId("login-password").fill("test");
    await page.getByTestId("login-submit").click();

    // The test user belongs to several orgs → the org picker shows. Picking
    // one goes through switchOrg (which sets the session cookie) and must
    // then resume the OAuth returnTo like every other login-success path.
    await page.getByTestId("org-picker-test").click();

    const { code, state } = await approveConsent(page);
    expect(code).toBeTruthy();
    expect(state).toBe("state-login");

    await exchangeAndCallMCP(page, clientId, code, pkce.verifier);
  });

  test("deny returns access_denied to the client, no code", async ({
    authenticatedPage: page,
  }) => {
    const clientId = await registerClient(page);
    const pkce = newPkcePair();

    await page.goto(authorizeUrl(clientId, pkce, "state-deny"));

    await expect(
      page.getByRole("heading", { name: "Authorize MCP access" }),
    ).toBeVisible({ timeout: 15000 });
    await page.getByRole("button", { name: "Deny" }).click();
    await page.waitForURL(`${CALLBACK_ORIGIN}/**`, { timeout: 15000 });

    const url = new URL(page.url());
    expect(url.searchParams.get("error")).toBe("access_denied");
    expect(url.searchParams.get("code")).toBeNull();
    expect(url.searchParams.get("state")).toBe("state-deny");
  });
});
