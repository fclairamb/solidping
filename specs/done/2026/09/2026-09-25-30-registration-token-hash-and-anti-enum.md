---
model: sonnet
effort: low
---

# Registration pending entries store the confirm token and password hash in plaintext; registration enumerates emails

## Problem

Two issues in `Register` (`handlers/auth/service.go`):

1. The pending-confirmation state entry stores the email-confirm token
   **plaintext** alongside the `passwordHash` (`service.go:2382-2387`), and
   the confirm path scans pending entries matching the raw token with `==`
   (`service.go:2422-2440`). A DB leak exposes both the token and the
   pending password hash for the 3-day TTL window. The reset flow in the
   same file does it right: SHA-256 the token before storage
   (`service.go:2321-2328`).
2. `Register` returns `ErrEmailAlreadyTaken` for existing emails
   (`service.go:2363-2365`) — a live account enumerator, inconsistent with
   the reset flow's uniform success shape (`service.go:2652-2671`).

## Proposal

1. Store only `sha256hex(token)` in the pending entry (add a
   `keyTokenHash`-style key, drop the raw one); the confirm path hashes the
   presented token and compares. Lookup by hash where the state store
   supports keying by value; if it scans, hash-compare per candidate (same
   shape as today's `==` scan).
2. Anti-enumeration: when the email is already taken, behave exactly like a
   successful registration from the caller's perspective — same response
   body, same 200 — and skip creating the pending entry and sending the
   email (optionally send the "someone signed up with your email" notice
   email the reset flow uses, if that pattern exists). Add a dummy work
   step (hash a throwaway password) so the response timing matches the
   success path — the codebase already does exactly this in `ChangePassword`
   (`service.go:3041-3047`).
3. Migration: no stored data to migrate — pending entries expire on their
   own; in-flight entries at deploy time just fail confirmation once and
   the user re-registers (acceptable, 3-day window). Note it in the PR.
4. Tests asserting the old error must be inverted: taken email now looks
   identical to success (but no pending row is created — assert that).
5. Docs/changelog: behavior note for API consumers (this is deliberately
   breaking for anyone who keyed on the error — nobody should).

## Tests

- Both DBs: register → pending entry stores a 64-char hex hash, not the raw
  token; confirm with the original token succeeds and creates the account;
  second confirm → uniform already-confirmed error.
- Taken email: response is byte-identical to the success case (status,
  body, headers); no pending entry row is written; no confirmation email
  sent (mail log assertion).
- Wrong token on confirm → same generic error as unknown token.
- Expired entry → same generic error.