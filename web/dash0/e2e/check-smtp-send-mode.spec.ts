import { test, expect, API_BASE, type Page } from "./fixtures";

// Coverage for spec 2026-08-19-04: send-mode SMTP checks submit a real probe
// email, addressed to a paired email check's tokenized address.
//
// The things worth proving end-to-end, none of which the vitest unit suite
// for the form module (mail.test.ts) reaches:
//   - the send-mode toggle reveals Mail From + the same-org email-check
//     picker, and creating a check through the real form stores
//     send_email/mail_from/delivery_check_uid;
//   - the SMTP check's detail page links to its paired delivery check;
//   - the email check's detail page lists the SMTP check(s) that deliver to
//     it — the other side of the same pairing.
//
// Modeled on check-ssh-tunnel.spec.ts (picker UI + paired-check detail links).

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

// ensureEmailInboxConfigured seeds the email.inbox system parameter so
// write-time validation (validateSMTPDeliveryConfig) accepts send mode.
// test@test.com is seeded as SuperAdmin (server/test/testdata/testdata.go),
// which is what this system-parameters endpoint requires. No connectivity is
// exercised by this save — that is the separate POST /system/email-inbox/test
// action — so a fake session URL is fine for this test's purposes.
async function ensureEmailInboxConfigured(page: Page, token: string) {
  const resp = await page.request.put(
    `${API_BASE}/api/v1/system/parameters/email.inbox`,
    {
      headers: { Authorization: `Bearer ${token}` },
      data: {
        value: {
          enabled: true,
          sessionUrl: "https://jmap.e2e-test.invalid/session",
          username: "e2e@e2e-test.invalid",
          password: "not-a-real-password",
          addressDomain: "inbox.e2e-test.invalid",
        },
      },
    },
  );
  expect(resp.status()).toBe(200);
}

async function createEmailCheck(
  page: Page,
  token: string,
  name: string,
): Promise<{ uid: string; slug: string }> {
  const resp = await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
    headers: { Authorization: `Bearer ${token}` },
    data: {
      name,
      slug: name.toLowerCase().replace(/[^a-z0-9]+/g, "-"),
      type: "email",
      config: {},
    },
  });
  expect(resp.status()).toBe(201);
  const body = await resp.json();
  return { uid: body.uid, slug: body.slug };
}

async function getCheck(page: Page, token: string, uid: string) {
  const resp = await page.request.get(
    `${API_BASE}/api/v1/orgs/test/checks/${uid}`,
    { headers: { Authorization: `Bearer ${token}` } },
  );
  expect(resp.status()).toBe(200);
  return await resp.json();
}

test.describe("SMTP send mode", () => {
  test("toggle reveals mail_from + delivery picker; creating stores send-mode config", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    await ensureEmailInboxConfigured(page, token);

    const deliveryName = `E2E Delivery Check ${Date.now()}`;
    const deliveryCheck = await createEmailCheck(page, token, deliveryName);

    await page.goto("orgs/test/checks/new?checkType=smtp");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    // Send mode is off by default: mail_from / picker are not shown yet.
    await expect(page.getByTestId("check-smtp-mail-from-input")).toHaveCount(0);
    await expect(page.getByTestId("check-smtp-delivery-select")).toHaveCount(0);

    const checkName = `E2E Send Mode ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page.getByTestId("check-host-input").fill("mail.e2e-test.invalid");

    await page.getByTestId("check-smtp-send-email-switch").click();
    await expect(page.getByTestId("check-smtp-mail-from-input")).toBeVisible();

    await page
      .getByTestId("check-smtp-mail-from-input")
      .fill("prober@e2e-test.invalid");

    await page.getByTestId("check-smtp-delivery-select").click();
    await page
      .getByRole("option", { name: deliveryName, exact: true })
      .click();

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    const created = await getCheck(page, token, uid);
    expect(created.config.send_email).toBe(true);
    expect(created.config.mail_from).toBe("prober@e2e-test.invalid");
    expect(created.config.delivery_check_uid).toBe(deliveryCheck.uid);

    // ── The SMTP check's detail page links to its paired delivery check ──
    await expect(page.getByTestId("check-smtp-delivery-via")).toBeVisible();
    await expect(page.getByTestId("check-smtp-delivery-via")).toContainText(
      deliveryName,
    );

    // ── The email check's detail page lists the SMTP check delivering to it ──
    await page.goto(`orgs/test/checks/${deliveryCheck.uid}`);
    await page.waitForLoadState("networkidle");
    const sources = page.getByTestId("check-smtp-delivery-sources");
    await expect(sources).toBeVisible();
    await expect(sources).toContainText(checkName);

    // ── Editing the check keeps send mode on and the picker selected ──
    await page.goto(`orgs/test/checks/${uid}/edit`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-smtp-send-email-switch")).toBeVisible();
    await expect(page.getByTestId("check-smtp-mail-from-input")).toHaveValue(
      "prober@e2e-test.invalid",
    );
    await expect(page.getByTestId("check-smtp-delivery-select")).toContainText(
      deliveryName,
    );
  });

  test("empty-state shows plain text with no check-creation affordance", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Force the picker's empty state regardless of how many email checks
    // already exist in the shared "test" org: stub the exact query the
    // picker's candidate list depends on (`useChecks(org, { type: "email" })`)
    // to return zero results, deterministically.
    await page.route(
      /\/api\/v1\/orgs\/test\/checks\?[^#]*type=email/,
      (route) =>
        route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({ data: [] }),
        }),
    );

    await page.goto("orgs/test/checks/new?checkType=smtp");
    await page.waitForLoadState("networkidle");
    await page.getByTestId("check-smtp-send-email-switch").click();

    // Per the spec's resolved open question: no "create a delivery check for
    // me" link or button anywhere in this form's send-mode section — plain
    // text only. Scoped to the section itself: the page has an unrelated
    // "Create Check" nav affordance elsewhere that a page-wide query would
    // false-positive on.
    const section = page.getByTestId("check-smtp-send-mode-section");
    await expect(page.getByTestId("check-smtp-delivery-select")).toHaveCount(0);
    await expect(
      section.getByText("No email checks in this organization yet."),
    ).toBeVisible();
    await expect(section.getByRole("link")).toHaveCount(0);
    await expect(section.getByRole("button", { name: /create/i })).toHaveCount(0);
  });
});
