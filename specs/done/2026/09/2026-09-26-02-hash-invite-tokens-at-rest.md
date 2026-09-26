---
effort: medium
---

# Invitation tokens are stored in plaintext, both in the state-entry key and in its value

## Problem

`CreateInvitation` (`server/internal/handlers/auth/service.go:3561`) generates a
32-byte random invite token and persists it in plaintext twice:

- as the state-entry key: `stateKey := inviteKeyPrefix + token` (`service.go:3607`);
- inside the stored value: `keyToken: token` (`service.go:3601`).

Anyone who can read `state_entries` (a DB dump, a backup, a read-only SQL
leak) gets a usable invite link for as long as the invite lives, which is up to
**1 week** (`getAllowedInviteExpirations`, `service.go:3466`). Accepting it
creates an account with the invited role in the inviting org.

This is the same defect spec 2026-09-25-30 fixed for pending registrations
(commit `fc77f8ba8`, "fix!: hash registration confirm tokens at rest") and that
password reset never had: reset keys by `passwordResetKeyPrefix +
hashPendingToken(token)` (`service.go:2816`, lookup `service.go:2999`). The
`keyTokenHash` comment at `service.go:68-72` even names invites as the one flow
still storing plaintext.

## Proposal

Key invites by the hash, exactly like password reset. Invites are already keyed
by token, so no value-side hash field is needed.

1. **Write** (`CreateInvitation`): `stateKey := inviteKeyPrefix +
   hashPendingToken(token)`, and drop `keyToken: token` from the stored
   `JSONMap`. The plaintext token is still returned in `InviteResponse.Token`
   and embedded in `InviteURL` / the invitation email; that is the one place the
   admin and the invitee legitimately see it.
2. **Read** (`GetInviteInfo`, `service.go:3708`, and `AcceptInvite`,
   `service.go:3744`): hash the presented token before building the key. Both
   are per-org `GetStateEntry` lookups by key, not scans, so this is a one-line
   change each. The two `DeleteStateEntry` calls in `AcceptInvite`
   (`service.go:3809`, `service.go:3841`) must delete the key that actually
   matched (use `matchedEntry.Key`, not a recomputed key), so they stay correct
   under the legacy fallback below.
3. **In-flight invites (decision: legacy fallback, not a hard cut).** Unlike
   registrations, an invite is sent by an admin to someone else and can be
   valid for a week, so silently breaking every outstanding link at deploy is
   not acceptable. On lookup, try `inviteKeyPrefix + hash` first; on miss, try
   the legacy `inviteKeyPrefix + token`. Mark the legacy branch with a comment
   and a dated `TODO(remove after 2026-10-10)` (deploy + max TTL 1w + margin).
   No data migration: legacy rows expire on their own. Only new rows are
   written in the hashed form.
4. **Paths that do not need the token** stay as they are and must keep working
   with hashed keys: `ListInvitations` (`service.go:3656`, reads email/role from
   the value), `RevokeInvitation` (`service.go:3690`, matches by `entry.UID`,
   deletes `entry.Key`), and `findOrgInvitationForEmail`
   (`server/internal/handlers/auth/join_policy.go:537`, matches by email, keeps
   `entry.Key`). None of them read `keyToken`; confirm with a grep and leave
   them alone.
5. **Comments**: update the `keyTokenHash` comment (`service.go:68-72`) and the
   `hashPendingToken` doc comment (`service.go:2368-2374`) so they no longer say
   invites store plaintext, and list invites as a key-suffix user alongside
   password reset.

### Tests and e2e

Nothing currently reads the plaintext invite token back out of a state entry:
Go tests (`service_test.go:83` onwards, `created_with_test.go:89`,
`handler_test.go:78`) and `web/dash0/e2e/invitations.spec.ts:28,195` all take
the token from the `CreateInvitation` response, which still carries it. The
`join_policy_test.go:149` fixture writes a synthetic `invite:tok-<name>` key and
only relies on email matching, so it is unaffected. The implementer should
re-grep (`inviteKeyPrefix`, `"invite:"`, `/api/v1/test/state-entries` in
`web/dash0/e2e/`) to confirm, and fix anything new. If a test does need the
token without the response, recover it from the queued invitation email via
`GET /api/v1/test/jobs?type=email` (`server/internal/handlers/testapi/handler.go:111`).

New Go tests (SQLite, plus Postgres where the file already runs both):

- Create an invite: the stored key is `invite:` + 64 hex chars equal to
  `hashPendingToken(resp.Token)`; no stored key contains `resp.Token`; the value
  has no `token` field.
- `GetInviteInfo(resp.Token)` and `AcceptInvite` with `resp.Token` succeed; the
  entry is gone afterwards.
- Presenting the hash itself as the token (`GetInviteInfo(hashPendingToken(t))`)
  is rejected with `ErrInvitationNotFound`. This is the negative that proves the
  hash is not usable as a credential.
- Legacy fallback: a row written by hand under `invite:<plaintext>` is still
  accepted via `AcceptInvite`, and deleted on acceptance.
- `ListInvitations` and `RevokeInvitation` work on a hashed-key invite.

Run `go test ./internal/handlers/auth/... -short`, the Postgres layer for the
auth package, and `web/dash0/e2e/invitations.spec.ts`.

### Changelog

One `fix:` entry: invite tokens are now stored hashed at rest. Outstanding
invites keep working. Not breaking (no `!`), unlike the registration change,
because of the legacy fallback.
