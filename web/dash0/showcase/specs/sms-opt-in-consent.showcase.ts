import {
  test,
  expect,
  apiLogin,
  ensureCleanShowcaseOrg,
  applyShowcaseIdentity,
  restoreShowcaseIdentity,
  still,
  beat,
  uiLogin,
  SHOWCASE_ORG,
} from "../fixtures";

/**
 * Showcase capture: the SMS opt-in consent control.
 *
 * NOT a test. This exists because our A2P 10DLC campaign registration is
 * reviewed by people who cannot log in: the consent step lives behind
 * authentication, so carriers reject it as unverifiable (error 30909,
 * sub-code 30921). The still produced here is published on
 * solidping.io/legal/sms-opt-in as the evidence they can inspect.
 *
 * It follows from that purpose that this capture must never be hand-edited or
 * staged — a doctored consent screen shown to a carrier is a misrepresentation.
 * Re-run it whenever the disclosure wording moves, so the published image and
 * the shipped UI cannot drift apart.
 */
test("SMS opt-in consent disclosure", async ({ page }) => {
  const bootstrapToken = await apiLogin(page);
  const token = await ensureCleanShowcaseOrg(page, bootstrapToken);
  const previousUserName = await applyShowcaseIdentity(page, token);

  try {
    // This org has no Twilio integration, so the form also shows "No SMS
    // provider is configured for this organization yet". That is left in
    // frame deliberately: seeding a provider would mean posting credentials
    // the server verifies against the live Twilio API, and the alternative —
    // cropping the banner out — would be dressing up a screenshot that a
    // carrier is meant to rely on. The published page captions it instead.
    await uiLogin(page);

    await page.goto(`orgs/${SHOWCASE_ORG}/account/notifications`);
    await page.waitForLoadState("networkidle");

    // Open the "add a contact" form and switch it to the Phone type — the
    // disclosure is bound to that choice, not to the page.
    await page.getByTestId("add-contact-button").click();
    await beat(page, 600);
    await page.getByRole("button", { name: "Phone" }).click();

    const notice = page.getByTestId("sms-consent-notice");
    await expect(notice).toBeVisible();

    // Fail loudly if any of the four carrier-required disclosures went missing:
    // a green run with a non-compliant screen is the one outcome that would
    // actively mislead a reviewer.
    for (const required of [
      "Message frequency varies by incident",
      "Message and data rates may apply",
      "Reply STOP to cancel, HELP for help",
      "alerts by SMS from Webingenia",
    ]) {
      await expect(notice).toContainText(required);
    }

    await beat(page, 800);
    await still(page, "sms-opt-in-consent");
  } finally {
    await restoreShowcaseIdentity(page, token, previousUserName);
  }
});
