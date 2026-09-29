---
model: sonnet
effort: medium
---

# Discord member mapping shows a red "Could not load" until the channel is saved

## Problem
Max (max@riskless.id, email of 2026-09-28: "member mapping isn't working properly") hit this on a
Discord integration. After installing the bot and picking a channel, the "Member mapping" card
is shown right away, but it renders in red: "Could not load the member mapping." and Re-sync
does nothing useful until the user clicks "Save changes".

Cause: the card is rendered from the local, unsaved form state
(`web/dash0/src/components/integrations/integration-form.tsx:1477`, `org && channelUid && currentId`,
where `currentId` is the form's `channel_id` at `:1295`). It then calls the identities endpoints, which
answer an error for an integration whose stored settings do not use the bot yet
(`server/internal/handlers/integrations/identities.go:158`, `supportsIdentities` -> `UsesBot()`).
The error path renders `text-destructive` (`integration-form.tsx:~2200`, key `form.slackMappingError`).
Nothing tells the user the fix is to save.

An unsaved state is not an error. It must not be red.

## Proposal
1. In `DiscordDestinationPanel` (`integration-form.tsx:~1477`), detect "destination not saved yet":
   compare the form's `channel_id` / `dm_user_id` (and bot-mode) with the saved values
   (the `initial` settings passed into `IntegrationForm`, `:111`). Thread a `dirty`/`savedSettings` prop down if needed.
2. When not saved, do not mount the identities queries. Render the Member mapping card in a neutral
   style (`text-muted-foreground`, no `text-destructive`) with the Re-sync button disabled and this text:
   "Save your changes first. Member mapping and re-sync become available once the destination is saved."
3. Once saved, mount `SlackMemberMapping` as today. Keep the red error for a real load failure
   on a saved integration (`:~2200`).
4. Add the new string to all four locales: `web/dash0/src/locales/{en,fr,de,es}/integrations.json`
   (next to `form.slackMappingError`, `en:142`). Suggested key: `form.discordMappingSaveFirst`.
5. Check the Slack variant of the same card (`:1983`) for the same unsaved-channel problem and apply
   the same neutral message if it applies. Do not change Slack behaviour otherwise.
6. Add the shipped pattern to the design reference page if a new "notice" style is introduced
   (`web/dash0/src/routes/orgs/$org/design-reference.tsx`); otherwise reuse the existing muted text/alert.

## Tests
- `web/dash0/e2e/` (Discord integration spec, or a new one): open a Discord integration, select a channel
  without saving. Expect the save-first hint visible, no `text-destructive` error, Re-sync disabled,
  and no request to `/integrations/:uid/identities`.
- Same spec: click "Save changes". Expect the hint gone, the mapping list (or the "nobody linked" empty
  state, `data-testid="discord-mapping-nobody-linked"`) shown, Re-sync enabled.
- Negative: on a saved integration, when the identities endpoint returns 500, the red
  "Could not load the member mapping." still shows.
- dash0 unit test (`bun run test:unit`) if the "is unsaved" comparison is extracted into a helper:
  equal settings -> saved, changed `channel_id` -> unsaved, DM tab -> compares `dm_user_id`.

## To verify
- Exact status/code the identities endpoint returns for an unsaved bot integration (expected `ErrIdentitiesUnsupportedType`).
  A backend 4xx with a clear code could also let the UI distinguish this case without client-side dirty tracking.
- Whether the error appears only for the first save after install, or also when switching channel/DM tab on an already-saved integration.
- Whether `initial` in `IntegrationForm` is refreshed after a successful save (so the card flips without a reload).
