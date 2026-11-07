import { test, expect, API_BASE, uniqueStamp } from "./fixtures";

test.describe("Status page subscribers (admin)", () => {
  test("edit route shows the subscriber list card with a count", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Open the status pages list.
    await page
      .getByTestId("app-sidebar")
      .getByRole("link", { name: "Status Pages" })
      .click();
    await page.waitForURL(/\/status-pages/);
    await page.waitForLoadState("networkidle");

    // Open the edit route for the first status page row.
    const editButton = page
      .locator('[data-testid^="status-page-row-edit-"]')
      .first();
    await expect(editButton).toBeVisible({ timeout: 10000 });
    await editButton.click();
    await page.waitForURL(/\/status-pages\/[^/]+\/edit/);

    // The subscribers card renders with a count badge.
    const card = page.getByTestId("status-page-subscribers");
    await expect(card).toBeVisible({ timeout: 10000 });
    await expect(page.getByTestId("subscriber-count")).toBeVisible();
    await expect(
      card.getByRole("heading", { name: /Subscribers/i }),
    ).toBeVisible();

    await page.screenshot({
      path: "test-results/screenshots/status-page-subscribers.png",
      fullPage: true,
    });
  });
});

// Spec 2026-11-02-04: the subscriber list carries third parties' email
// addresses, so it is org-admin only. A `user` member gets 403 FORBIDDEN on
// the API and, in the dashboard, a "Permission Denied" notice in place of the
// list. The owner (positive control) still gets the list.
test.describe("Status page subscribers (non-admin)", () => {
  test("a user member is refused the list server-side and sees a notice", async ({
    page,
  }) => {
    const stamp = uniqueStamp();
    const password = "Strong-Pass-123!";
    const ownerEmail = `subs-owner-${stamp}@unknown.example`;
    const memberEmail = `subs-user-${stamp}@unknown.example`;

    for (const email of [ownerEmail, memberEmail]) {
      const resp = await page.request.post(`${API_BASE}/api/v1/test/users`, {
        data: { email, password, name: "Subs Test" },
      });
      if (resp.status() !== 201) {
        test.skip(
          true,
          `test user-seed endpoint unavailable (server not in SP_RUNMODE=test?): ${resp.status()}`,
        );
      }
    }

    const ownerLogin = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { email: ownerEmail, password },
    });
    expect(ownerLogin.status()).toBe(200);
    const ownerSession = (await ownerLogin.json()) as { accessToken: string };

    const orgSlug = `subs-${stamp}`;
    const orgResp = await page.request.post(`${API_BASE}/api/v1/orgs`, {
      headers: { Authorization: `Bearer ${ownerSession.accessToken}` },
      data: { name: `Subs Co ${stamp}`, slug: orgSlug },
    });
    expect(orgResp.status()).toBe(201);
    const ownerToken = ((await orgResp.json()) as { accessToken: string })
      .accessToken;
    const ownerAuth = { Authorization: `Bearer ${ownerToken}` };

    const pageResp = await page.request.post(
      `${API_BASE}/api/v1/orgs/${orgSlug}/status-pages`,
      { headers: ownerAuth, data: { name: "Subs Page", slug: `subs-page-${stamp}` } },
    );
    expect(pageResp.status()).toBe(201);
    const statusPageUid = ((await pageResp.json()) as { uid: string }).uid;

    const addResp = await page.request.post(
      `${API_BASE}/api/v1/orgs/${orgSlug}/members`,
      { headers: ownerAuth, data: { email: memberEmail, role: "user" } },
    );
    expect(addResp.status()).toBe(201);

    const memberLogin = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { org: orgSlug, email: memberEmail, password },
    });
    expect(memberLogin.status()).toBe(200);
    const session = (await memberLogin.json()) as {
      accessToken: string;
      refreshToken?: string;
      expiresIn?: number;
    };

    const listPath = `${API_BASE}/api/v1/orgs/${orgSlug}/status-pages/${statusPageUid}/subscribers`;

    // Server-side: the user member is refused, the owner is not.
    const refused = await page.request.get(listPath, {
      headers: { Authorization: `Bearer ${session.accessToken}` },
    });
    expect(refused.status()).toBe(403);
    expect(((await refused.json()) as { code: string }).code).toBe("FORBIDDEN");

    const allowed = await page.request.get(listPath, { headers: ownerAuth });
    expect(allowed.status()).toBe(200);

    // Dashboard: the same member sees the notice, not the list or its count.
    await page.addInitScript(
      ({ accessToken, refreshToken, expiresIn, slug }) => {
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
        localStorage.setItem("solidping_org", slug as string);
      },
      {
        accessToken: session.accessToken,
        refreshToken: session.refreshToken ?? "",
        expiresIn: session.expiresIn ?? 0,
        slug: orgSlug,
      },
    );

    await page.goto(`orgs/${orgSlug}/status-pages/${statusPageUid}/edit`);

    const card = page.getByTestId("status-page-subscribers");
    await expect(card).toBeVisible({ timeout: 10000 });
    await expect(page.getByTestId("subscribers-permission-denied")).toBeVisible();
    await expect(page.getByTestId("subscriber-count")).toHaveCount(0);
    // Adding a webhook/Slack delivery stays open to a `user` member.
    await expect(page.getByTestId("subscriber-add-endpoint")).toBeVisible();
  });
});
