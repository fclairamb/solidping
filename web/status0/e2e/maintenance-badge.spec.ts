import { test, expect, type APIRequestContext } from "@playwright/test";
import { API_BASE as BASE, STATUS_BASE } from "./fixtures";

/**
 * Verifies the "Scheduled Maintenance" badge on the public status page.
 *
 * The badge renders for a resource whose check sits inside an *active*
 * maintenance window (driven by `check.inMaintenance` on the public status
 * payload), and it REPLACES the normal status badge.
 *
 * This spec seeds everything it needs (check + status page + section +
 * resource + an active window) instead of hoping the server it runs against
 * happens to have a resource under maintenance. The stock seeds do not: neither
 * `make dev` (`default`) nor `SP_RUNMODE=test` (`test`, what CI runs) creates a
 * single section, resource or maintenance window, so the previous
 * "skip when the badge count is 0" version of this file skipped on 100% of CI
 * runs — green and proving nothing, which is the exact blindness spec
 * 2026-09-09-04 set out to remove.
 */

/**
 * Credentials per seeded org: `make dev` seeds `default`, `SP_RUNMODE=test`
 * (CI) seeds `test` instead, so probe rather than hardcode.
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

interface Fixture {
  org: string;
  token: string;
  url: string;
  slug: string;
  statusPageUid: string;
  windowUid: string;
  checkUids: string[];
  /** Public name of the resource whose check is under maintenance. */
  maintenanceName: string;
  /** Public name of the resource that is NOT under maintenance. */
  quietName: string;
}

/**
 * Seeds a dedicated public status page carrying two resources: one whose check
 * is inside an active maintenance window, one whose check is not.
 *
 * The second resource is what makes the assertions worth making — the badge has
 * to track the WINDOW, not merely exist on the page, and a badge rendered on
 * every row would satisfy a positive-only test while being wrong.
 */
async function seedMaintenanceFixture(
  request: APIRequestContext,
): Promise<Fixture> {
  const { org, token } = await login(request);
  const auth = { Authorization: `Bearer ${token}` };
  const suffix = Date.now().toString().slice(-9);

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

  const makeCheck = (name: string) =>
    post(`/api/v1/orgs/${org}/checks`, {
      type: "http",
      name,
      config: { url: "https://example.com" },
      period: "00:05:00",
    });

  const maintenanceCheck = await makeCheck(`e2e-maint-under-${suffix}`);
  const quietCheck = await makeCheck(`e2e-maint-quiet-${suffix}`);

  const slug = `e2e-maint-${suffix}`.slice(0, 40);
  const statusPage = await post(`/api/v1/orgs/${org}/status-pages`, {
    name: `E2E Maintenance ${suffix}`,
    slug,
    visibility: "public",
    // No auto-publish: a freshly created http check can go down on its own and
    // an incident banner would only add noise to what this spec looks at.
    autoPublish: false,
  });
  const section = await post(
    `/api/v1/orgs/${org}/status-pages/${statusPage.uid}/sections`,
    { name: "Core", slug: `core-${suffix}`.slice(0, 40) },
  );

  // Deliberately NOT "Under maintenance" / "Not under maintenance": Playwright's
  // `hasText` is a case-insensitive SUBSTRING match, so the second name would
  // also match the first row's filter and both locators would resolve to two
  // rows. Neither name is a substring of the other.
  const maintenanceName = `Parked component ${suffix}`;
  const quietName = `Sibling component ${suffix}`;
  const resourcePath = `/api/v1/orgs/${org}/status-pages/${statusPage.uid}/sections/${section.uid}/resources`;
  await post(resourcePath, {
    checkUid: maintenanceCheck.uid,
    publicName: maintenanceName,
    position: 0,
  });
  await post(resourcePath, {
    checkUid: quietCheck.uid,
    publicName: quietName,
    position: 1,
  });

  // A window that started 5 minutes ago and runs for another hour: active
  // "now" on any clock the server and this process can disagree about.
  const now = Date.now();
  const window = await post(`/api/v1/orgs/${org}/maintenance-windows`, {
    title: `E2E maintenance window ${suffix}`,
    startAt: new Date(now - 5 * 60_000).toISOString(),
    endAt: new Date(now + 60 * 60_000).toISOString(),
    recurrence: "none",
  });
  const setChecks = await request.put(
    `${BASE}/api/v1/orgs/${org}/maintenance-windows/${window.uid}/checks`,
    { headers: auth, data: { checkUids: [maintenanceCheck.uid] } },
  );
  expect(setChecks.status(), await setChecks.text()).toBeLessThan(300);

  return {
    org,
    token,
    url: `${BASE}${STATUS_BASE}/${org}/${slug}`,
    slug,
    statusPageUid: String(statusPage.uid),
    windowUid: String(window.uid),
    checkUids: [String(maintenanceCheck.uid), String(quietCheck.uid)],
    maintenanceName,
    quietName,
  };
}

