# Command Palette: "New Check" shortcut

## Goal

Add a **New Check** entry to the Command Palette so users can quickly navigate to the check creation form without leaving the keyboard.

## Behaviour

- Entry label: **"New Check"**
- Appears in the Command Palette regardless of the current page, as long as the user is inside an org context (i.e. `$org` is available in the route).
- Selecting the entry navigates to `/:org/checks/new`.
- The entry should include a suitable icon (e.g. `Plus` or `PlusCircle`) and a short description such as "Create a new monitoring check".

## Out of scope

- No changes to the check creation form itself.
- No keyboard shortcut beyond the palette entry.

## Implementation Plan

1. **Add the entry to `CommandMenu`** (`web/dash0/src/components/CommandMenu.tsx`)
   - Introduce a new `actions` group (separate from `pages` so it appears as its own section).
   - Add a "New Check" entry pointing to `/orgs/$org/checks/new` with the `PlusCircle` icon and a short description.
   - Render the description in muted text next to the title (similar to how check slugs are rendered today).
2. **Translations** — add `command.groupActions`, `command.newCheck`, and `command.newCheckDescription` keys in `web/dash0/src/locales/{en,fr,de,es}/nav.json`.
3. **E2E test** — extend `web/dash0/e2e/command-menu.spec.ts` with a test that opens the palette, selects "New Check" and asserts navigation to `/orgs/<org>/checks/new`.
4. **QA** — run `make build-backend build-client lint-back test` and Playwright if reasonable, then loop until green.
