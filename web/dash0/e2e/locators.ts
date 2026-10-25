import type { Locator, Page } from "@playwright/test";

// Locators for the checks index, keyed on data attributes instead of text so
// specs never depend on copy (see AGENTS.md, "Locators: getByTestId").

/** The name cell of a check row (checks index tables), matched by exact name. */
export function checkNameLocator(scope: Page | Locator, name: string): Locator {
  return scope.locator(
    `[data-testid="check-row-name"][data-check-name=${JSON.stringify(name)}]`,
  );
}

/** The title of a check group section on the checks index, matched by exact name. */
export function groupNameLocator(scope: Page | Locator, name: string): Locator {
  return scope.locator(
    `[data-testid="group-name"][data-group-name=${JSON.stringify(name)}]`,
  );
}

/** The check-name line of an active incident on the dashboard, matched by exact check name. */
export function activeIncidentLocator(page: Page, checkName: string): Locator {
  return page
    .getByTestId("active-incidents")
    .locator(
      `[data-testid="active-incident-check-name"][data-check-name=${JSON.stringify(checkName)}]`,
    );
}

/** The row of a check on the checks index, matched by exact check name. */
export function checkRowLocator(page: Page, name: string): Locator {
  return page.getByTestId("check-row").filter({ has: checkNameLocator(page, name) });
}
