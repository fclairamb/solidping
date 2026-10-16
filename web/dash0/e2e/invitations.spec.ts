import { test, expect, API_BASE, DASH_BASE } from "./fixtures";

test.describe("Invitations", () => {
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
      new RegExp(`${DASH_BASE}/invite/`)
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

    // Spec 2026-10-16-06: an invitation for an existing account is accepted
    // only signed in as that account. The account here has left every org,
    // the case an invitation exists to bring back, so its login takes the
    // zero-org branch, which must still resume the invitation.
    test("logged-out existing user is sent to login, lands back on the invite and accepts", async ({
      authenticatedPage,
      page,
      playwright,
    }) => {
      const stamp = Date.now();
      const email = `invite-existing-${stamp}@example.com`;
      const password = "Existing-Pass-123!";

      const invite = async () => {
        const resp = await authenticatedPage.request.post(
          `${API_BASE}/api/v1/orgs/test/invitations`,
          { data: { email, role: "user", expiresIn: "24h", app: "dash0" } }
        );
        expect(resp.status()).toBe(201);
        return (await resp.json()).token as string;
      };

      // Create the account through a first invitation, from a request
      // context of its own so no session cookie reaches `page`.
      const anon = await playwright.request.newContext();
      try {
        const created = await anon.post(`${API_BASE}/api/v1/auth/accept-invite`, {
          data: { token: await invite(), name: "Existing", password },
        });
        expect(created.status()).toBe(200);
      } finally {
        await anon.dispose();
      }

      // Then make it leave the org.
      const membersResp = await authenticatedPage.request.get(
        `${API_BASE}/api/v1/orgs/test/members`
      );
      expect(membersResp.status()).toBe(200);
      const { data: members } = (await membersResp.json()) as {
        data: { uid: string; email: string }[];
      };
      const member = members.find((m) => m.email === email);
      expect(member).toBeTruthy();
      const removed = await authenticatedPage.request.delete(
        `${API_BASE}/api/v1/orgs/test/members/${member!.uid}`
      );
      expect(removed.status()).toBe(204);

      const token = await invite();

      // The create-account form refuses an existing account: the visitor is
      // sent to log in, with a notice instead of an error.
      await page.goto(`invite/${token}`);
      await page.locator("#password").fill("Any-Other-Pass-1!");
      await page.getByRole("button", { name: "Create account & join" }).click();

      await page.waitForURL(/\/login/, { timeout: 10000 });
      await expect(page.getByTestId("login-invite-notice")).toBeVisible();
      expect(decodeURIComponent(page.url())).toContain(`/invite/${token}`);

      await page.locator("#email").fill(email);
      await page.locator("#password").fill(password);
      await page.getByTestId("login-submit").click();

      // Back on the invitation, signed in.
      await page.waitForURL(new RegExp(`/invite/${token}$`), { timeout: 10000 });
      await expect(page.getByText("You're already signed in")).toBeVisible();

      await page.getByRole("button", { name: "Accept Invitation" }).click();
      await page.waitForURL(/\/orgs\/test/, { timeout: 10000 });
      expect(page.url()).not.toContain("/invite/");
    });

    test("signed in as another account sees the mismatch card", async ({
      authenticatedPage,
    }) => {
      const page = authenticatedPage;
      const email = `invite-mismatch-${Date.now()}@example.com`;

      const createResp = await page.request.post(
        `${API_BASE}/api/v1/orgs/test/invitations`,
        { data: { email, role: "user", expiresIn: "24h", app: "dash0" } }
      );
      expect(createResp.status()).toBe(201);
      const { token } = await createResp.json();

      await page.goto(`invite/${token}`);
      await page.getByRole("button", { name: "Accept Invitation" }).click();

      const card = page.getByTestId("invite-email-mismatch");
      await expect(card).toBeVisible();
      await expect(card).toContainText("you are signed in as test@test.com");
      await expect(
        card.getByRole("button", { name: "Sign out and continue" })
      ).toBeVisible();
      // Positive control: still on the invitation, nothing was accepted.
      expect(page.url()).toContain(`/invite/${token}`);
    });

    test("a bogus token shows the invalid invitation card", async ({
      page,
    }) => {
      await page.goto("invite/this-token-does-not-exist-at-all");

      await expect(
        page.getByText("This invitation link is invalid or has expired.")
      ).toBeVisible();

      // Must not be mistaken for the transient/retryable error state added
      // alongside this coverage.
      await expect(page.getByText("Temporary error")).not.toBeVisible();
    });
  });
});
