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
        expected_fingerprint:
          "SHA256:47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU=",
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

    // postgres does too — the flagship bastion use case. Support now extends to
    // every TCP-based type (databases, brokers, mail, …), and the form reads
    // that from the API's `supportsTunnel` rather than from a hard-coded list.
    await page.goto("orgs/test/checks/new?checkType=postgresql");
    await page.waitForLoadState("networkidle");
    await expandSection(page, "section-advanced-trigger");
    await expect(page.getByTestId("check-tunnel-section")).toBeVisible();

    // dns does not — it is DNS/UDP-based, and an SSH direct-tcpip forward is TCP
    // only. The form reads that from the API, not from a hard-coded list.
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

// Coverage for spec 2026-07-18-01: UX gaps in the declare-and-use loop above.
test.describe("SSH tunnel UX gaps", () => {
  test("empty-state link deep-links to a preselected SSH check form", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Force TunnelSelect's empty state regardless of how many SSH checks
    // already exist in the shared "test" org (other tests in this file, or a
    // long-lived dev DB, may have created bastions): stub the exact query the
    // selector's candidate list depends on (`useChecks(org, { type: "ssh" })`)
    // to return zero results, deterministically.
    await page.route(/\/api\/v1\/orgs\/test\/checks\?[^#]*type=ssh/, (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ data: [] }),
      }),
    );

    await page.goto("orgs/test/checks/new?checkType=http");
    await page.waitForLoadState("networkidle");
    await expandSection(page, "section-advanced-trigger");

    const emptyStateLink = page.getByTestId("tunnel-empty-create-link");
    await expect(emptyStateLink).toBeVisible();
    await expect(emptyStateLink).toHaveText(
      "Create an SSH check for your bastion",
    );

    await emptyStateLink.click();
    await page.waitForURL(/checkType=ssh/);
    await page.waitForLoadState("networkidle");

    // The new-check form landed with SSH preselected — no picking a type by
    // hand, and no half-filled http/tcp form left behind.
    await expect(page.getByTestId("check-type-select")).toContainText("SSH");
  });

  test("unverified bastion shows a plain-language disabled reason", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    // An SSH check with no expected_fingerprint: legal to create, but the
    // server refuses to tunnel through it (unverified host key = silent
    // MITM risk), so the selector must list it disabled with a reason that
    // names the field a human fills in — not the raw config key.
    const unverifiedName = `E2E Unverified Bastion ${Date.now()}`;
    const resp = await page.request.post(
      `${API_BASE}/api/v1/orgs/test/checks`,
      {
        headers: { Authorization: `Bearer ${token}` },
        data: {
          name: unverifiedName,
          slug: unverifiedName.toLowerCase().replace(/[^a-z0-9]+/g, "-"),
          type: "ssh",
          config: { host: "unverified.example.com" },
        },
      },
    );
    expect(resp.status()).toBe(201);

    await page.goto("orgs/test/checks/new?checkType=http");
    await page.waitForLoadState("networkidle");
    await expandSection(page, "section-advanced-trigger");
    await page.getByTestId("check-tunnel-select").click();

    const option = page.getByRole("option", {
      name: new RegExp(`${unverifiedName}.*needs a host key fingerprint`),
    });
    await expect(option).toBeVisible();
    await expect(option).toHaveAttribute("aria-disabled", "true");
    await expect(option).not.toContainText("expected_fingerprint");
  });

  test("disables an SSH check that is out of the check's private region", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    // A private region the bastion is NOT allocated to. Unique per run so it
    // never collides with residue in the shared "test" org.
    const regionSlug = `tun-${Date.now().toString(36)}`.slice(0, 20);
    const regionResp = await page.request.post(
      `${API_BASE}/api/v1/orgs/test/private-regions`,
      {
        headers: { Authorization: `Bearer ${token}` },
        data: { slug: regionSlug, name: `Tunnel Region ${regionSlug}` },
      },
    );
    expect(regionResp.ok()).toBeTruthy();

    // A cloud-region bastion (default regions) — verified, so the only reason it
    // can be rejected is the region rule.
    const bastionName = `E2E Region Bastion ${Date.now()}`;
    await createBastion(page, token, bastionName);

    await page.goto("orgs/test/checks/new?checkType=http");
    await page.waitForLoadState("networkidle");

    // Put the check in the private region the bastion doesn't cover.
    await page.getByTestId(`region-option-@${regionSlug}`).click();

    await expandSection(page, "section-advanced-trigger");
    await page.getByTestId("check-tunnel-select").click();

    // The bastion is offered disabled, with the region reason named in the
    // friendly `@slug` form the server uses.
    const option = page.getByRole("option", {
      name: new RegExp(`${bastionName}.*not in region @${regionSlug}`),
    });
    await expect(option).toBeVisible();
    await expect(option).toHaveAttribute("aria-disabled", "true");

    // Cleanup: nothing is allocated to the region (the form was never saved).
    await page.request.delete(
      `${API_BASE}/api/v1/orgs/test/private-regions/${regionSlug}`,
      { headers: { Authorization: `Bearer ${token}` } },
    );
  });

  test("region edit that leaves the tunnel's coverage is rejected inline", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    const regionSlug = `tune-${Date.now().toString(36)}`.slice(0, 20);
    const regionResp = await page.request.post(
      `${API_BASE}/api/v1/orgs/test/private-regions`,
      {
        headers: { Authorization: `Bearer ${token}` },
        data: { slug: regionSlug, name: `Edit Region ${regionSlug}` },
      },
    );
    expect(regionResp.ok()).toBeTruthy();

    // A verified cloud bastion + a dependent tunneling through it (both in cloud
    // regions, so the tunnel is legal to start with).
    const bastionName = `E2E Edit Bastion ${Date.now()}`;
    const bastion = await createBastion(page, token, bastionName);

    const checkName = `E2E Edit Tunneled ${Date.now()}`;
    const createResp = await page.request.post(
      `${API_BASE}/api/v1/orgs/test/checks`,
      {
        headers: { Authorization: `Bearer ${token}` },
        data: {
          name: checkName,
          slug: checkName.toLowerCase().replace(/[^a-z0-9]+/g, "-"),
          type: "http",
          config: {
            url: "http://internal-edit.private/health",
            tunnelCheckUid: bastion.uid,
          },
        },
      },
    );
    expect(createResp.status()).toBe(201);
    const dependent = await createResp.json();

    // Edit the dependent and add the private region the bastion doesn't cover.
    await page.goto(`orgs/test/checks/${dependent.uid}/edit`);
    await page.waitForLoadState("networkidle");
    await page.getByTestId(`region-option-@${regionSlug}`).click();

    // The live validation rejects it, inline on BOTH the regions field and the
    // tunnel field (a region change can now be blocked by a tunnel dependency).
    const regionError = page.getByTestId("region-tunnel-error");
    await expect(regionError).toBeVisible();
    await expect(regionError).toContainText(`@${regionSlug}`);

    // Cleanup: the rejected edit was never saved, so nothing is in the region.
    await page.request.delete(
      `${API_BASE}/api/v1/orgs/test/private-regions/${regionSlug}`,
      { headers: { Authorization: `Bearer ${token}` } },
    );
  });

  test("checks list shows a tunnel indicator naming the bastion", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    const bastionName = `E2E List Bastion ${Date.now()}`;
    const bastion = await createBastion(page, token, bastionName);

    const checkName = `E2E List Tunneled ${Date.now()}`;
    const createResp = await page.request.post(
      `${API_BASE}/api/v1/orgs/test/checks`,
      {
        headers: { Authorization: `Bearer ${token}` },
        data: {
          name: checkName,
          slug: checkName.toLowerCase().replace(/[^a-z0-9]+/g, "-"),
          type: "http",
          config: {
            url: "http://internal-list.private/health",
            tunnelCheckUid: bastion.uid,
          },
        },
      },
    );
    expect(createResp.status()).toBe(201);

    await page.goto("orgs/test/checks");
    await page.waitForLoadState("networkidle");

    // Narrow the paginated list to just this check.
    await page.getByPlaceholder("Search checks...").fill(checkName);
    const row = page.getByRole("row").filter({ hasText: checkName });
    await expect(row).toBeVisible();

    const indicator = row.getByTestId("check-tunnel-indicator");
    await expect(indicator).toBeVisible();

    await indicator.hover();
    await expect(page.getByText(`via ${bastionName}`)).toBeVisible();
  });
});
