import { test, expect, API_BASE, getAuthToken, uniqueStamp } from "./fixtures";
import type { Page, Route } from "@playwright/test";

// Coverage for spec 2026-10-03-07 (AI-authored js checks). The LLM side is
// stubbed: the public config says AI is on, and the contract and generate
// endpoints answer canned replies. Saving goes through the real create path,
// so the check, its ai block and its first version are real.

const SCRIPT = `var r = http.get(env.BASE_URL + "/health");
if (r.error) { return { status: "down", output: { failure: "assertion", error: r.error } }; }
return { status: r.statusCode === 200 ? "up" : "down", output: { failure: "assertion" } };`;

async function enableAI(page: Page) {
  await page.route("**/api/v1/config", async (route: Route) => {
    const response = await route.fetch();
    const body = await response.json();
    await route.fulfill({ response, json: { ...body, ai: { enabled: true } } });
  });
}

test.describe("AI-authored checks", () => {
  test("describe it: prompt, confirm the contract, generate, save", async ({ authenticatedPage }) => {
    test.setTimeout(90_000);
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const headers = { Authorization: `Bearer ${token}` };
    const name = `AI check ${uniqueStamp()}`;

    await enableAI(page);

    let contractPrompt = "";
    await page.route("**/api/v1/orgs/test/checks/ai/contract", async (route: Route) => {
      contractPrompt = (route.request().postDataJSON() as { prompt: string }).prompt;
      await route.fulfill({
        json: { contract: ["GET /health answers 200"], model: "stub-model" },
      });
    });

    let generateBody: { contract: string[]; secrets?: Record<string, string> } | undefined;
    await page.route("**/api/v1/orgs/test/checks/ai/generate", async (route: Route) => {
      generateBody = route.request().postDataJSON();
      await route.fulfill({
        json: {
          script: SCRIPT,
          config: {
            script: SCRIPT,
            env: { BASE_URL: "https://acme.com" },
            ai: {
              prompt: "acme answers on /health",
              contract: generateBody?.contract ?? [],
              model: "stub-model",
              generated_at: "2026-10-03T10:00:00Z",
              repair: "propose",
            },
          },
          secretNames: [],
          lastRun: { status: "up", durationMs: 120, output: { statusCode: 200 } },
          model: "stub-model",
          turns: 2,
        },
      });
    });

    await page.goto("orgs/test/checks");
    await page.getByTestId("describe-check-button").click();
    await page.waitForURL(/\/checks\/describe/);

    await page.getByTestId("ai-prompt-input").fill("acme answers on /health");
    await page.getByTestId("ai-propose-contract").click();

    const contract = page.getByTestId("ai-contract-input");
    await expect(contract).toHaveValue("GET /health answers 200");
    expect(contractPrompt).toBe("acme answers on /health");

    // The user edits the contract before generating.
    await contract.fill("GET /health answers 200\nthe body says ok");
    await page.getByTestId("ai-generate").click();

    await expect(page.getByTestId("ai-generated-script")).toContainText("env.BASE_URL");
    expect(generateBody?.contract).toEqual(["GET /health answers 200", "the body says ok"]);

    await page.getByTestId("ai-name-input").fill(name);
    await page.getByTestId("ai-save").click();
    await page.waitForURL(/\/checks\/[0-9a-f-]{36}/, { timeout: 15000 });

    // The check page shows what the check was written from.
    await expect(page.getByTestId("ai-detail-prompt")).toContainText("acme answers on /health");
    await expect(page.getByTestId("ai-detail-contract")).toContainText("the body says ok");

    const uid = page.url().match(/checks\/([0-9a-f-]{36})/)?.[1] as string;
    const check = await (
      await page.request.get(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, { headers })
    ).json();
    expect(check.type).toBe("js");
    expect(check.config.ai.contract).toEqual(["GET /health answers 200", "the body says ok"]);

    // The first version records that an AI wrote it.
    const versions = await (
      await page.request.get(`${API_BASE}/api/v1/orgs/test/checks/${uid}/versions`, { headers })
    ).json();
    expect(versions.data[0].origin).toBe("ai_generate");

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, { headers });
  });

  test("the Describe it entry is hidden without an AI provider", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    await page.goto("orgs/test/checks");
    await expect(page.getByTestId("new-check-button")).toBeVisible();
    await expect(page.getByTestId("describe-check-button")).toHaveCount(0);
  });

  test("approve a repair proposal", async ({ authenticatedPage }) => {
    test.setTimeout(90_000);
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const headers = { Authorization: `Bearer ${token}` };

    const createResp = await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
      headers,
      data: {
        name: `AI repair ${uniqueStamp()}`,
        type: "js",
        enabled: false,
        config: { script: SCRIPT, env: { BASE_URL: "https://acme.com" } },
      },
    });
    expect(createResp.status()).toBe(201);
    const uid = (await createResp.json()).uid as string;

    // The repair proposal is stubbed: an ai_repair version built on v1.
    let approved = false;
    const base = `**/api/v1/orgs/test/checks/${uid}/versions`;
    const v1 = {
      version: 1, status: "applied", origin: "user", createdAt: "2026-10-03T09:00:00Z",
    };
    const proposal = {
      version: 2,
      origin: "ai_repair",
      baseVersion: 1,
      reason: "AI repair: the script drifted at step parse",
      createdAt: "2026-10-03T10:00:00Z",
    };

    await page.route(base, async (route: Route) => {
      await route.fulfill({
        json: { data: [{ ...proposal, status: approved ? "applied" : "proposed" }, v1] },
      });
    });
    await page.route(`${base}/2/diff`, async (route: Route) => {
      await route.fulfill({
        json: { version: 2, against: 1, changes: [{ field: "config.script", from: "old", to: "new" }] },
      });
    });
    await page.route(`${base}/1/diff`, async (route: Route) => {
      await route.fulfill({ json: { version: 1, against: null, changes: [] } });
    });
    let approveCalls = 0;
    await page.route(`${base}/2/approve`, async (route: Route) => {
      approveCalls++;
      approved = true;
      await route.fulfill({ json: { uid } });
    });

    await page.goto(`orgs/test/checks/${uid}/history`);
    const card = page.getByTestId("check-proposal-2");
    await expect(card).toContainText("the script drifted");
    await page.getByTestId("check-proposal-approve-2").click();

    await expect(page.getByTestId("check-proposal-2")).toHaveCount(0, { timeout: 10000 });
    expect(approveCalls).toBe(1);

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, { headers });
  });
});
