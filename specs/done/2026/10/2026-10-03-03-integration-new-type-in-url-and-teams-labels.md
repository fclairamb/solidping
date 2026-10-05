---
model: sonnet
effort: medium
---

# Integration "new" page keeps its type only in local state; Teams labels are ambiguous

## Problem
- `web/dash0/src/routes/orgs/$org/integrations.new.tsx:70` seeds `type` from `?type=` once (`useState(search.type ?? null)`), then `setType` only changes React state. Picking a type does not update the URL, so a reload or a shared link loses the selection, and the browser back button leaves the page instead of returning to the picker.
- The Cancel buttons in the form views (`integrations.new.tsx:~178`, `~210`, `~253`) link to the integrations list. The user expects Cancel to return to the type selection. (`FreeboxForm` already does this through `onCancel={() => setType(null)}` at `:190`.)
- `common:channels.msteams` and `common:channels.msteams-bot` (`web/dash0/src/locales/{en,fr,de,es}/common.json:70-71`) both read "Microsoft Teams", so the picker shows two identical tiles.

## Proposal
1. Make the URL the source of truth for the selected type in `integrations.new.tsx`: derive `type` from `Route.useSearch().type` and drop the `useState`. Picking a type calls `navigate({ search: { type: c } })` (push, so Back returns to the picker). Validate `search.type` against `ALL_TYPES` in `validateSearch` (`:36`) and drop unknown values so a bad `?type=` shows the picker.
2. Cancel in every form view navigates to `/orgs/$org/integrations/new` with no `type` (replace the three `<Link to="/orgs/$org/integrations">` buttons and the Freebox `onCancel`). Keep the Cancel on the picker itself going to the integrations list.
3. Rename the labels in `en/common.json`: `msteams` -> "Teams (webhook)", `msteams-bot` -> "Teams (bot)". Apply the same strings in `fr`, `de`, `es` (technical names, not translated). Update `web/dash0/src/locales/identical-allowlist.ts:97-98` accordingly.
4. Update the places that quote the old label: `web/dash0/e2e/integrations.spec.ts`, `web/dash0/e2e/channels-msteams-bot.spec.ts`, and `web/docs/docs/configuration/notifications.md` where it names the UI entry.

## Tests
- `web/dash0/e2e/integrations.spec.ts`: clicking `pick-slack` sets `?type=slack`; reloading keeps the form open; browser Back returns to the picker.
- Same file: Cancel on a form returns to the picker (URL has no `type`), Cancel on the picker goes to the integrations list.
- Same file: `?type=bogus` shows the picker, not a crash or empty form.
- Same file: the picker shows "Teams (webhook)" and "Teams (bot)" as two distinct tiles.
- Locale lint/typecheck (`make lint`) passes for the identical-allowlist change.

## To verify
- Whether `ConnectionType` has a runtime list to validate against beyond `ALL_TYPES`, and whether other routes link to `/integrations/new?type=` with a type missing from `ALL_TYPES` (e.g. bot-only types).
