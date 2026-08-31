import { test, expect, API_BASE } from "./fixtures";

test.describe("Invitations", () => {
  test("should create invitation with correct base URL", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Create invitation via API using the page's auth context
    const response = await page.request.post(
      `${API_BASE}/api/v1/orgs/test/invitations`,
      {
        data: {
          email: `invite-${Date.now()}@example.com`,
          role: "user",
          expiresIn: "24h",
          app: "dash0",
        },
      }
    );

    expect(response.status()).toBe(201);
    const body = await response.json();

    // The invite URL should use the server's base URL (not hardcoded localhost)
    expect(body.inviteUrl).toBeTruthy();
    expect(body.inviteUrl).toContain("/dash0/invite/");
    expect(body.token).toBeTruthy();

    // Verify the URL starts with the server base URL
    expect(body.inviteUrl).toMatch(/^https?:\/\//);
  });

  test("should use dash app in invite URL", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    const response = await page.request.post(
      `${API_BASE}/api/v1/orgs/test/invitations`,
      {
        data: {
          email: `invite-dash-${Date.now()}@example.com`,
          role: "user",
          expiresIn: "24h",
          app: "dash",
        },
      }
    );

    expect(response.status()).toBe(201);
    const body = await response.json();

    expect(body.inviteUrl).toContain("/dash/invite/");
  });

  test("should reject invalid app", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    const response = await page.request.post(
      `${API_BASE}/api/v1/orgs/test/invitations`,
      {
        data: {
          email: `invite-bad-${Date.now()}@example.com`,
          role: "user",
          expiresIn: "24h",
          app: "invalid",
        },
      }
    );

    expect(response.status()).toBe(400);
  });

  test("should default to dash0 when app is omitted", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    const response = await page.request.post(
      `${API_BASE}/api/v1/orgs/test/invitations`,
      {
        data: {
          email: `invite-default-${Date.now()}@example.com`,
          role: "user",
          expiresIn: "24h",
        },
      }
    );

    expect(response.status()).toBe(201);
    const body = await response.json();

    expect(body.inviteUrl).toContain("/dash0/invite/");
  });

  test("reports emailSent: false in test mode (email disabled)", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    const response = await page.request.post(
      `${API_BASE}/api/v1/orgs/test/invitations`,
      {
        data: {
          email: `invite-email-status-${Date.now()}@example.com`,
          role: "user",
          expiresIn: "24h",
          app: "dash0",
        },
      }
    );

    expect(response.status()).toBe(201);
    const body = await response.json();

    // Test mode runs with SP_EMAIL_ENABLED unset/false, so the create
    // response must say the email was NOT queued.
    expect(body.emailSent).toBe(false);
  });

  test("dialog shows the link-is-primary fallback copy when email is not configured", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/organization/invitations");

    await page.getByRole("button", { name: "Invite" }).click();

    const dialog = page.getByRole("dialog");
    await expect(dialog).toBeVisible();

    await dialog
      .getByLabel("Email", { exact: true })
      .fill(`invite-dialog-${Date.now()}@example.com`);
    await dialog.getByRole("button", { name: "Send invitation" }).click();

    // Test mode runs with email disabled, so the dialog must show the
    // "link is your only channel" fallback copy — not the "email was sent"
    // success copy.
    await expect(
      dialog.getByText(
        "Email sending is not configured on this instance — share this link with the recipient."
      )
    ).toBeVisible();
    await expect(
      dialog.getByText(/An invitation email was sent to/)
    ).not.toBeVisible();

    // The invite link is still surfaced as the (only) way to share it.
    await expect(dialog.locator("input[readonly]")).toHaveValue(
      /\/dash0\/invite\//
    );
  });

  // Spec 2026-08-31-02: this suite asserted the shape of `inviteUrl` but
  // never actually opened it — the gap that would have answered "does the
  // invite flow work" in seconds instead of triggering an investigation.
  test.describe("opening the invite link", () => {
    test("logged-out visitor sees the join card and can accept as a new user", async ({
      authenticatedPage,
      page,
    }) => {
      const stamp = Date.now();
      const email = `invite-e2e-${stamp}@example.com`;

      const createResp = await authenticatedPage.request.post(
        `${API_BASE}/api/v1/orgs/test/invitations`,
        {
          data: {
            email,
            role: "user",
            expiresIn: "24h",
            app: "dash0",
          },
        }
      );
      expect(createResp.status()).toBe(201);
      const { token } = await createResp.json();
      expect(token).toBeTruthy();

      // Read the same public payload the page itself fetches, so the
      // assertions below check against the server's actual values instead
      // of hardcoding fixture data (org display name, masking rules) that
      // is free to change independently of this test.
      const infoResp = await page.request.get(
        `${API_BASE}/api/v1/auth/invite/${token}`
      );
      expect(infoResp.status()).toBe(200);
      const info = await infoResp.json();
      expect(info.email).not.toBe(email); // must be masked, not the raw address
      expect(info.email).toContain("***@");

      // `page` (not `authenticatedPage`) starts with no storage state — a
      // genuinely logged-out visitor, which is the scenario this spec is
      // about: the invite link is meant to be opened by someone who has no
      // session yet.
      await page.goto(`invite/${token}`);
      await page.waitForLoadState("networkidle");

      await expect(
        page.getByRole("heading", { name: `Join ${info.orgName}` })
      ).toBeVisible();
      await expect(page.getByText(info.role, { exact: true })).toBeVisible();
      await expect(page.getByText(info.email)).toBeVisible();

      await page.locator("#password").fill("Strong-Pass-123!");
      await page
        .getByRole("button", { name: "Create account & join" })
        .click();

      // Lands authenticated in the org — not stuck on /invite, not bounced
      // to /login.
      await page.waitForURL(/\/orgs\//, { timeout: 10000 });
      expect(page.url()).not.toContain("/invite/");
      expect(page.url()).not.toContain("/login");

      // Poll rather than read once: waitForURL resolves on the FIRST match,
      // and the app keeps navigating (org resolution, then the dashboard
      // route) for a moment afterwards. A bare page.evaluate racing that
      // teardown dies with "Execution context was destroyed", which is what
      // made this test fail in CI while passing locally.
      await expect
        .poll(
          async () => {
            try {
              return await page.evaluate(() =>
                window.localStorage.getItem("solidping_session_token")
              );
            } catch {
              return null;
            }
          },
          { timeout: 10_000 }
        )
        .toBeTruthy();
    });

    test("a bogus token shows the invalid invitation card", async ({
      page,
    }) => {
      await page.goto("invite/this-token-does-not-exist-at-all");
      await page.waitForLoadState("networkidle");

      await expect(
        page.getByText("This invitation link is invalid or has expired.")
      ).toBeVisible();

      // Must not be mistaken for the transient/retryable error state added
      // alongside this coverage.
      await expect(page.getByText("Temporary error")).not.toBeVisible();
    });
  });
});
