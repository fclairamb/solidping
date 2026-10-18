---
model: haiku
effort: low
---

# Onboarding-checklist dismiss e2e reloads before the dismissal PUT lands

## Problem

`web/dash0/e2e/onboarding-checklist.spec.ts:378`, "dismissing survives a full reload, follows the user to another browser session, and is undone from the account page", is racy.

- `handleDismiss` (`web/dash0/src/components/dashboard/onboarding-checklist.tsx:202`) hides the card through local state (`setManuallyDismissed(true)`). It then fires `setDismissal.mutate(...)`, an async `PUT /api/v1/me/ui-state/<key>` (`useSetUiState`, `web/dash0/src/api/hooks.ts:9032`).
- The test clicks `onboarding-dismiss` (`:387`) and asserts the card is gone (`:388`). That is true at once because of the local state.
- It then calls `openDashboard` (`:394`, a `page.goto`) right away. If the PUT has not completed, the navigation aborts it or reloads before it lands. The card comes back: "Expected: 0, Received: 1" at `:395`.

Seen on 2026-10-18 against a SQLite side-car (slower, serialized writes). CI on Postgres usually wins the race.

Rest of the spec: the account-page restore (`:413-417`) is already safe. `account.profile.tsx:290` awaits `resetChecklist.mutateAsync()` before the "checklist is back" toast, and the test waits for that toast before navigating. No other test in the file writes ui-state and then navigates. The self-dismiss write (`onboarding-checklist.tsx:159`) is not followed by a reload in any test.

## Proposal

1. In `web/dash0/e2e/onboarding-checklist.spec.ts:387`, start waiting before the click and await it before the reload at `:394`:
   ```ts
   const dismissSaved = page.waitForResponse(
     (r) => r.url().includes("/api/v1/me/ui-state/") && r.request().method() === "PUT" && r.ok(),
   );
   await page.getByTestId("onboarding-dismiss").click();
   await expect(page.getByTestId("onboarding-checklist")).toHaveCount(0);
   await dismissSaved;
   ```
   Add a short comment: the card hides from local state, so its absence does not prove the write landed.
2. Do not touch the component. A user who reloads within a few ms of dismissing is not a product bug.

## Tests

- Start a side-car per `wiki/testing/local-e2e.md` (SQLite test mode on a port other than 4000; never stop the `:4000` devloop). Run `onboarding-checklist.spec.ts` with `--repeat-each=5 --workers=2 --retries=0`, several times. Every run green.
- Negative control: without the fix, the same command should reproduce the `:395` failure at least once on SQLite. If it never does, say so in the PR rather than claiming the race is proven.
- `bun run lint:e2e && bun run typecheck:e2e` in `web/dash0`.
- Ship on a `fix/` branch with a PR. Flaky tests are bugs: never re-run to green (AGENTS.md).

## To verify

- That the account-page toast really renders only after `resetChecklist.mutateAsync()` resolves (`account.profile.tsx:285-300`). If it shows before, apply the same `waitForResponse` pattern (matching that endpoint's method) before the `openDashboard` at `:417`.
