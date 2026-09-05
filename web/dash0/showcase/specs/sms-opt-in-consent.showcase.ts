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
 * Showcase capture: the SMS opt-in flow, end to end.
 *
 * NOT a test. This exists because our A2P 10DLC campaign registration is
 * reviewed by people who cannot log in: the consent step lives behind
 * authentication, so carriers reject it as unverifiable (error 30909,
 * sub-code 30921). The stills produced here are published on
 * solidping.io/legal/sms-opt-in as the evidence they can inspect.
 *
 * A single frame of the disclosure was not enough — the campaign was rejected
 * a second time on the same field. Twilio's remediation for 30909 asks for "a
 * public URL with hosted screenshots or other proof that shows the full
 * consent experience", so this walks the whole path: the empty settings page,
 * the disclosure, the number typed in, the unverified contact, and the
 * verification round-trip that turns consent into something the user proved
 * they control.
 *
 * It follows from that purpose that these captures must never be hand-edited
 * or staged — a doctored consent screen shown to a carrier is a
 * misrepresentation. Re-run whenever the flow or its wording moves, so the
 * published images and the shipped UI cannot drift apart.
 *
 * ## No cursor overlay here, deliberately
 *
 * The create-http-check recording installs a synthetic pointer
 * (`installCursor()` in ../fixtures) because headless Chromium paints none and
 * a marketing video needs one. This capture must NOT: every pixel a carrier
 * reviewer inspects has to be the shipped UI, and an arrow we drew ourselves is
 * exactly the sort of embellishment that turns evidence into a mock-up. It is
 * opt-in for that reason — this file simply never calls it, and
 * `SHOWCASE_CURSOR=0` disables it globally as a second switch.
 *
 * (The 2x `deviceScaleFactor` the showcase project now records at is a
 * different matter: it changes the pixel density of the capture, not the
 * layout or a single word of the UI being evidenced.)
 */
test("SMS opt-in flow", async ({ page }) => {
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

    // 1. Where the user starts: notification settings, email only. Nothing has
    //    been asked of them, and no phone number is on the account.
    await beat(page, 600);
    await still(page, "sms-opt-in-1-settings");

    // 2. Open the "add a contact" form and switch it to the Phone type — the
    //    disclosure is bound to that choice, not to the page.
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
    await still(page, "sms-opt-in-2-consent");

    // 3. The number typed in, disclosure still on screen, Add not yet pressed.
    //    This is the frame that shows consent is read before it is given.
    const phone = "+15551234567";
    await page.getByTestId("add-contact-input").fill(phone);
    await beat(page, 600);
    await still(page, "sms-opt-in-3-number-entered");

    // 4. Submitted. The contact exists but is marked Unverified, and in that
    //    state it is never sent an alert.
    await page.getByTestId("add-contact-submit").click();
    await page.waitForLoadState("networkidle");
    const list = page.getByTestId("notification-routes-list");
    await expect(list.getByText("Phone (SMS)")).toBeVisible();
    await expect(list.getByText(/Unverified/)).toBeVisible();
    await beat(page, 800);
    await still(page, "sms-opt-in-4-unverified");

    // 5. The verification round-trip: a code to the handset, typed back in.
    //    Captured at the prompt rather than after sending — this org has no
    //    SMS provider, so no message would actually leave, and a frame
    //    implying one did would be a lie.
    await page.getByTestId(/^verify-phone-/).first().click();
    await expect(page.getByTestId("verify-phone-dialog")).toBeVisible();
    await beat(page, 800);
    await still(page, "sms-opt-in-5-verify");
  } finally {
    await restoreShowcaseIdentity(page, token, previousUserName);
  }
});
