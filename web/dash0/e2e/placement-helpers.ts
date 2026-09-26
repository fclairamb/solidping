import { expect, type Page } from "@playwright/test";

/**
 * A new check is placed automatically by default (spec 2026-09-25-06): the
 * region picker shows "Automatic (N regions) · Choose regions" and hides the
 * per-region checkboxes. A spec that wants to pick regions by hand clicks
 * "Choose regions" first, which switches the form to the pinned picker.
 */
export async function choosePinnedRegions(page: Page): Promise<void> {
  const picker = page.getByTestId("check-regions-picker");
  await expect(picker).toBeVisible();

  if ((await picker.getAttribute("data-placement")) === "auto") {
    await page.getByTestId("check-placement-choose").click();
  }

  await expect(picker).toHaveAttribute("data-placement", "pinned");
}
