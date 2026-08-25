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
});
