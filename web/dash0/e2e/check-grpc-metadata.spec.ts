import { test, expect, API_BASE, type Page } from "./fixtures";
import { expandSection } from "./section-helpers";

// Coverage for spec 2026-09-01-03: the gRPC form used to expose only
// host/port/serviceName/tls, so "TLS without certificate verification" and
// request metadata were API-only. This drives the real form for all of it and
// asserts the round-trip, including that the secret half is split out of the
// public config and survives an unrelated edit — the exact failure mode the
// HTTP form's `headersDirty` guard exists for.

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

test.describe("gRPC check form", () => {
  test("round-trips TLS skip-verify, metadata, secret metadata and the timeout", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    await page.goto("orgs/test/checks/new?checkType=grpc");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    const checkName = `E2E gRPC ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page.locator("#host").fill("grpc.example.com");
    await page.locator("#port").fill("443");
    await page.locator("#serviceName").fill("my.service.v1");
    // TLS is what reveals the skip-verify toggle in the Advanced section.
    await page.getByTestId("check-grpc-tls-checkbox").click();

    await expandSection(page, "section-advanced-trigger");
    await page.getByTestId("check-grpc-tls-skip-verify-switch").click();
    await expect(
      page.getByTestId("check-grpc-tls-skip-verify-warning"),
    ).toBeVisible();
    // The per-check timeout is the shared, protocol-agnostic input — this
    // asserts it really does reach a gRPC check's config.
    await page.getByTestId("check-timeout-input").fill("7");

    await page.getByTestId("grpc-metadata-add-button").click();
    await page.getByTestId("grpc-metadata-key-0").fill("x-tenant");
    await page.getByTestId("grpc-metadata-value-0").fill("acme");

    await expandSection(page, "section-authentication-trigger");
    await page.getByTestId("grpc-secret-metadata-add-button").click();
    await page.getByTestId("grpc-secret-metadata-key-0").fill("authorization");
    await page
      .getByTestId("grpc-secret-metadata-value-0")
      .fill("Bearer e2e-grpc-secret");

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    const created = await getCheck(page, token, uid);
    expect(created.config.host).toBe("grpc.example.com");
    expect(created.config.port).toBe(443);
    expect(created.config.serviceName).toBe("my.service.v1");
    expect(created.config.tls).toBe(true);
    expect(created.config.tlsSkipVerify).toBe(true);
    expect(created.config.timeout).toBe("7s");
    expect(created.config.metadata).toEqual({ "x-tenant": "acme" });
    // The secret half must never be in the public, queryable config — in ANY
    // storage-encryption mode, including the plaintext fallback CI runs.
    expect(created.config).not.toHaveProperty("secretMetadata");
    expect(created.configPrivateKeys).toContain("secretMetadata");

    // ── Edit only the service name ──
    await page.goto(`orgs/test/checks/${uid}/edit`);
    await page.waitForLoadState("networkidle");
    await expect(page.locator("#serviceName")).toBeVisible();

    // The stored secret is advertised, not echoed.
    await expandSection(page, "section-authentication-trigger");
    await expect(
      page.getByTestId("grpc-secret-metadata-encrypted"),
    ).toBeVisible();

    await page.locator("#serviceName").fill("my.service.v2");
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f-]{36}$/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    const edited = await getCheck(page, token, uid);
    expect(edited.config.serviceName).toBe("my.service.v2");
    expect(edited.config.tlsSkipVerify).toBe(true);
    expect(edited.config.metadata).toEqual({ "x-tenant": "acme" });
    expect(
      edited.configPrivateKeys,
      "editing the service name wiped the secret metadata",
    ).toContain("secretMetadata");

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  });
});
