import { test, expect } from "./fixtures";

const API_BASE = "http://localhost:4000";

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
});
