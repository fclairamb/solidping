import type { Page } from "@playwright/test";

// The check form groups less-common fields (slug, labels, group, dependencies,
// incident tracking, flapping, timeout) into collapsible sections that are
// collapsed by default on create. Expanding a section only when it is closed
// keeps helpers idempotent — a section that auto-opened because it already
// holds non-default values (e.g. on the edit page) is left untouched.
export async function expandSection(
  page: Page,
  triggerTestId: string,
): Promise<void> {
  const trigger = page.getByTestId(triggerTestId);
  await trigger.waitFor({ state: "visible" });
  if ((await trigger.getAttribute("data-state")) === "closed") {
    await trigger.click();
  }
}
