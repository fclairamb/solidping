---
model: opus
effort: medium
---

# Make dashboard-created Pushover integrations deliver: one canonical settings key pair, encrypted, with a drift guard

GitHub Issue: https://github.com/fclairamb/solidping/issues/494

## Source

The owner traced a new user's session in production: they created a Pushover integration
from the dashboard and pressed **Send test** about 25 times in 10 minutes (3 rage clicks),
re-entering the token, toggling *Enabled*/*Default*, deleting and recreating it twice. Every
Pushover row in prod has settings keys `user,token` and an empty `settings_private_keys`. The
one test request still in the logs completed in 6.7 ms, too fast to have reached
`api.pushover.net`. The user left and later deleted their organization.

The report's diagnosis is right: three layers use three names for the same two settings.

## Problem

| Layer | Where | User key | API token |
|---|---|---|---|
| Dashboard form (writes) | `web/dash0/src/components/integrations/integration-form.tsx:573-589` | `user` | `token` |
| Sender (reads) | `server/internal/notifications/pushover.go:108-115` (`pushoverSettings`) | `userKey` | `apiToken` |
| Secret encryption list | `server/internal/crypto/credentials/conn_secrets.go:49` | `user_key` | `api_token` |

- **Delivery always fails.** `PushoverSender.parseSettings` (`pushover.go:117`) round-trips
  `payload.Integration.Settings` through JSON into `pushoverSettings`; `apiToken` is empty, so
  it returns `ErrPushoverAPITokenNotConfigured` (`pushover.go:25`) before any network call.
  This hits every "Send test" and every real notification.
- **Credentials stored in plaintext.** `SplitConfig` (`crypto/credentials/secret_fields.go:33`)
  moves only the keys listed in `conn_secrets.go` into `settings_private`. `user` and `token`
  are not listed, so they stay in `integrations.settings` in clear.
- **The error is shown as a delivery failure.** The test badge says "failed" without saying
  the integration is missing its API token, so the user kept retyping a correct token.
- The user-level Pushover contact (`UserContactTypePushover = "pushover_user"`,
  `server/internal/db/models/user_contact.go:15`) and the escalation-step sender
  (`server/internal/jobs/jobtypes/job_escalation_step.go`) must be checked for the same drift.
- The docs (`web/docs/docs/configuration/notifications.md:772-781`) name the fields "User Key"
  and "API Token" but not their setting keys.

## Why a spec

Existing rows need a migration that renames keys and moves secrets into `settings_private`
(encrypted, which a plain SQL migration cannot do: it needs the org's key, so it is a
one-shot job or a startup step). Correctness hinges on credential handling. It also adds a
cross-type CI guard.

## Proposal

1. **Canonical pair: `user_key` / `api_token`**, matching the encryption list and the
   snake_case of sibling secrets (`api_key`, `app_token`, `routing_key`).
   - Form (`integration-form.tsx`): read/write `user_key` and `api_token`.
   - Sender: `pushoverSettings` tags become `json:"api_token"` / `json:"user_key"`.
   - Docs: name the keys in the Pushover section (useful for API/apply users).
2. **Tolerant sender during the transition.** After unmarshalling, fall back to
   `token`/`apiToken` and `user`/`userKey` when the canonical key is empty, so existing rows
   deliver before the backfill runs. Remove the fallback in a later release (leave a dated
   `TODO` naming this spec).
3. **Backfill existing rows.** A one-shot job (pattern of the other credential re-seal or
   migration jobs; see `crypto/credentials`) that, for every `pushover` integration, renames
   `user`/`userKey` → `user_key` and `token`/`apiToken` → `api_token`, then re-splits with
   `SplitConfig` so both land encrypted in `settings_private`, and clears them from
   `settings`. Idempotent. Log the count.
4. **Same check for user contacts and escalation.** Read how `pushover_user` contacts store
   the user key and how `job_escalation_step.go` builds the Pushover payload. Align them on
   the same keys if they drift.
5. **Configuration errors are not delivery failures.** When a sender returns a
   "not configured" error (`ErrPushover*NotConfigured` and siblings in other senders), the
   test endpoint returns a distinct code (e.g. `INTEGRATION_MISCONFIGURED`) and dash0 shows
   "This integration is missing its API token" instead of a generic failure.
6. **Drift guard test (CI).** For every integration type, assert that:
   - the keys the dashboard form writes are the keys the sender reads (keep a Go-side
     table of form keys per type, checked against a fixture exported from the form, or a
     shared JSON manifest both sides import, whichever is simpler);
   - every secret field the form writes is listed in `conn_secrets.go`.

### Tests

- Sender unit test: canonical keys deliver (against a stub server), legacy `user`/`token` and
  `userKey`/`apiToken` deliver too.
- Backfill job test (sqlite + postgres): a legacy row ends with `user_key`/`api_token` in
  `settings_private` only, and `settings_private_keys` lists both. A second run is a no-op.
- Playwright: create a Pushover integration and press **Send test** against a stubbed sender
  or test mode; the request reaches the sender with the canonical keys.
- The drift guard test above.

## Closing the issue

This spec closes #494. The implementing PR body **must** carry one `Closes #494` line so the
squash-merge closes it. When the spec is archived to `specs/done/`, verify the issue is
closed and, if the merge did not close it, close it by hand:

    gh issue close 494 --comment "Implemented by <PR or commit>; spec: <archived spec path>"
