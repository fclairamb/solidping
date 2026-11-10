/**
 * Peer re-wrap of sealed secrets (spec 2026-11-10-03).
 *
 * A real agent (the solidping binary in agent mode) holds the old and the new
 * region key of a private location. A check is sealed to the old key with
 * `solidping seal`, the key is rotated by pasting a public key generated
 * offline (`solidping agent region-key generate`), and "Re-seal for the
 * current agents" makes the agent re-seal the check for the new key: the
 * needs-re-seal flag clears without typing the secret again.
 *
 * Needs the binary at the repository root (CI builds it there) or in
 * SOLIDPING_BIN; skipped without it.
 */
import { execFileSync, spawn, type ChildProcess } from "node:child_process";
import { existsSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test, expect, API_BASE, uniqueStamp, type Page } from "./fixtures";

const BINARY = process.env.SOLIDPING_BIN ?? join(import.meta.dirname, "../../../solidping");

interface RegionKey {
  identity: string;
  recipient: string;
  fingerprint: string;
}

/** `solidping agent region-key generate`, parsed. */
function generateRegionKey(): RegionKey {
  const out = execFileSync(BINARY, ["agent", "region-key", "generate"], { encoding: "utf8" });
  const lines = out.split("\n").map((line) => line.trim()).filter(Boolean);
  const recipient = lines.find((line) => line.startsWith("# public key: "))?.slice("# public key: ".length) ?? "";
  const fingerprint = lines.find((line) => line.startsWith("# fingerprint: "))?.slice("# fingerprint: ".length) ?? "";
  const identity = lines.find((line) => line.startsWith("AGE-SECRET-KEY-1")) ?? "";
  expect(recipient).toMatch(/^age1/);
  expect(identity).not.toBe("");
  return { identity, recipient, fingerprint };
}

/** `solidping seal`: the sealedSecrets object for a private region. */
function seal(token: string, region: string, secrets: Record<string, string>, configDir: string): unknown {
  const out = execFileSync(
    BINARY,
    ["seal", "--org", "test", "--server", API_BASE, "--region", region, "--token", token],
    {
      encoding: "utf8",
      input: JSON.stringify(secrets),
      env: { PATH: process.env.PATH ?? "", HOME: configDir, SOLIDPING_CONFIG: join(configDir, "settings.json") },
    },
  );
  return JSON.parse(out);
}

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

