import { test, expect, type Page } from "@playwright/test";
import { API_BASE, DASH_BASE, escapeRegExp, uniqueStamp } from "./fixtures";

/**
 * Spec 2026-09-25-15 — an org-less session never switched into an org it
 * belongs to.
 *
 * An org-less session (an access token scoped to no org, and no refresh token
 * by design) is what a federated login refused by some org used to hand out,
 * and what a password login by a user with no membership still does. When
 * such a session opened an org it IS a member of, OrgLayout's switch guard
 * required `auth.org !== null`, so no switch-org ran: every org-scoped call
 * answered 403, the live socket closed 4403 and tried to refresh, found no
 * refresh token, cleared the session and sent a real member to
 * `/orgs/<org>/login?session_expired=true`.
 *
 * The session now re-mints through `POST /auth/switch-org` before the org's
 * children, and the live socket, mount.
 */

/**
 * Provisions an ordinary member of a fresh `acc-*` org and seeds the browser
 * with an ORG-LESS session for it: the token of a login made before the org
 * existed, with no refresh token. The org-scoped session POST /api/v1/orgs
 * answers with is deliberately thrown away.
 *
 * (`test@test.com` cannot carry this: it is a superadmin, and superadmins
 * never switch.)
 */
async function seedOrgLessMember(page: Page): Promise<string> {
  const stamp = uniqueStamp();
  const email = `org-less-${stamp}@unknown.example`;
  const password = "Strong-Pass-123!";

  const created = await page.request.post(`${API_BASE}/api/v1/test/users`, {
    data: { email, password, name: "Org-less Member" },
  });
  test.skip(
    created.status() !== 201,
    `test user-seed endpoint unavailable (server not in SP_RUNMODE=test?): ${created.status()}`,
  );

  const login = await page.request.post(`${API_BASE}/api/v1/auth/login`, { data: { email, password } });
  expect(login.status()).toBe(200);
  const orgLess = (await login.json()) as {
    accessToken: string;
    refreshToken?: string;
    expiresIn?: number;
  };
  expect(orgLess.refreshToken ?? null, "precondition: an org-less login has no refresh token").toBeNull();

  const slug = `acc-${stamp}`;
  const org = await page.request.post(`${API_BASE}/api/v1/orgs`, {
    headers: { Authorization: `Bearer ${orgLess.accessToken}` },
    data: { name: `Org-less Member ${stamp}`, slug },
  });
  expect(org.status()).toBe(201);

  await page.addInitScript(
    ({ accessToken, expiresIn }) => {
      localStorage.setItem("solidping_session_token", accessToken);
      localStorage.setItem("solidping_expires_at", String(Date.now() + expiresIn * 1000));
      localStorage.setItem("solidping_expires_in", String(expiresIn));
    },
    { accessToken: orgLess.accessToken, expiresIn: orgLess.expiresIn ?? 3600 },
  );

  return slug;
}

test.describe("Org-less session switching into a member org", () => {
  for (const start of ["org root", "org root with a stale stored org", "another org's login page"] as const) {
    test(`from the ${start}: switches, renders, and keeps the session`, async ({ page }) => {
      const slug = await seedOrgLessMember(page);
      if (start === "org root with a stale stored org") {
        // A slug left in storage by an earlier session, naming the very org
        // being opened. Trusted, it made the org-less token look already
        // scoped to that org and the switch never ran; /auth/me must clear it.
        await page.addInitScript((stale) => {
          if (!localStorage.getItem("solidping_refresh_token")) {
            localStorage.setItem("solidping_org", stale);
          }
        }, slug);
      }

      const navigations: string[] = [];
      page.on("framenavigated", (frame) => {
        if (frame === page.mainFrame()) navigations.push(frame.url());
      });
      const consoleErrors: string[] = [];
      page.on("console", (msg) => {
        if (msg.type() === "error") consoleErrors.push(msg.text());
      });
      const forbidden: string[] = [];
      page.on("response", (response) => {
        if (response.status() === 403) forbidden.push(response.url());
      });

      // Order of events: the org's live socket must not be dialled before the
      // session is scoped to the org.
      const isSwitch = (url: string, method: string) =>
        url.endsWith("/api/v1/auth/switch-org") && method === "POST";
      const events: string[] = [];
      page.on("websocket", () => events.push("websocket"));
      page.on("response", (response) => {
        if (isSwitch(response.url(), response.request().method())) events.push("switch-org");
      });
      const switched = page.waitForResponse(
        (response) => isSwitch(response.url(), response.request().method()),
        { timeout: 20000 },
      );

      // "another org's login page" is the report's path: the login page sees an
      // authenticated session and sends it to an org it can use.
      await page.goto(start === "another org's login page" ? "orgs/test/login" : `orgs/${slug}`);

      const switchResponse = await switched;
      expect(switchResponse.status()).toBe(200);

      await page.waitForURL(new RegExp(`${escapeRegExp(`${DASH_BASE}/orgs/${slug}`)}(/|$)`), {
        timeout: 20000,
      });
      await expect(page.getByTestId("sidebar-trigger")).toBeVisible({ timeout: 20000 });

      // The switch left a full session behind: a refresh token scoped to the org.
      await expect
        .poll(() => page.evaluate(() => localStorage.getItem("solidping_refresh_token")))
        .toBeTruthy();

      await page.waitForLoadState("networkidle");

      const firstSocket = events.indexOf("websocket");
      if (firstSocket !== -1) {
        expect(
          events.indexOf("switch-org"),
          "the live socket was dialled before the session was scoped to the org",
        ).toBeLessThan(firstSocket);
      }
      expect(forbidden, "no org-scoped request may be refused on the member's own org").toEqual([]);
      expect(navigations.filter((u) => u.includes("session_expired=true"))).toEqual([]);
      expect(consoleErrors.filter((m) => m.includes("token refresh failed"))).toEqual([]);
      await expect(page).toHaveURL(new RegExp(`${escapeRegExp(`${DASH_BASE}/orgs/${slug}`)}(/|$)`));
    });
  }
});
