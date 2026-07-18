import { generateKeyPairSync } from "node:crypto";
import { test, expect, API_BASE, type Page } from "./fixtures";

// Coverage for spec 2026-07-18-06: a check's config secret must NEVER be echoed
// back by the API, in any storage-encryption mode — and specifically in the
// plaintext fallback that CI runs (SP_ENCRYPTION_MASTER_KEY is unset), which was
// the leak this spec fixes. Before the fix, an ssh check's private_key came back
// verbatim in GET/LIST when no master key was set.
//
// The invariant proven end-to-end here:
//   - the private key never appears in the GET response, only its key name in
//     configPrivateKeys (which lights up the edit-form placeholder dots);
//   - the edit form shows the placeholder, never the key, and the textarea is
//     empty;
//   - saving the check without re-entering the key preserves it (the dashboard
//     never received it, so it can't echo it back) — the check stays intact.

function generatePEMPrivateKey(): string {
  const { privateKey } = generateKeyPairSync("ed25519", {
    publicKeyEncoding: { type: "spki", format: "pem" },
    privateKeyEncoding: { type: "pkcs8", format: "pem" },
  });
  return privateKey as string;
}

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

async function getCheck(page: Page, token: string, uid: string) {
  const resp = await page.request.get(
    `${API_BASE}/api/v1/orgs/test/checks/${uid}`,
    { headers: { Authorization: `Bearer ${token}` } },
  );
  expect(resp.status()).toBe(200);
  return await resp.json();
}

test.describe("SSH check secret in plaintext mode", () => {
  test("never echoes the private key, shows a placeholder, and preserves it on save", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const privateKey = generatePEMPrivateKey();

    // ── Create through the real form ──
    await page.goto("orgs/test/checks/new?checkType=ssh");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    const checkName = `E2E SSH Secret ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page.getByTestId("check-host-input").fill("ssh.internal.example");
    await page.getByTestId("check-private-key-input").fill(privateKey);

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    // ── The API must advertise the key, never echo it — even in plaintext mode ──
    const created = await getCheck(page, token, uid);
    expect(
      created.config,
      "the private key must never be present in the config the API returns",
    ).not.toHaveProperty("private_key");
    expect(created.configPrivateKeys).toContain("private_key");
    expect(created.config.host).toBe("ssh.internal.example");

    // ── The edit form shows the placeholder, and the textarea is empty ──
    await page.goto(`orgs/test/checks/${uid}/edit`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-private-key-input")).toBeVisible();
    await expect(page.getByTestId("check-private-key-input")).toHaveValue("");
    await expect(page.getByTestId("private-key-encrypted")).toBeVisible();

    // ── Save an unrelated edit without re-entering the key ──
    await page.getByTestId("check-host-input").fill("ssh.internal.example2");
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f-]{36}$/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    // ── The credential survived the unrelated edit, still never echoed ──
    const edited = await getCheck(page, token, uid);
    expect(edited.config.host).toBe("ssh.internal.example2");
    expect(edited.config).not.toHaveProperty("private_key");
    expect(
      edited.configPrivateKeys,
      "saving without re-entering the key destroyed the stored private key",
    ).toContain("private_key");

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  });
});
