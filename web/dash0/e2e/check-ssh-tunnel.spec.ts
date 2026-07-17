import { test, expect, API_BASE, type Page } from "./fixtures";
import { expandSection } from "./section-helpers";

// Coverage for spec 2026-07-16-04: a tunnel-capable check can be pointed at an
// SSH check to dial its probe through that bastion.
//
// The three things worth proving end-to-end, none of which a unit test reaches:
//   - the selector is gated on server-declared capability metadata
//     (`supportsTunnel`), so it shows on http/tcp and NOT on, say, dns;
//   - creating a tunneled check through the real form actually stores
//     `tunnelCheckUid` on the check;
//   - the bastion's detail page lists its dependents — which is what makes the
//     delete-guard 409 comprehensible instead of a mystery.

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

// createBastion creates an SSH check that is legal as a tunnel: it has an
// expected_fingerprint, without which the server refuses to tunnel through it.
async function createBastion(
  page: Page,
  token: string,
  name: string,
): Promise<{ uid: string; slug: string }> {
  const resp = await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
    headers: { Authorization: `Bearer ${token}` },
    data: {
      name,
      slug: name.toLowerCase().replace(/[^a-z0-9]+/g, "-"),
      type: "ssh",
      config: {
        host: "bastion.example.com",
        expected_fingerprint: "SHA256:47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU=",
        username: "probe",
        password: "s3cret",
      },
    },
  });
  expect(resp.status()).toBe(201);
  const body = await resp.json();
  return { uid: body.uid, slug: body.slug };
}

async function getCheck(page: Page, token: string, uid: string) {
  const resp = await page.request.get(
    `${API_BASE}/api/v1/orgs/test/checks/${uid}`,
    { headers: { Authorization: `Bearer ${token}` } },
  );
  expect(resp.status()).toBe(200);
  return await resp.json();
}

test.describe("SSH tunnel selector", () => {
  test("appears only on tunnel-capable check types", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // http supports tunneling.
    await page.goto("orgs/test/checks/new?checkType=http");
    await page.waitForLoadState("networkidle");
    await expandSection(page, "section-advanced-trigger");
    await expect(page.getByTestId("check-tunnel-section")).toBeVisible();

    // tcp does too.
    await page.goto("orgs/test/checks/new?checkType=tcp");
    await page.waitForLoadState("networkidle");
    await expandSection(page, "section-advanced-trigger");
    await expect(page.getByTestId("check-tunnel-section")).toBeVisible();

    // dns does not — v1 proves the seam on http + tcp only, and the form reads
    // that from the API rather than from a hard-coded list.
    await page.goto("orgs/test/checks/new?checkType=dns");
    await page.waitForLoadState("networkidle");
    await expandSection(page, "section-advanced-trigger");
    await expect(page.getByTestId("check-tunnel-section")).toHaveCount(0);
  });

  test("creates a tunneled check and lists it on the bastion", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    const bastionName = `E2E Bastion ${Date.now()}`;
    const bastion = await createBastion(page, token, bastionName);

    // ── Create a tunneled http check through the real form ──
    await page.goto("orgs/test/checks/new?checkType=http");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    const checkName = `E2E Tunneled ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page
      .getByTestId("check-url-input")
      .fill("http://internal-api.private/health");

    await expandSection(page, "section-advanced-trigger");
    await page.getByTestId("check-tunnel-select").click();
    await page.getByRole("option", { name: bastionName, exact: true }).click();

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    // The reference is stored on the public side of the config — it is not a
    // secret, and the delete guard queries it as JSON.
    const created = await getCheck(page, token, uid);
    expect(created.config.tunnelCheckUid).toBe(bastion.uid);

    // ── The dependent's detail page links to its bastion ──
    await expect(page.getByTestId("check-tunnel-via")).toBeVisible();

    // ── The bastion's detail page lists its dependents ──
    await page.goto(`orgs/test/checks/${bastion.uid}`);
    await page.waitForLoadState("networkidle");
    const dependents = page.getByTestId("check-tunnel-dependents");
    await expect(dependents).toBeVisible();
    await expect(dependents).toContainText(checkName);

    // ── Editing the check keeps the tunnel selected ──
    await page.goto(`orgs/test/checks/${uid}/edit`);
    await page.waitForLoadState("networkidle");
    await expandSection(page, "section-advanced-trigger");
    await expect(page.getByTestId("check-tunnel-select")).toContainText(
      "E2E Bastion",
    );
  });
});
