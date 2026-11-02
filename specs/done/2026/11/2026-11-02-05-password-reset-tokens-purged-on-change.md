---
model: opus
effort: medium
pr: batch
automerge: false
---

# Outstanding password-reset links survive a password change, a reset and an email change

## Problem

Found by the security scan of 2026-11-02 (finding AC6). Severity: low. Whoever holds an unused reset link can trigger it: the old mailbox's owner after an email change, or anyone who read a reset email.

- Reset tokens are stored as `password_reset:<sha256(token)>`, valid 1 h (`server/internal/handlers/auth/service.go:3081`), and resolved by hash alone (`:3221`).
- Nothing deletes a user's other outstanding tokens:
  - `ResetPassword` deletes only the entry it consumed (`:3273`);
  - `ChangePassword` revokes refresh tokens but leaves reset entries (`:3394`);
  - so does the email change (`email_change.go:176`).
- Consequences:
  - A link requested before a password change still sets a new password afterwards.
  - After an email change, a link sent to the old address still works for up to an hour. Moving the email away from a lost or compromised mailbox does not cut that mailbox off.
  - When several resets were requested, using one leaves the others valid.

## Proposal

1. Index reset entries per user: write `password_reset_user:<userUID>:<tokenHash>` next to the token entry, with the same TTL.
2. Add `purgePasswordResets(ctx, userUID)`, which lists that prefix and deletes both keys of each entry. Call it from `ResetPassword` (after success), `ChangePassword`/`persistPasswordChange`, the first-password set, `applyEmailChange` and the super-admin email change.
3. Defence in depth: store the email the link was sent to in the entry, and refuse the reset if it no longer matches the user's current email.

## Tests

In `server/internal/handlers/auth`:

- Two resets requested. Using the first one makes the second fail with the expired/invalid error.
- Reset requested, then `ChangePassword`. The reset link fails.
- Reset requested, then an email change. The link fails.
- Positive control: a reset requested after the password change works.
