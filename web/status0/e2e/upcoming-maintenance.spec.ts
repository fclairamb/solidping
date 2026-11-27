import { test, expect, type APIRequestContext } from "@playwright/test";
import { API_BASE as BASE, STATUS_BASE } from "./fixtures";

/**
 * Spec 2026-11-27-06: the public page announces a maintenance window that
 * starts within 7 days, and hides the section when there is none.
 */

const ORG_LOGINS = [
  { org: "default", email: "admin@solidping.io", password: "solidpass" },
  { org: "test", email: "test@test.com", password: "test" },
];

type Json = Record<string, unknown>;

async function login(
  request: APIRequestContext,
): Promise<{ org: string; token: string }> {
  const wanted = process.env.E2E_ORG;
  const candidates = wanted
    ? ORG_LOGINS.filter((c) => c.org === wanted)
    : ORG_LOGINS;
  for (const { org, email, password } of candidates) {
    const response = await request.post(`${BASE}/api/v1/auth/login`, {
      data: { org, email, password },
    });
    if (response.ok()) {
      const body = (await response.json()) as { accessToken: string };
      return { org, token: body.accessToken };
    }
  }
  throw new Error(`Could not log in to ${BASE} for any known seeded org`);
}

async function seedPage(
  request: APIRequestContext,
  withWindow: boolean,
): Promise<{ url: string; cleanup: () => Promise<void>; name: string }> {
  const { org, token } = await login(request);
  const auth = { Authorization: `Bearer ${token}` };
  const suffix = `${Date.now().toString().slice(-8)}${withWindow ? "w" : "n"}`;
  const created: string[] = [];

  const post = async (path: string, data: Json): Promise<Json> => {
    const response = await request.post(`${BASE}${path}`, {
      headers: auth,
      data,
    });
    expect(
      response.status(),
      `POST ${path} -> ${await response.text()}`,
    ).toBeLessThan(300);
    return (await response.json()) as Json;
  };

  const check = await post(`/api/v1/orgs/${org}/checks`, {
    type: "http",
    name: `e2e-upc-${suffix}`,
    config: { url: "https://example.com" },
    period: "00:05:00",
  });
  created.push(`/api/v1/orgs/${org}/checks/${check.uid}`);

  const slug = `e2e-upc-${suffix}`;
  const statusPage = await post(`/api/v1/orgs/${org}/status-pages`, {
    name: `E2E Upcoming ${suffix}`,
    slug,
    visibility: "public",
    autoPublish: false,
  });
  created.push(`/api/v1/orgs/${org}/status-pages/${statusPage.uid}`);

  const section = await post(
    `/api/v1/orgs/${org}/status-pages/${statusPage.uid}/sections`,
    { name: "Core", slug: `core-${suffix}` },
  );
  const name = `Billing component ${suffix}`;
  await post(
    `/api/v1/orgs/${org}/status-pages/${statusPage.uid}/sections/${section.uid}/resources`,
    { checkUid: check.uid, publicName: name, position: 0 },
  );

  if (withWindow) {
    const now = Date.now();
    const window = await post(`/api/v1/orgs/${org}/maintenance-windows`, {
      title: `E2E planned upgrade ${suffix}`,
      description: "Read-only mode during the upgrade",
      startAt: new Date(now + 2 * 3600_000).toISOString(),
      endAt: new Date(now + 3 * 3600_000).toISOString(),
      recurrence: "none",
    });
    created.push(`/api/v1/orgs/${org}/maintenance-windows/${window.uid}`);
    const set = await request.put(
      `${BASE}/api/v1/orgs/${org}/maintenance-windows/${window.uid}/checks`,
      { headers: auth, data: { checkUids: [check.uid] } },
    );
    expect(set.status(), await set.text()).toBeLessThan(300);
  }

  return {
    url: `${BASE}${STATUS_BASE}/${org}/${slug}`,
    name,
    cleanup: async () => {
      for (const path of created.reverse()) {
        await request.delete(`${BASE}${path}`, { headers: auth }).catch(() => {});
      }
    },
  };
}

test.describe("Public status page - upcoming maintenance", () => {
  test("announces a window starting in 2 hours", async ({ page, request }) => {
    const fx = await seedPage(request, true);
    try {
      await page.goto(fx.url);
      const section = page.getByTestId("upcoming-maintenance");
      await expect(section).toBeVisible();
      const items = page.getByTestId("upcoming-maintenance-item");
      await expect(items).toHaveCount(1);
      await expect(items.first()).toContainText("E2E planned upgrade");
      await expect(items.first()).toContainText("Read-only mode");
      await expect(items.first()).toContainText(fx.name);
      await expect(
        page.getByTestId("upcoming-maintenance-active-badge"),
      ).toHaveCount(0);
    } finally {
      await fx.cleanup();
    }
  });

  test("hides the section when nothing is planned", async ({
    page,
    request,
  }) => {
    const fx = await seedPage(request, false);
    try {
      await page.goto(fx.url);
      await expect(page.getByTestId("resource-row")).toHaveCount(1);
      await expect(page.getByTestId("upcoming-maintenance")).toHaveCount(0);
    } finally {
      await fx.cleanup();
    }
  });
});
