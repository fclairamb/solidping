# Remove "From address" field from email notification channel form

## Context

The email notification channel edit page (`/dash0/orgs/$org/channels/$uid`) renders a "From address" input that lets each channel configure its own sender address. This field is **dead code with two problems**:

1. **It does nothing.** The user-entered value lands in `IntegrationConnection.Settings["from"]` (`web/dash0/src/components/channels/channel-form.tsx:115-124`) but `EmailSender.Send` (`server/internal/notifications/email.go:67-115`) only reads `recipients` and `subject_prefix` from the connection settings. The actual SMTP envelope `From` is set by `SMTPSender.setFrom` (`server/internal/email/sender.go:91-102`) from `config.Email.From` / `config.Email.FromName`, which are loaded from the system parameters `email.from` / `email.from_name` (`server/internal/systemconfig/systemconfig.go:35-36,194-211`) — i.e. the global admin-only fields shown on `/dash0/orgs/$org/server.mail` (`server.mail.tsx:285-305`).
2. **It would be a footgun if wired up.** Per-channel sender addresses break SPF/DKIM/DMARC alignment with whatever envelope/return-path the configured SMTP relay is authorised for. Anyone could type `noreply@whitehouse.gov`, the message would either be rejected outright or silently fail authentication on the recipient side. The sender address is correctly an instance-level concern, not a per-channel one.

So the right move is to remove the per-channel input — both because it currently lies to the user, and to prevent it from being "fixed" later in the wrong direction.

## Scope

**In scope**

- Remove the "From address" input and its surrounding wrapper from the email branch of `channel-form.tsx`.
- Remove the `form.fromAddress` translation key from all four locale files (`en`, `fr`, `es`, `de`).
- Stop persisting `from` in `settings` for new email connections (drops out automatically once the input is gone — no defensive code needed).
- Strip `from` from any existing email-channel connection rows on read or via a one-shot cleanup, so the saved-but-ignored value does not linger and re-appear if a future feature reads the key. **Recommended approach:** a tiny idempotent SQL migration that does
  ```sql
  UPDATE integration_connections
     SET settings = settings - 'from'
   WHERE type = 'email' AND settings ? 'from';
  ```
  Add it as a regular Bun migration in `server/internal/migrations/` — it's safe (drops a key that nothing reads) and removes the cleanup from future code paths.

**Out of scope**

- The system-level "From address" / "From name" inputs on `/server.mail` — those are the *correct* place to set the sender and stay as-is.
- Any change to `EmailSender.Send`, `SMTPSender.setFrom`, or `config.Email.From` — they already do the right thing.
- Encryption-at-rest plumbing for the email channel — `from` was never a secret field (`crypto/credentials/conn_secrets.go:29` only marks `smtp_password`), so nothing in `*_private` needs touching.
- Per-channel sender override as a "real" feature — explicitly *not* what we want.

## Approach

### 1. Frontend: drop the input

`web/dash0/src/components/channels/channel-form.tsx`, in the `case "email":` branch (lines 112-150), delete the first `<div className="space-y-2">` block (lines 115-124) containing the `ch-from` Label + Input. Keep the outer `<div className="space-y-3">` and the recipients block. Result: the email channel form shows only "Recipients (one per line)".

### 2. Translations: drop the key

In each of `web/dash0/src/locales/{en,fr,es,de}/channels.json`, remove the `"fromAddress": "..."` line under `"form"` (currently line 65 in each file). Re-check that no other component references `t("channels:form.fromAddress")` — quick grep confirms only `channel-form.tsx` uses it, so this is safe.

### 3. Data cleanup migration

Add `server/internal/migrations/NNN_drop_email_connection_from_setting.sql` (next migration number) with the `UPDATE … settings - 'from'` shown above plus an empty `down` (the field is meaningless to restore). The migration is idempotent — re-runs are no-ops once the key is gone.

### 4. Tests

- Add a `web/dash0/e2e/channel-form-email.spec.ts` (or extend an existing one) asserting that the email channel edit form has **no** input with id `ch-from` and exactly one visible Recipients textarea.
- Backend test: `server/internal/handlers/connections/service_test.go` — extend the create/update email-connection coverage to assert that submitting `settings: {"from": "x@y", "recipients": [...]}` either silently drops `from` or, if we just leave it through (no client will send it post-frontend-change), at least documents the current behaviour. Cheapest acceptable path: no new backend test, since the only behaviour change is on the frontend and the migration is mechanical.

### 5. Manual verification (`make dev`)

1. Open an existing email channel (the one in the screenshot, `0f1f586a-9939-47d9-b175-0b6849be0328`) — confirm "From address" is gone, "Recipients" is unchanged.
2. Save the channel without other edits — `GET` the connection, confirm `settings.from` is absent (post-migration) and the channel still works (trigger a test alert; verify the recipient sees a message with the system-configured From).
3. Switch the dashboard locale to French / Spanish / German and confirm no missing-translation warning appears in the console.
4. Run the migration on a fresh DB and on a DB that already has email channels with `from` set; both should end up with no `from` key in `integration_connections.settings`.

## Risks and follow-ups

- **Visible config loss.** If any user *thought* the field was working and relied on the stored value showing up in their inbox, they'll notice the From address never matched anyway — but it's worth a one-line release note: "Per-channel sender addresses have been removed; configure the global sender at Settings → Mail."
- **Future cleanups.** If we ever do want per-channel sender addresses (e.g. for users who run multiple authenticated SMTP relays), the right shape is a per-channel SMTP credential set, not a free-form From string. Out of scope here, but worth noting in the spec rationale so the next person doesn't re-add a free-form input.
