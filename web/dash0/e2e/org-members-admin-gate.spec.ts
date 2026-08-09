import { test, expect, API_BASE } from "./fixtures";

// Spec 2026-08-09-03: adding, re-roling and removing a member is admin-only.
// Before this spec, any member — including a viewer — could POST/PATCH/DELETE
// /orgs/:org/members, up to promoting themselves to admin, by calling the API
// directly. This proves the server-side gate first (bypassing the UI
// entirely). The dashboard's /organization/* layout (organization.tsx) has
// separately redirected non-admins away from the whole section since the
// project's first commit, so a non-admin can never reach the members page's
// row controls through normal navigation either — this test pins that too.
test.describe("Members write routes are admin-only", () => {
  // seedOwnedOrg creates a user, logs them in, creates an org through the
  // real POST /api/v1/orgs, and seeds the browser with the returned
  // org-scoped session — same technique as org-owner-delete.spec.ts.
  async function seedOwnedOrg(page: import("@playwright/test").Page) {
    const stamp = Date.now() + Math.floor(Math.random() * 1000);
    const email = `gate-owner-${stamp}@unknown.example`;
    const password = "Strong-Pass-123!";

    const createUserResp = await page.request.post(
      `${API_BASE}/api/v1/test/users`,
      { data: { email, password, name: "Gate Owner" } },
    );
    if (createUserResp.status() !== 201) {
      test.skip(
        true,
        `test user-seed endpoint unavailable (server not in SP_RUNMODE=test?): ${createUserResp.status()}`,
      );
    }

    const loginResp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { email, password },
    });
    expect(loginResp.status()).toBe(200);

    const session = (await loginResp.json()) as { accessToken: string };

    const orgSlug = `gate-${stamp.toString(36)}`;
    const createOrgResp = await page.request.post(`${API_BASE}/api/v1/orgs`, {
      headers: { Authorization: `Bearer ${session.accessToken}` },
      data: { name: `Gate Co ${stamp}`, slug: orgSlug },
    });
    expect(createOrgResp.status()).toBe(201);

    const org = (await createOrgResp.json()) as {
      slug: string;
      accessToken: string;
    };

    return { orgSlug, ownerToken: org.accessToken };
  }

  // seedNonAdmin adds a plain "user" role member to the org (owner-created)
  // and returns their logged-in session, plus their uid for the PATCH target.
  async function seedNonAdmin(
    page: import("@playwright/test").Page,
    orgSlug: string,
    ownerToken: string,
  ) {
    const stamp = Date.now() + Math.floor(Math.random() * 1000);
    const email = `gate-user-${stamp}@unknown.example`;
    const password = "Strong-Pass-123!";

    const createResp = await page.request.post(`${API_BASE}/api/v1/test/users`, {
      data: { email, password, name: "Gate User" },
    });
    if (createResp.status() !== 201) {
      test.skip(true, "test user-seed endpoint unavailable");
    }

    const addResp = await page.request.post(
      `${API_BASE}/api/v1/orgs/${orgSlug}/members`,
      {
        headers: { Authorization: `Bearer ${ownerToken}` },
        data: { email, role: "user" },
      },
    );
    expect(addResp.status()).toBe(201);
    const added = (await addResp.json()) as { uid: string };

    const login = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { org: orgSlug, email, password },
    });
    expect(login.status()).toBe(200);
    const session = (await login.json()) as {
      accessToken: string;
      refreshToken?: string;
      expiresIn?: number;
    };

    return { email, uid: added.uid, session };
  }

  test("a non-admin member is refused server-side and redirected out of the members page", async ({
    page,
  }) => {
    const { orgSlug, ownerToken } = await seedOwnedOrg(page);
    const { email: userEmail, uid: userUid, session } = await seedNonAdmin(
      page,
      orgSlug,
      ownerToken,
    );

    // Server-side proof first: the raw PATCH is 403 even bypassing the UI —
    // and the negative control that the row is untouched.
    const patchResp = await page.request.patch(
      `${API_BASE}/api/v1/orgs/${orgSlug}/members/${userUid}`,
      {
        headers: { Authorization: `Bearer ${session.accessToken}` },
        data: { role: "admin" },
      },
    );
    expect(patchResp.status()).toBe(403);
    const body = (await patchResp.json()) as { code: string };
    expect(body.code).toBe("FORBIDDEN");

    const stillResp = await page.request.get(
      `${API_BASE}/api/v1/orgs/${orgSlug}/members/${userUid}`,
      { headers: { Authorization: `Bearer ${ownerToken}` } },
    );
    expect(stillResp.status()).toBe(200);
    const still = (await stillResp.json()) as { role: string };
    expect(still.role).toBe("user");

    // Positive control for the gate itself: the owner CAN make the same
    // change, proving the 403 above came from the caller's role, not from a
    // malformed request.
    const ownerPatchResp = await page.request.patch(
      `${API_BASE}/api/v1/orgs/${orgSlug}/members/${userUid}`,
      {
        headers: { Authorization: `Bearer ${ownerToken}` },
        data: { role: "viewer" },
      },
    );
    expect(ownerPatchResp.status()).toBe(200);

    // Swap the browser session to the non-admin and check the dashboard.
    await page.addInitScript(
      ({ accessToken, refreshToken, expiresIn, slug }) => {
        localStorage.setItem("solidping_session_token", accessToken as string);
        if (refreshToken) {
          localStorage.setItem(
            "solidping_refresh_token",
            refreshToken as string,
          );
        }
        if (expiresIn) {
          localStorage.setItem(
            "solidping_expires_at",
            String(Date.now() + Number(expiresIn) * 1000),
          );
          localStorage.setItem("solidping_expires_in", String(expiresIn));
        }
        localStorage.setItem("solidping_org", slug as string);
      },
      {
        accessToken: session.accessToken,
        refreshToken: session.refreshToken ?? "",
        expiresIn: session.expiresIn ?? 0,
        slug: orgSlug,
      },
    );

    await page.goto(`orgs/${orgSlug}/organization/members`);
    await page.waitForLoadState("networkidle");

    // The entire /organization/* section (organization.tsx, the shared
    // layout for members/invitations/settings/etc.) has redirected non-admins
    // to the org dashboard since the very first commit — so a non-admin can
    // never even render the members table through normal navigation. That is
    // a stronger guarantee than "the row controls are disabled," so assert
    // it directly: the URL bounces back out of /organization entirely.
    await expect(page).toHaveURL(
      new RegExp(`/orgs/${orgSlug}(?!/organization)(/)?$`),
    );
    await expect(page.getByTestId(`member-role-${userEmail}`)).toHaveCount(0);

    // Positive control: the redirect is about the role, not a broken page —
    // the org's own dashboard rendered underneath it.
    await expect(page.getByRole("heading", { name: "Dashboard" })).toBeVisible();
  });
});
