import { test, expect, API_BASE, type Page } from "./fixtures";
import { expandSection } from "./section-helpers";

// Coverage for spec 2026-07-16-03: an HTTP check's basic-auth credential is
// stored as ONE reserved, encrypted `basicAuth` key (both halves protected),
// and editing anything else on the check must not silently destroy it — nor
// the check's secret headers, which the form used to wipe on every save by
// force-sending `secretHeaders: {}` (they never come back on GET, so the form
// always thought they were empty).
//
// The two halves fold into `basicAuth` on every deployment; whether that key is
// then encrypted depends on SP_ENCRYPTION_MASTER_KEY, which the E2E server (like
// CI's) does NOT set — it runs the documented plaintext fallback. So the
// assertions are mode-aware:
//
//   - encrypted (master key set): the credential never comes back, so "it
//     survived" is asserted through `configPrivateKeys` + the form's encrypted
//     placeholder;
//   - plaintext fallback (CI): `config.basicAuth` is readable, so the exact
//     credential is asserted verbatim across the edit.
//
// Either way the invariant is the same: the credential has ONE home, and an
// unrelated edit must not destroy it or the check's secret headers.

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

test.describe("HTTP check basic auth", () => {
  test("keeps the encrypted credential and secret headers when only the URL is edited", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    // ── Create, through the real form ──
    await page.goto("orgs/test/checks/new?checkType=http");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    const checkName = `E2E Basic Auth ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page
      .getByTestId("check-url-input")
      .fill("https://example.com/basic-auth-before");

    await expandSection(page, "section-authentication-trigger");
    await page.getByTestId("check-username-input").fill("probe");
    await page.getByTestId("check-password-input").fill("hunter2");
    await page.getByTestId("add-secret-header-button").click();
    await page.getByTestId("secret-header-key-0").fill("X-Api-Key");
    await page.getByTestId("secret-header-value-0").fill("sk-e2e-secret");

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    // Both halves fold into one reserved key — the split `username`/`password`
    // pair is gone from storage on every deployment.
    const created = await getCheck(page, token, uid);
    expect(created.config).not.toHaveProperty("username");
    expect(created.config).not.toHaveProperty("password");

    const encrypted: boolean =
      created.configPrivateKeys?.includes("basicAuth") ?? false;
    if (encrypted) {
      // Master key set: nothing leaks into the public, queryable config.
      expect(created.configPrivateKeys).toContain("secretHeaders");
      expect(created.config).not.toHaveProperty("basicAuth");
      expect(created.config).not.toHaveProperty("secretHeaders");
    } else {
      // Plaintext fallback: the credential is readable, but it still has a
      // single home.
      expect(created.config.basicAuth).toBe("probe:hunter2");
    }

    // ── Edit only the URL ──
    await page.goto(`orgs/test/checks/${uid}/edit`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-url-input")).toBeVisible();

    // The stored credential is advertised, not echoed: the inputs stay empty
    // (the folded `basicAuth` is not split back into them) and, when it is
    // encrypted, an explicit placeholder says so.
    await expandSection(page, "section-authentication-trigger");
    await expect(page.getByTestId("check-username-input")).toHaveValue("");
    await expect(page.getByTestId("check-password-input")).toHaveValue("");
    if (encrypted) {
      await expect(page.getByTestId("basic-auth-encrypted")).toBeVisible();
    }

    await page
      .getByTestId("check-url-input")
      .fill("https://example.com/basic-auth-after");
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f-]{36}$/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    // ── The credential and the secret headers survived the unrelated edit ──
    const edited = await getCheck(page, token, uid);
    expect(edited.config.url).toBe("https://example.com/basic-auth-after");
    expect(edited.config).not.toHaveProperty("username");
    expect(edited.config).not.toHaveProperty("password");

    if (encrypted) {
      expect(
        edited.configPrivateKeys,
        "editing the URL destroyed the basic-auth credential",
      ).toContain("basicAuth");
      expect(
        edited.configPrivateKeys,
        "editing the URL wiped the secret headers",
      ).toContain("secretHeaders");
    } else {
      expect(
        edited.config.basicAuth,
        "editing the URL destroyed the basic-auth credential",
      ).toBe("probe:hunter2");
      expect(
        edited.config.secretHeaders,
        "editing the URL wiped the secret headers",
      ).toEqual({ "X-Api-Key": "sk-e2e-secret" });
    }

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  });
});
