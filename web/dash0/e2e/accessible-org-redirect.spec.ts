import { test as base, expect, type Page } from "@playwright/test";
import { test, API_BASE } from "./fixtures";

/**
 * Spec 2026-09-08-01 §C — landing on an org you cannot use is no longer a dead
 * end.
 *
 * Any session can end up on a foreign org URL: a bookmark to an org you were
 * removed from, a link a colleague pasted from THEIR org, a slug from an old
 * email, the live demo entered from another org's login page. Before this
 * spec, the org layout let non-members "fall through to the normal 403
 * handling": every child query answered 403, `QueryErrorView` rendered
 * "Permission Denied", and that card's only button linked back to the very
 * same org. There was no way out but editing the URL by hand.
 *
 * The client already holds the answer — `/auth/me` returns every org the user
 * belongs to — so the layout now redirects to one they can use, and says so.
 */

/** An org slug nobody is a member of. */
const FOREIGN_ORG = "not-my-org";

/**
 * Provisions a REAL ordinary (non-super-admin) member and seeds the browser
 * with its session.
 *
 * The suite's shared `test@test.com` fixture cannot carry these tests: it is a
 * **superadmin** (server/test/testdata/testdata.go), and a superadmin crosses
 * orgs on its claims alone — `pickAccessibleOrg` deliberately hands it back the
 * URL's org untouched. Running the non-member case as that user would assert
 * nothing at all. So: seed a zero-org user through the test-only
 * `POST /api/v1/test/users` endpoint (the same one create-org.spec.ts uses, and
 * for the same reason — `POST /auth/register` is not usable in this
 * environment), log in for real, and create one organization through
 * `POST /api/v1/orgs`, whose response carries the fresh org-scoped session the
 * browser then adopts.
 *
 * @returns the slug of the single org this session belongs to.
 */
async function seedOrdinaryMember(page: Page): Promise<string> {
  const stamp = Date.now().toString(36) + Math.random().toString(36).slice(2, 6);
  const email = `accessible-org-${stamp}@unknown.example`;
  const password = "Strong-Pass-123!";

  const created = await page.request.post(`${API_BASE}/api/v1/test/users`, {
    data: { email, password, name: "Accessible Org User" },
  });
  if (created.status() !== 201) {
    test.skip(
      true,
      `test user-seed endpoint unavailable (server not in SP_RUNMODE=test?): ${created.status()}`,
    );
  }

  const loginResp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { email, password },
  });
  expect(loginResp.status()).toBe(200);
  const noOrgSession = (await loginResp.json()) as { accessToken: string };

  const slug = `acc-${stamp}`;
  const orgResp = await page.request.post(`${API_BASE}/api/v1/orgs`, {
    headers: { Authorization: `Bearer ${noOrgSession.accessToken}` },
    data: { name: `Accessible Org ${stamp}`, slug },
  });
  expect(orgResp.status()).toBe(201);
  const session = (await orgResp.json()) as {
    slug: string;
    accessToken: string;
    refreshToken?: string;
    expiresIn?: number;
  };
  expect(session.slug).toBe(slug);

  await page.addInitScript(
    ({ accessToken, refreshToken, expiresIn }) => {
      localStorage.setItem("solidping_session_token", accessToken as string);
      if (refreshToken) {
        localStorage.setItem("solidping_refresh_token", refreshToken as string);
      }
      if (expiresIn) {
        localStorage.setItem(
          "solidping_expires_at",
          String(Date.now() + Number(expiresIn) * 1000),
        );
        localStorage.setItem("solidping_expires_in", String(expiresIn));
      }
    },
    {
      accessToken: session.accessToken,
      refreshToken: session.refreshToken ?? "",
      expiresIn: session.expiresIn ?? 0,
    },
  );

  return slug;
}

// Uses the plain (unauthenticated) `page` fixture: every test here brings its
// own session, either a freshly provisioned ordinary member or — for the
// super-admin control — the shared fixture.
base.describe("Redirect to an accessible org", () => {
  base("a member landing on an org they do not belong to is redirected to their own, with no 403", async ({
    page,
  }) => {
    const myOrg = await seedOrdinaryMember(page);

    // The "no 403s" harness from create-org.spec.ts. The whole point of the
    // fix is that the children never mount against the foreign org, so not one
    // org-scoped request may be refused on the way out.
    const forbiddenUrls: string[] = [];
    page.on("response", (response) => {
      if (response.status() === 403) forbiddenUrls.push(response.url());
    });

    await page.goto(`orgs/${FOREIGN_ORG}`);

    await page.waitForURL(new RegExp(`/orgs/${myOrg}(/|$)`), { timeout: 20000 });
    expect(page.url()).not.toContain(FOREIGN_ORG);

    // The redirect is explained rather than silent — otherwise the URL just
    // changes under the visitor with no hint that their link was wrong.
    await expect(page.getByText(new RegExp(FOREIGN_ORG))).toBeVisible({
      timeout: 10000,
    });

    // And the destination is a WORKING dashboard, not another error card.
    await expect(page.getByTestId("sidebar-trigger")).toBeVisible({
      timeout: 20000,
    });
    await page.waitForLoadState("networkidle");
    expect(
      forbiddenUrls,
      "no request may be answered 403 on the way out of a foreign org",
    ).toEqual([]);
  });

  base("a sub-path of a foreign org lands on the org root, not a 404 one hop later", async ({
    page,
  }) => {
    // Check / status-page / incident uids do not carry across organizations,
    // so the sub-path is deliberately dropped: preserving it would only 404
    // against the org the visitor was sent to.
    const myOrg = await seedOrdinaryMember(page);

    await page.goto(
      `orgs/${FOREIGN_ORG}/checks/00000000-0000-0000-0000-000000000000`,
    );

    await page.waitForURL(new RegExp(`/orgs/${myOrg}/?$`), { timeout: 20000 });
    await expect(page.getByTestId("sidebar-trigger")).toBeVisible({
      timeout: 20000,
    });
  });

  base("an org the user IS a member of is never redirected away", async ({
    page,
  }) => {
    // The positive control: without it, a guard that always redirected would
    // pass both tests above.
    const myOrg = await seedOrdinaryMember(page);

    await page.goto(`orgs/${myOrg}`);
    await page.waitForLoadState("networkidle");

    expect(page.url()).toContain(`/orgs/${myOrg}`);
    await expect(page.getByTestId("sidebar-trigger")).toBeVisible({
      timeout: 20000,
    });
  });
});

test.describe("Redirect to an accessible org (super admin)", () => {
  test("a super admin keeps the org the URL names", async ({
    authenticatedPage: page,
  }) => {
    // `test@test.com` is a superadmin (testdata.go). Super admins cross orgs on
    // their claims alone, so the guard must leave them exactly where they
    // asked to be — including on an org they hold no membership in. This is
    // the second half of the positive control: it proves the redirect keys on
    // membership rather than firing for every unfamiliar slug.
    await page.goto(`orgs/${FOREIGN_ORG}`);
    await page.waitForLoadState("networkidle");

    expect(page.url()).toContain(FOREIGN_ORG);
  });
});
