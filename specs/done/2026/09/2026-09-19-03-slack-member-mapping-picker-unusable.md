---
model: sonnet
effort: medium
---

# The Slack member-mapping picker is a dead control: matched rows read "Pick a person…" and nobody can be picked when the workspace list is unavailable

## Problem

On `https://solidping.k8xp.com/d/orgs/acme/integrations/b69c17d8-4dc7-483e-841c-3998051ed8f4`
(the org's only Slack integration, `isDefault: true`), the **Member mapping**
card says "7 matched · 3 not mapped", every row carries a **Matched** or
**Not found** badge, and yet every row's combobox reads **"Pick a person…"**.
Opening any of them shows "No people found". An admin can neither pick anyone
nor see who a "Matched" row is actually mapped to.

### What the dev API answers today (v0.29.1, commit `92991bf6`)

```
GET /api/v1/orgs/acme/integrations/b69c17d8-…/identities        → 200
  9 rows; the 7 "matched" ones carry externalId + displayName + source=auto
GET /api/v1/orgs/acme/channels/b69c17d8-…/slack/destinations      → 409
  {"code":"CHANNEL_NOT_CONNECTED","title":"Slack channel is not connected — install the Slack app"}
GET /api/v1/orgs/acme/integrations/b69c17d8-…                     → 200
  "settingsPrivateKeys": ["access_token"]   ← the token is in settings_private
```

The bot **is** installed (the identity sync matched 7 people through it). The
409 comes from the deployed `GetDestinations`, which parses the bot token out of
the *public* `conn.Settings` map only (`models.SlackSettingsFromJSONMap(conn.Settings)`
at the deployed `service.go:1290`) while the token has been split into the
encrypted `settings_private` envelope. Empty token → `ErrSlackNotConnected` → 409.

**That backend half is already covered** by spec `2026-09-18-02-slack-bot-token-encrypted-settings`
and is implemented on this batch branch: `GetDestinations` now goes through
`slack.BotToken` ([`server/internal/integrations/slack/service.go:1346`](../../server/internal/integrations/slack/service.go),
helper in [`token.go:40`](../../server/internal/integrations/slack/token.go)),
with `TestGetDestinations_SplitRow` in
[`token_test.go:102`](../../server/internal/integrations/slack/token_test.go)
proving the picker works on a split row. Dev keeps answering 409 until that
ships. This spec does **not** redo any of it.

### What is not covered anywhere: the dashboard degrades silently

Once the destinations fetch fails (or the list simply lacks an id), the
member-mapping card makes the state look worse than it is and gives no way
to recover or even understand it. All in
[`web/dash0/src/components/integrations/integration-form.tsx`](../../web/dash0/src/components/integrations/integration-form.tsx):

1. **The stored mapping is never shown.** `SlackMemberMapping` renders
   `SlackUserCombobox` with `users={workspaceUsers}` and
   `currentId={identity.externalId ?? ""}` (`:1997-2001`). The combobox
   label is `users.find(u => u.id === currentId)` or else "Pick a person…"
   (`:2216-2218`). `identity.displayName` (which the API returns for every
   matched/manual row) is never used, so a row can be **Matched** and read
   **Pick a person…** in the same breath. The same happens with a healthy
   list whenever a mapped user is no longer in it (deactivated, or a guest
   that `users.list` does not return): the mapping silently looks empty.

2. **The card does not know the list failed.** `SlackPanel` passes only
   `workspaceUsers={data?.users ?? []}` (`:1749-1753`), never the query's
   `isLoading` / `isError`. A failed fetch and an empty workspace are
   indistinguishable: the picker opens on "No people found" (`:2248`) and
   stays fully interactive although it can do nothing.

3. **The panel-level error gives the wrong advice.** On any destinations
   error the channel picker area shows the fixed copy *"Could not connect to
   Slack workspace — re-install the bot."* (`:1724-1729`). For a 409 / 502
   caused by the server failing to read a perfectly good token, re-installing
   is wrong and would not help. The API already sends a precise `title`.

## Proposal

Frontend only (the backend fix rides on `2026-09-18-02`). Keep the
`SlackMemberMapping` / `SlackUserCombobox` shapes; extend them.

### 1. Label precedence in `SlackUserCombobox`

Add an optional `fallbackLabel?: string` prop. Trigger label precedence:

1. `@{realName || name}` of the workspace user whose `id === currentId`;
2. else, when `currentId` is set and `fallbackLabel` is set: `@{fallbackLabel}`
   (not muted — it is a real selection);
3. else "Pick a person…" (muted, as today).

`SlackMemberMapping` passes `fallbackLabel={identity.displayName}`. A
**Matched** / **Manual** row therefore always shows who it is mapped to,
whether or not the workspace list is available. When the list *is* loaded
and lacks the id, keep the name and add a small muted hint under it
(`form.slackMappingNotInWorkspaceList`, "Not in the workspace member list")
so an admin knows the picker cannot re-select that person.

### 2. Surface the destinations query state in the card

Give `SlackMemberMapping` two new props, `workspaceUsersLoading: boolean` and
`workspaceUsersError?: string` (the `err.message` of the destinations query,
which carries the API `title` the same way the mapping `onError` at `:2011`
already relies on). `SlackPanel` passes them from `useSlackDestinations`.

- **Loading**: each row's combobox trigger is `disabled` and shows a
  `Loader2`; the stored name still shows through rule 1.
- **Error**: render one destructive line at the top of the list,
  `form.slackMappingUsersError` — "Workspace members could not be loaded:
  {{reason}}" — and render every row's combobox trigger `disabled`. The
  status badges, counts, stored names, the **Clear** trash button and
  **Re-sync** stay as they are (re-sync is the natural retry; on its success
  invalidate the `["slack-destinations", org, channelUid]` query so the
  pickers come back without a reload).
- Wrap the disabled trigger's label in the standard tooltip pattern from the
  design reference if one exists; otherwise the inline error line is enough.

### 3. Use the API's own message at the panel level

Replace the fixed "re-install the bot" copy at `:1724-1729` with
`err.message` when it is non-empty (409 → "Slack channel is not connected —
install the Slack app", 502 → "Could not connect to Slack workspace"), falling
back to the existing `form.slackError` string otherwise.

### 4. Locales

Every new key (`form.slackMappingNotInWorkspaceList`,
`form.slackMappingUsersError`, anything else added) goes into **all six**
`web/dash0/src/locales/*/integrations.json`; `bun run test:unit` must stay
green (a missing key in one locale fails it).

### 5. Tests

Extend [`web/dash0/e2e/slack-member-mapping.spec.ts`](../../web/dash0/e2e/slack-member-mapping.spec.ts)
(it already stubs the destinations route at `:71`):

- **Destinations 409**: matched rows still show `@<displayName>` (not
  "Pick a person…"), the card shows the error line containing the API
  title, every `slack-user-combobox` is disabled, counts unchanged, the
  clear button on a matched row is still enabled.
- **Destinations 200 but the list lacks a matched `externalId`**: the row
  shows `@<displayName>` plus the "Not in the workspace member list" hint;
  opening the picker still lists the users that *are* there.
- **Panel-level copy**: with the 409 stub the channel area shows the API
  title, not "re-install the bot".
- The existing "manual override PUTs the picked workspace user" test keeps
  passing (rule 1 must not change the healthy path).

No backend change and no new Go test; if the implementer finds
`TestGetDestinations_SplitRow` missing from the branch, that is spec
`2026-09-18-02`'s gate, not this one.

### Verification after both specs ship to dev

```
curl -s -H "Authorization: Bearer $KEY" \
  https://solidping.k8xp.com/api/v1/orgs/acme/channels/b69c17d8-…/slack/destinations | jq '.users | length'
```

must be > 0, and the page must show the seven matched names in their
comboboxes with the pickers enabled.
