import { test, expect, API_BASE } from "./fixtures";
import type { Page } from "@playwright/test";

// Coverage for spec 2026-08-01-03: a status page resource can target a check
// GROUP instead of a single check, and the group is published as ONE component
// that never names its members.
//
// The flow is exercised end to end against the real API: seed a group with two
// member checks, add it through the editor's "Group" tab, then load the PUBLIC
// status page and assert exactly one component is rendered under the group's
// name — with neither member's name anywhere on the page.

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  const body = await resp.json();
  return body.accessToken;
}

async function api(
  page: Page,
  token: string,
  path: string,
  data: Record<string, unknown>,
): Promise<Record<string, unknown>> {
  const resp = await page.request.post(`${API_BASE}${path}`, {
    headers: { Authorization: `Bearer ${token}` },
    data,
  });
  expect(resp.status(), `POST ${path} -> ${await resp.text()}`).toBeLessThan(300);
  return resp.json();
}

test.describe("Status page group resources", () => {
  test("a check group is published as one component that hides its members", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const suffix = Date.now().toString().slice(-9);

    const groupSlug = `e2e-grp-${suffix}`;
    const groupName = `E2E Group ${suffix}`;
    const group = await api(page, token, "/api/v1/orgs/test/check-groups", {
      name: groupName,
      slug: groupSlug,
    });

    // Two members with names that must NEVER surface publicly.
    const memberNames = [`e2e-secret-tcp-${suffix}`, `e2e-secret-tls-${suffix}`];
    for (const name of memberNames) {
      await api(page, token, "/api/v1/orgs/test/checks", {
        type: "http",
        name,
        config: { url: `https://httpbin.org/anything/${name}` },
        period: "00:05:00",
        checkGroupUid: group.uid,
      });
    }

    const pageSlug = `e2e-grp-page-${suffix}`.slice(0, 40);
    const statusPage = await api(page, token, "/api/v1/orgs/test/status-pages", {
      name: `E2E Group Page ${suffix}`,
      slug: pageSlug,
      visibility: "public",
    });
    await api(
      page,
      token,
      `/api/v1/orgs/test/status-pages/${statusPage.uid}/sections`,
      { name: "Core", slug: "core" },
    );

    // --- Add the group through the editor UI ---
    await page.goto(`orgs/test/status-pages/${statusPage.uid}`);
    await page.waitForLoadState("networkidle");

    await page.getByRole("button", { name: "Add Component" }).first().click();
    await page.getByTestId("resource-kind-group").click();
    await page.getByTestId("resource-group-select").click();
    await page.getByTestId(`check-group-picker-option-${groupSlug}`).click();
    await page.getByTestId("resource-add-submit").click();

    // The row lands in the section, labelled with the GROUP's name and flagged
    // as a group (dashboard-side context only).
    const row = page.getByTestId("resource-row-name").filter({ hasText: groupName });
    await expect(row).toHaveCount(1);
    await expect(page.getByTestId("resource-row-group-badge").first()).toBeVisible();

    // --- The public page renders exactly one component, naming only the group ---
    const publicResp = await page.request.get(
      `${API_BASE}/api/v1/status-pages/test/${pageSlug}`,
    );
    expect(publicResp.status()).toBe(200);

    const publicBody = await publicResp.text();
    const publicJSON = JSON.parse(publicBody);
    // CreateStatusPage now seeds a default "Services" section (spec
    // 2026-08-28-16), so the group's own "Core" section is no longer
    // reliably index 0 — find it by slug instead.
    const coreSection = (publicJSON.sections ?? []).find(
      (section: { slug?: string }) => section.slug === "core",
    );
    const resources = coreSection?.resources ?? [];
    expect(resources).toHaveLength(1);
    expect(resources[0].checkUid).toBeUndefined();
    expect(resources[0].checkGroupUid).toBe(group.uid);
    expect(resources[0].check?.name).toBe(groupName);
    // No member name, no member type, no member count anywhere in the payload.
    for (const name of memberNames) {
      expect(publicBody).not.toContain(name);
    }
    expect(resources[0].check?.type ?? "").toBe("");

    // The rendered public page shows one component under the group's name.
    await page.goto(`/status0/test/${pageSlug}`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByText(groupName).first()).toBeVisible();
    for (const name of memberNames) {
      await expect(page.getByText(name)).toHaveCount(0);
    }
  });

  test("the API rejects a resource with zero or two targets", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const suffix = Date.now().toString().slice(-9);

    const group = await api(page, token, "/api/v1/orgs/test/check-groups", {
      name: `E2E XOR Group ${suffix}`,
      slug: `e2e-xor-${suffix}`,
    });
    const check = await api(page, token, "/api/v1/orgs/test/checks", {
      type: "http",
      name: `e2e-xor-check-${suffix}`,
      config: { url: `https://httpbin.org/anything/${suffix}` },
      period: "00:05:00",
    });

    const statusPage = await api(page, token, "/api/v1/orgs/test/status-pages", {
      name: `E2E XOR Page ${suffix}`,
      slug: `e2e-xor-page-${suffix}`.slice(0, 40),
      visibility: "public",
    });
    const section = await api(
      page,
      token,
      `/api/v1/orgs/test/status-pages/${statusPage.uid}/sections`,
      { name: "Core", slug: "core" },
    );

    const resourcesPath =
      `${API_BASE}/api/v1/orgs/test/status-pages/${statusPage.uid}` +
      `/sections/${section.uid}/resources`;

    for (const body of [
      {},
      { checkUid: check.uid, checkGroupUid: group.uid },
    ]) {
      const resp = await page.request.post(resourcesPath, {
        headers: { Authorization: `Bearer ${token}` },
        data: body,
      });
      expect(resp.status()).toBe(422);

      const err = await resp.json();
      expect(err.code).toBe("VALIDATION_ERROR");
      // The message names BOTH fields so the caller can see the pair.
      const text = JSON.stringify(err);
      expect(text).toContain("checkUid");
      expect(text).toContain("checkGroupUid");
    }
  });
});