test.describe("Sealed secrets: peer re-wrap", () => {
  test.skip(!existsSync(BINARY), `needs the solidping binary (${BINARY})`);

  let agent: ChildProcess | undefined;
  let workDir = "";

  test.afterEach(() => {
    agent?.kill("SIGTERM");
    agent = undefined;
    if (workDir) rmSync(workDir, { recursive: true, force: true });
  });

  test("re-seal from the region page after a key rotation clears the flag", async ({ authenticatedPage: page }) => {
    test.setTimeout(180_000);

    const token = await getAuthToken(page);
    const headers = { Authorization: `Bearer ${token}` };
    const slug = `rw${uniqueStamp().slice(0, 8)}`;
    const region = `@${slug}`;
    workDir = mkdtempSync(join(tmpdir(), "sp-rewrap-"));

    const oldKey = generateRegionKey();
    const newKey = generateRegionKey();

    expect(
      (await page.request.post(`${API_BASE}/api/v1/orgs/test/private-regions`, {
        headers,
        data: { slug, name: `Rewrap ${slug}` },
      })).ok(),
    ).toBeTruthy();
    expect(
      (await page.request.put(`${API_BASE}/api/v1/orgs/test/private-regions/${slug}/region-key`, {
        headers,
        data: { recipient: oldKey.recipient },
      })).ok(),
    ).toBeTruthy();

    const minted = await page.request.post(`${API_BASE}/api/v1/orgs/test/agent-enrollment-tokens`, {
      headers,
      data: { regionSlug: slug },
    });
    expect(minted.ok()).toBeTruthy();
    const enrollmentToken = (await minted.json()).token as string;

    // The agent holds both keys, as during a rotation.
    agent = spawn(BINARY, ["serve"], {
      env: {
        PATH: process.env.PATH ?? "",
        HOME: workDir,
        SP_NODE_ROLE: "agent",
        SP_AGENT_SERVER_URL: API_BASE,
        SP_AGENT_ENROLLMENT_TOKEN: enrollmentToken,
        SP_AGENT_KEYS_FILE: join(workDir, "agent-keys.json"),
        SP_AGENT_NAME: `rewrap-${slug}`,
        SP_AGENT_REGION_KEYS: `${oldKey.identity},${newKey.identity}`,
      },
      stdio: "ignore",
    });

    // Online and advertising the region key.
    await expect
      .poll(
        async () => {
          const resp = await page.request.get(`${API_BASE}/api/v1/orgs/test/private-regions`, { headers });
          const rows = ((await resp.json()).data ?? []) as Array<{ slug: string; agentsWithRegionKey?: number }>;
          return rows.find((row) => row.slug === slug)?.agentsWithRegionKey ?? 0;
        },
        { timeout: 60_000, intervals: [1000] },
      )
      .toBeGreaterThan(0);

    const created = await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
      headers,
      data: {
        name: `Rewrap ${slug}`,
        slug: `rewrap-${slug}`,
        type: "http",
        regions: [region],
        config: { url: "https://internal.example.com" },
        sealedSecrets: seal(token, region, { basicAuth: "alice:hunter2" }, workDir),
      },
    });
    expect(created.ok(), await created.text()).toBeTruthy();
    const checkUid = (await created.json()).uid as string;

    // Rotate: paste the public key of the new region key, generated offline.
    await page.goto("orgs/test/organization/private-locations");
    const cell = page.getByTestId(`region-key-${slug}`);
    await expect(cell).toBeVisible();
    await cell.getByTestId("region-key-paste-button").click();
    await page.getByTestId("region-key-paste-input").fill(newKey.recipient);
    await page.getByTestId("region-key-paste-confirm").click();
    await expect(page.getByTestId("region-key-paste-dialog")).toHaveCount(0);
    await expect(page.getByTestId(`region-key-fingerprint-${slug}`)).toHaveText(newKey.fingerprint);

    const flagged = await page.request.get(`${API_BASE}/api/v1/orgs/test/checks/${checkUid}`, { headers });
    expect((await flagged.json()).needsReseal).toBe(true);
    await expect(page.getByTestId(`region-key-stale-${slug}`)).toBeVisible();

    // Re-seal on the agent: it opens the blob with the old key and seals it
    // for the new one.
    await page.getByTestId(`rewrap-button-${slug}`).click();
    await expect(page.getByTestId(`rewrap-done-${slug}`)).toBeVisible({ timeout: 90_000 });

    const cleared = await page.request.get(`${API_BASE}/api/v1/orgs/test/checks/${checkUid}`, { headers });
    const body = await cleared.json();
    expect(body.needsReseal).toBe(false);
    expect(body.secretsEncryption).toBe("browser");
    await expect(page.getByTestId(`region-key-stale-${slug}`)).toHaveCount(0);

    // Leave the shared server as it was: no check, no agent, no location.
    agent?.kill("SIGTERM");
    agent = undefined;
    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${checkUid}`, { headers });
    const agents = await page.request.get(`${API_BASE}/api/v1/orgs/test/agents`, { headers });
    for (const row of ((await agents.json()).data ?? []) as Array<{ uid: string; region: string }>) {
      if (row.region === region) {
        await page.request.delete(`${API_BASE}/api/v1/orgs/test/agents/${row.uid}`, { headers });
      }
    }
    await page.request.delete(`${API_BASE}/api/v1/orgs/test/private-regions/${slug}`, { headers });
  });
});
