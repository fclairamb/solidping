import { test, expect, API_BASE, uniqueStamp } from "./fixtures";

// Spec 2026-11-17-07: the email suppression list carries recipients' addresses
// (mostly third parties) and re-subscribing overrides their own opt-out, so
// both routes are org-admin only. A viewer gets 403 FORBIDDEN on the API and,
// on the email integration page, no suppression card at all: the request is
// never sent, so there is no error and no retry loop. The owner (positive
// control) still gets the list and the card.
test.describe("Email suppressions (non-admin)", () => {
  test("a viewer is refused the list server-side and sees no suppression card", async ({
    page,
  }) => {
    const stamp = uniqueStamp();
    const password = "Strong-Pass-123!";
    const ownerEmail = `supp-owner-${stamp}@unknown.example`;
    const viewerEmail = `supp-viewer-${stamp}@unknown.example`;

    for (const email of [ownerEmail, viewerEmail]) {
      const resp = await page.request.post(`${API_BASE}/api/v1/test/users`, {
        data: { email, password, name: "Suppressions Test" },
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

    const orgSlug = `supp-${stamp}`;
    const orgResp = await page.request.post(`${API_BASE}/api/v1/orgs`, {
      headers: { Authorization: `Bearer ${ownerSession.accessToken}` },
      data: { name: `Supp Co ${stamp}`, slug: orgSlug },
    });
    expect(orgResp.status()).toBe(201);
    const ownerToken = ((await orgResp.json()) as { accessToken: string })
      .accessToken;
    const ownerAuth = { Authorization: `Bearer ${ownerToken}` };

    const integrationResp = await page.request.post(
      `${API_BASE}/api/v1/orgs/${orgSlug}/integrations`,
      {
        headers: ownerAuth,
        data: {
          type: "email",
          name: `Supp Email ${stamp}`,
          settings: { to: ["ops@acme.example"] },
        },
      },
    );
    expect(integrationResp.status()).toBe(201);
    const integrationUid = ((await integrationResp.json()) as { uid: string })
      .uid;

    const addResp = await page.request.post(
      `${API_BASE}/api/v1/orgs/${orgSlug}/members`,
      { headers: ownerAuth, data: { email: viewerEmail, role: "viewer" } },
    );
    expect(addResp.status()).toBe(201);

    const viewerLogin = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { org: orgSlug, email: viewerEmail, password },
    });
    expect(viewerLogin.status()).toBe(200);
    const session = (await viewerLogin.json()) as {
      accessToken: string;
      refreshToken?: string;
      expiresIn?: number;
    };

    // Server-side: the viewer is refused both routes, the owner is not.
    const listPath = `${API_BASE}/api/v1/orgs/${orgSlug}/email-suppressions`;
    const viewerAuth = { Authorization: `Bearer ${session.accessToken}` };

    const refused = await page.request.get(listPath, { headers: viewerAuth });
    expect(refused.status()).toBe(403);
    expect(((await refused.json()) as { code: string }).code).toBe("FORBIDDEN");

    const refusedDelete = await page.request.delete(
      `${listPath}/00000000-0000-0000-0000-000000000000`,
      { headers: viewerAuth },
    );
    expect(refusedDelete.status()).toBe(403);

    const allowed = await page.request.get(listPath, { headers: ownerAuth });
    expect(allowed.status()).toBe(200);

    // Dashboard: count any request to the suppression list.
    let suppressionRequests = 0;
    page.on("request", (req) => {
      if (req.url().includes("/email-suppressions")) {
        suppressionRequests++;
      }
    });

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

    await page.goto(`orgs/${orgSlug}/integrations/${integrationUid}`);

    // The page itself loads for the viewer, without a redirect.
    await expect(page.getByTestId("recent-notifications-card")).toBeVisible({
      timeout: 10000,
    });
    await expect(page).toHaveURL(new RegExp(`/integrations/${integrationUid}`));
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("suppressions-card")).toHaveCount(0);
    await expect(page.getByTestId("suppression-row")).toHaveCount(0);
    expect(suppressionRequests).toBe(0);
  });
});