/** Best-effort teardown, so repeated runs do not pile up active windows. */
async function cleanUp(request: APIRequestContext, fixture: Fixture) {
  const auth = { Authorization: `Bearer ${fixture.token}` };
  const paths = [
    `/api/v1/orgs/${fixture.org}/maintenance-windows/${fixture.windowUid}`,
    `/api/v1/orgs/${fixture.org}/status-pages/${fixture.statusPageUid}`,
    ...fixture.checkUids.map((uid) => `/api/v1/orgs/${fixture.org}/checks/${uid}`),
  ];
  for (const path of paths) {
    await request.delete(`${BASE}${path}`, { headers: auth }).catch(() => {});
  }
}

test.describe("Public status page — scheduled maintenance badge", () => {
  test("a resource under an active window shows the badge, its sibling does not", async ({
    page,
    request,
  }) => {
    const fixture = await seedMaintenanceFixture(request);

    try {
      // Server truth first: the public payload must carry inMaintenance on
      // exactly one of the two resources. If it does not, the DOM assertions
      // below would be reporting on the wrong thing.
      const publicPage = await request.get(
        `${BASE}/api/v1/status-pages/${fixture.org}/${fixture.slug}`,
      );
      expect(publicPage.status(), await publicPage.text()).toBe(200);
      const payload = (await publicPage.json()) as {
        sections?: {
          resources?: {
            publicName?: string;
            check?: { inMaintenance?: boolean };
          }[];
        }[];
      };
      const resources = (payload.sections ?? []).flatMap((s) => s.resources ?? []);
      expect(
        resources.map((r) => [r.publicName, Boolean(r.check?.inMaintenance)]),
      ).toEqual([
        [fixture.maintenanceName, true],
        [fixture.quietName, false],
      ]);

      await page.goto(fixture.url);
      await page.waitForLoadState("networkidle");

      const maintenanceRow = page
        .getByTestId("resource-row")
        .filter({ hasText: fixture.maintenanceName });
      const quietRow = page
        .getByTestId("resource-row")
        .filter({ hasText: fixture.quietName });
      await expect(maintenanceRow).toHaveCount(1);
      await expect(quietRow).toHaveCount(1);

      // POSITIVE: the badge is there, and it says what it should.
      const badge = maintenanceRow.getByTestId("resource-maintenance-badge");
      await expect(badge).toHaveCount(1);
      await expect(badge).toBeVisible();
      await expect(badge).toHaveText(/Scheduled Maintenance/i);
      // It REPLACES the normal status badge rather than sitting next to it.
      await expect(
        maintenanceRow.getByTestId("resource-status-badge"),
      ).toHaveCount(0);

      // NEGATIVE: the sibling, on the same page and rendered by the same
      // component, wears the ordinary status badge and no maintenance badge.
      // Without this half, a component that badged every row would pass.
      await expect(
        quietRow.getByTestId("resource-maintenance-badge"),
      ).toHaveCount(0);
      await expect(quietRow.getByTestId("resource-status-badge")).toHaveCount(1);
    } finally {
      await cleanUp(request, fixture);
    }
  });
});
