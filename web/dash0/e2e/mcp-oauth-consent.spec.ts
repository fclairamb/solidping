import { expect, type Page } from "@playwright/test";
import { createHash, randomBytes } from "node:crypto";
import { createServer, type Server } from "node:http";
import { test, API_BASE, DASH_BASE, freshLogin, uniqueStamp } from "./fixtures";

/**
 * MCP OAuth 2.1 consent flow (spec 2026-06-20-03) — the browser legs an MCP
 * client (e.g. claude.ai) drives when the user clicks "Connect":
 *
 *   GET /api/v1/oauth/authorize  →  (no session cookie) → /d/login
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

// One port per Playwright worker: CI runs two workers per shard, and this
// file's tests run on both (fullyParallel), so a fixed port collides.
const CALLBACK_PORT = 39777 + Number(process.env.TEST_PARALLEL_INDEX ?? "0");
const CALLBACK_ORIGIN = `http://127.0.0.1:${CALLBACK_PORT}`;
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
    callbackServer.listen(CALLBACK_PORT, "127.0.0.1", resolve),
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
    // must bounce through /d/login, which refreshes the cookie and
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

/**
 * Reads the one-shot confirmation token out of the queued registration email
 * (only its hash is stored), as confirm-registration-no-org.spec.ts does.
 */
async function registrationConfirmToken(page: Page, email: string): Promise<string> {
  const res = await page.request.get(`${API_BASE}/api/v1/test/jobs?type=email`);
  expect(res.status()).toBe(200);
  const { data: jobs } = (await res.json()) as {
    data: Array<{
      config: { template?: string; to?: string[]; templateData?: { ConfirmURL?: string } };
    }>;
  };
  const job = jobs.find(
    (j) => j.config.template === "registration.html" && j.config.to?.includes(email),
  );
  const confirmURL = job?.config.templateData?.ConfirmURL ?? "";
  expect(confirmURL, "the registration email must have been queued").toBeTruthy();
  return confirmURL.split("/").pop() ?? "";
}

/** Exchanges the code, then creates a check through MCP with the token. */
async function exchangeAndCreateCheck(
  page: Page,
  clientId: string,
  code: string,
  verifier: string,
  checkSlug: string,
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
  expect(tok.scope).toBe("mcp");

  const call = (sessionId: string, body: object) =>
    page.request.post(`${API_BASE}/api/v1/mcp`, {
      headers: {
        Authorization: `Bearer ${tok.access_token}`,
        "Content-Type": "application/json",
        ...(sessionId ? { "Mcp-Session-Id": sessionId } : {}),
      },
      data: body,
    });

  const init = await call("", {
    jsonrpc: "2.0",
    id: 1,
    method: "initialize",
    params: {
      protocolVersion: "2025-03-26",
      clientInfo: { name: "e2e-consent", version: "1.0" },
    },
  });
  expect(init.status()).toBe(200);
  const sessionId = init.headers()["mcp-session-id"] ?? "";
  expect(sessionId).toBeTruthy();

  const created = await call(sessionId, {
    jsonrpc: "2.0",
    id: 2,
    method: "tools/call",
    params: {
      name: "create_check",
      arguments: {
        name: "E2E new user check",
        slug: checkSlug,
        type: "http",
        config: { url: "https://example.com" },
      },
    },
  });
  expect(created.status()).toBe(200);
  const rpc = await created.json();
  expect(rpc.result, JSON.stringify(rpc)).toBeTruthy();
  expect(rpc.result.isError ?? false, JSON.stringify(rpc.result)).toBe(false);
}

/** Creates the first org from /no-org with the suggested name. */
async function createFirstOrg(page: Page): Promise<void> {
  await expect(page.getByTestId("create-org-submit")).toBeVisible({ timeout: 15000 });
  await page.getByTestId("create-org-submit").click();
}

// fclairamb/solidping-business#35: a person with no SolidPing account (or an
// account with no org) must end up back in the MCP client with a working
// token by only using the UI.
test.describe("MCP OAuth consent flow for a user without an org", () => {
  test("brand-new password user signs up, confirms, creates an org and approves", async ({
    page,
    browser,
  }) => {
    const stamp = uniqueStamp();
    const email = `mcp-new-${stamp}@acme.com`;
    const clientId = await registerClient(page);
    const pkce = newPkcePair();

    await page.goto(authorizeUrl(clientId, pkce, "state-new-user"));

    // Logged out: the login page, then "Create an account" keeps returnTo.
    await expect(page.getByTestId("login-title")).toBeVisible({ timeout: 15000 });
    await page.getByTestId("login-create-account-link").click();
    await expect(page.getByTestId("register-form")).toBeVisible();
    expect(new URL(page.url()).searchParams.get("returnTo")).toContain(
      "/api/v1/oauth/authorize?",
    );

    await page.getByTestId("register-email").fill(email);
    await page.getByTestId("register-password").fill("Strong-Pass-123!");
    await page.getByTestId("register-submit").click();
    await expect(page.getByTestId("register-confirmation-message")).toContainText(email, {
      timeout: 10000,
    });

    // The emailed link opens elsewhere: a fresh browser context, so nothing
    // but the server can carry the pending authorization.
    const token = await registrationConfirmToken(page, email);
    const mailContext = await browser.newContext();
    try {
      const mail = await mailContext.newPage();
      await mail.goto(`${API_BASE}${DASH_BASE}/confirm-registration/${token}`);
      await mail.getByTestId("confirm-registration-button").click();

      await mail.waitForURL(/\/no-org\?.*returnTo=/, { timeout: 15000 });
      await createFirstOrg(mail);

      const { code, state } = await approveConsent(mail);
      expect(code).toBeTruthy();
      expect(state).toBe("state-new-user");

      await exchangeAndCreateCheck(mail, clientId, code, pkce.verifier, `mcp-new-${stamp}`);
    } finally {
      await mailContext.close();
    }
  });

  test("signed-in user without an org is sent to create one, then to consent", async ({
    page,
  }) => {
    const stamp = uniqueStamp();
    const email = `mcp-orgless-${stamp}@acme.com`;
    const password = "Strong-Pass-123!";
    const created = await page.request.post(`${API_BASE}/api/v1/test/users`, {
      data: { email, password, name: "Org-less user" },
    });
    expect(created.status()).toBe(201);

    // A plain sign-in first: the account has no org, so it lands on /no-org.
    await page.goto("orgs/test/login");
    await page.getByTestId("login-email").fill(email);
    await page.getByTestId("login-password").fill(password);
    await page.getByTestId("login-submit").click();
    await page.waitForURL(/\/no-org/, { timeout: 15000 });

    // Then the MCP client opens authorize with that org-less session: the
    // server sends it to org creation, never to "/orgs//oauth/consent".
    const clientId = await registerClient(page);
    const pkce = newPkcePair();
    await page.goto(authorizeUrl(clientId, pkce, "state-orgless"));
    await page.waitForURL(/\/no-org\?.*returnTo=/, { timeout: 15000 });
    expect(page.url()).not.toContain("/orgs//");

    await createFirstOrg(page);

    const { code, state } = await approveConsent(page);
    expect(code).toBeTruthy();
    expect(state).toBe("state-orgless");

    await exchangeAndCreateCheck(page, clientId, code, pkce.verifier, `mcp-orgless-${stamp}`);
  });
});
