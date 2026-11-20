import { test, expect } from "@playwright/test";
import {
  API_BASE as BASE,
  STATUS_BASE,
  resolveDefaultStatusPage,
} from "./fixtures";

// A `?lang=` value that is not a language tag used to reach Intl during render,
// throw RangeError and blank the whole page (no error boundary). Anyone can
// craft such a link, so every visitor of it must still see the page.
test.describe("Public status page with an invalid ?lang=", () => {
  for (const lang of ["123", "en_", "a-b-c"]) {
    test(`?lang=${lang} still renders the page name and its checks`, async ({
      page,
    }) => {
      const target = await resolveDefaultStatusPage();
      const pageErrors: string[] = [];
      page.on("pageerror", (err) => pageErrors.push(err.message));

      await page.goto(
        `${BASE}${STATUS_BASE}/${target.org}/${target.slug}?lang=${encodeURIComponent(lang)}`,
      );
      await page.waitForLoadState("networkidle");

      await expect(page.getByText(target.name).first()).toBeVisible({
        timeout: 10000,
      });
      await expect(
        page.getByTestId("status-page-unavailable"),
      ).not.toBeVisible();
      expect(pageErrors.filter((m) => /Invalid language tag/.test(m))).toEqual(
        [],
      );
      // Falls back to English rather than keeping the invalid tag.
      await expect(page.locator("html")).toHaveAttribute("lang", "en");
    });
  }

  test("?lang=fr still renders the French labels (positive control)", async ({
    page,
  }) => {
    const target = await resolveDefaultStatusPage();

    await page.goto(
      `${BASE}${STATUS_BASE}/${target.org}/${target.slug}?lang=fr`,
    );
    await page.waitForLoadState("networkidle");

    await expect(page.getByText(target.name).first()).toBeVisible({
      timeout: 10000,
    });
    await expect(page.locator("html")).toHaveAttribute("lang", "fr");
    await expect(page.getByText("Propulsé par SolidPing").first()).toBeVisible();
  });
});
