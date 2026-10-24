import http from "node:http";
import type { AddressInfo } from "node:net";

import { test, expect, API_BASE, uniqueStamp, type Page } from "./fixtures";

// Spec 2026-10-18-02: an AI agent (A2A) integration is configured from the
// dashboard, and its Test button resolves the Agent Card from the unsaved form
// values, showing the agent name, its skills and the protocol version.
//
// The card is served by a tiny HTTP server this spec starts on loopback. The
// test-mode server allows private targets, so it may fetch it.

let cardServer: http.Server;
let cardBase = "";
let lastCardAuth: string | undefined;

test.beforeAll(async () => {
  cardServer = http.createServer((req, res) => {
    if (req.url !== "/.well-known/agent-card.json") {
      res.writeHead(404).end();
      return;
    }
    lastCardAuth = req.headers.authorization;
    res.writeHead(200, { "Content-Type": "application/json" });
    res.end(
      JSON.stringify({
        name: "acme investigator",
        description: "Finds the root cause of incidents",
        version: "1.0.0",
        supportedInterfaces: [
          { url: `${cardBase}/rpc`, protocolBinding: "JSONRPC", protocolVersion: "1.0" },
        ],
        capabilities: {},
        defaultInputModes: ["text/plain"],
        defaultOutputModes: ["text/plain"],
        skills: [
          { id: "investigate", name: "Investigate", description: "Reads checks and results", tags: [] },
        ],
      }),
    );
  });
  await new Promise<void>((resolve) => cardServer.listen(0, "127.0.0.1", resolve));
  cardBase = `http://127.0.0.1:${(cardServer.address() as AddressInfo).port}`;
});

test.afterAll(async () => {
  await new Promise<void>((resolve) => cardServer.close(() => resolve()));
});

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  const body = await resp.json();
  return body.accessToken;
}

test.describe("AI agent (A2A) integration", () => {
  test("Test resolves the card before saving, and the token is stored as a secret", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    await page.goto("orgs/test/integrations/new");
    await page.getByTestId("pick-a2a").click();

    await page.locator("#ch-name").fill(`E2E AI agent ${uniqueStamp()}`);
    await page.getByTestId("a2a-card-url").fill(cardBase);
    await page.getByTestId("a2a-auth").click();
    await page.getByTestId("a2a-auth-bearer").click();
    await page.getByTestId("a2a-token").fill("e2e-agent-token");
    await page.getByTestId("a2a-instructions").fill("Investigate, do not restart anything");

    await page.getByTestId("a2a-test").click();
    await expect(page.getByTestId("a2a-agent-name")).toHaveText("acme investigator");
    await expect(page.getByTestId("a2a-agent-version")).toContainText("1.0");
    await expect(page.getByTestId("a2a-agent-skill")).toHaveCount(1);
    await expect(page.getByTestId("a2a-agent-skill")).toContainText("Investigate");
    expect(lastCardAuth).toBe("Bearer e2e-agent-token");

    await page.getByRole("button", { name: /create integration/i }).click();
    await page.waitForURL((url) =>
      /\/orgs\/test\/integrations\/[0-9a-f-]{36}$/.test(url.pathname),
    );
    const uid = new URL(page.url()).pathname.split("/").pop() as string;

    const integration = await page.request
      .get(`${API_BASE}/api/v1/orgs/test/integrations/${uid}`, {
        headers: { Authorization: `Bearer ${token}` },
      })
      .then((r) => r.json());

    expect(integration.type).toBe("a2a");
    expect(integration.settings.agent_card_url).toBe(cardBase);
    expect(integration.settings.instructions).toBe("Investigate, do not restart anything");
    expect(integration.settings).not.toHaveProperty("token");
    expect(integration.settingsPrivateKeys).toContain("token");

    // On the edit page the token is not echoed back; Test still works with
    // the stored one.
    lastCardAuth = undefined;
    await page.getByTestId("a2a-test").click();
    await expect(page.getByTestId("a2a-agent-name")).toHaveText("acme investigator");
    expect(lastCardAuth).toBe("Bearer e2e-agent-token");

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/integrations/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  });

  test("a card that cannot be fetched is reported by Test", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/integrations/new?type=a2a");
    await page.getByTestId("a2a-card-url").fill(`${cardBase}/missing/card.json`);
    await page.getByTestId("a2a-test").click();

    await expect(page.getByTestId("a2a-test-error")).toContainText("404");
    await expect(page.getByTestId("a2a-test-result")).toHaveCount(0);
  });
});
