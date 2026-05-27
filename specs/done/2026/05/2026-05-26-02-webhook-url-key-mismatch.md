# Fix: Webhook URL Key Mismatch (frontend `webhook_url` vs backend `url`)

## Problem

Webhook channel notifications always fail with **"webhook url not configured"** even when the URL
is visibly set in the channel edit form.

Root cause: the frontend stores the URL under the key `webhook_url`; the backend notification
sender reads it from the key `url`. These have never matched.

### Evidence

- Incident notification audit row: `Status: Failed`, `Error: webhook url not configured`
- "Send test" button on the channel edit page shows the same error immediately (0 ms).
- The URL _is_ displayed in the edit form because `webhook_url` is stored unencrypted in the
  public `Settings` JSONB (it is not in the `connectionSecretFields` registry for
  `ConnectionTypeWebhook`, so it bypasses split-and-encrypt entirely).

### Code pointers

| Location | Key used | Role |
|---|---|---|
| `web/dash0/src/components/channels/channel-form.tsx:137-138` | `webhook_url` | reads and writes the URL field for `case "webhook"` |
| `server/internal/notifications/webhook.go` (the `Send` func) | `url` | reads the URL to send the HTTP request |
| `server/internal/crypto/credentials/conn_secrets.go` | `url` | declared secret key for `ConnectionTypeWebhook` |

Discord / Mattermost / GoogleChat correctly use `webhook_url` both in the form and in their
backends — the mismatch is specific to the generic `webhook` type.

## Fix

### 1. Frontend: rename the key

In `channel-form.tsx`, change the `case "webhook"` branch to read/write `url` instead of
`webhook_url`:

```diff
-value={(settings.webhook_url as string) || ""}
-onChange={(v) => update("webhook_url", v)}
+value={(settings.url as string) || ""}
+onChange={(v) => update("url", v)}
```

After this fix the frontend will send `{ settings: { url: "https://…" } }` in PATCH/POST, which
the backend encryption pipeline will split into `SettingsPrivate` (since `url` is a declared secret
key) and the notification sender will read correctly.

### 2. DB migration: copy `webhook_url` → `url` for existing channels

Existing webhook channels have the URL in the unencrypted public `settings` column under
`webhook_url`. Add a SQL migration that:

1. Copies `settings->>'webhook_url'` to `settings->'url'` for all
   `integration_connections` rows with `type = 'webhook'` and a non-empty `webhook_url`.
2. Removes the now-redundant `webhook_url` key from `settings`.

```sql
-- migrate: up
UPDATE integration_connections
SET settings = (settings - 'webhook_url') || jsonb_build_object('url', settings->>'webhook_url')
WHERE type = 'webhook'
  AND settings ? 'webhook_url'
  AND settings->>'webhook_url' <> '';
```

After migration the URL lives in the plaintext `settings` column under `url`. On the next `GET`
(or manual save) the standard PATCH pipeline will split-and-encrypt it into `settings_private`.

### 3. (Optional) Backend fallback for zero-downtime

If a phased rollout is needed, `WebhookSender.Send` can fall back to `webhook_url` when `url` is
absent:

```go
url, ok := payload.Connection.Settings["url"].(string)
if !ok || url == "" {
    url, ok = payload.Connection.Settings["webhook_url"].(string)
}
if !ok || url == "" {
    return ErrWebhookURLNotConfigured
}
```

This is **not required** if the migration and the frontend fix ship together in the same release.
Include it only if there is a risk of the migration running before the frontend is deployed.

## Testing

- Create a new webhook channel, set a URL, save. Trigger an incident. Assert the notification is
  delivered.
- Edit an existing channel (migrated), save without touching the URL. Assert the URL is preserved
  and a notification is delivered.
- "Send test" on a freshly-created webhook channel returns success (not the old
  "webhook url not configured").
- Unit test: add a `TestWebhookSender_URLKeyResolution` case that asserts `Send` works when
  `Settings["url"]` is set, and returns `ErrWebhookURLNotConfigured` when it is absent.

## Non-goals

- Changing Discord / Mattermost / GoogleChat — they correctly use `webhook_url` everywhere.
- Retrying previously-failed notifications.

## Implementation Plan

### Step 1 — Frontend: rename `webhook_url` → `url` for `case "webhook"`

In `web/dash0/src/components/channels/channel-form.tsx` lines 137-138, change the `webhook`
branch to read/write `url` instead of `webhook_url`.  Discord/Mattermost/GoogleChat branches
are untouched — they use `webhook_url` intentionally.

### Step 2 — DB migration: back-fill existing webhook channel rows

Add migration `033_webhook_settings_url_key.up.sql` (and matching `.down.sql`) for both
`server/internal/db/postgres/migrations/` and `server/internal/db/sqlite/migrations/`.

The UP migration moves `settings->>'webhook_url'` → `settings->'url'` for all
`integration_connections` rows where `type = 'webhook'` and `webhook_url` is present and
non-empty, then strips the old key.

The DOWN migration is a no-op (reverting would require re-adding `webhook_url` but the data
is still present under `url`; left intentionally blank for safety).

### Step 3 — Unit test: `TestWebhookSender_URLKeyResolution`

Add a test in `server/internal/notifications/webhook_test.go` that:
- Asserts `Send` succeeds when `Settings["url"]` is set.
- Asserts `Send` returns `ErrWebhookURLNotConfigured` when neither key is present (already
  covered by `TestWebhookSender_Send_MissingURL`, but an explicit named test is required by
  the spec).
