# Runbook — "This invitation link is invalid or has expired"

If you land here because a fresh invitation link shows dash0's "invalid or
expired" card, read this before re-opening the investigation from scratch.

## What was reported (2026-08-31)

Opening a freshly created invitation link — copied straight from the
invitations page (`orgs/$org/organization/invitations`), not the email —
landed on the "invalid or expired" card instead of the join/accept page, at
least in local development.

## What we found

**No product defect.** The reporter confirmed the link works: the create →
copy → open → accept chain (`Service.CreateInvitation` →
`GET /api/v1/auth/invite/:token` → `Service.GetInviteInfo` →
`GetStateEntry`) behaves correctly on both dialects. There was no failing run
to attribute to Postgres's `expires_at > NOW()` comparison or SQLite's
`datetime('now')` string comparison — both were hypotheses raised by a static
read of the code, not confirmed causes. **Do not re-open that hunt** on the
strength of one more "invalid or expired" report; verify first (see below).

## What actually shipped from this report

Two real, durable improvements came out of chasing this down, independent of
whether the original report had a root cause:

1. **The invite page conflated every failure into "invalid or expired."**
   `routes/invite.$token.tsx` rendered the destructive card for *any* error
   from `useInviteInfo` — including a 429 (rate limited), a 5xx, or a plain
   network failure. A transient error and a genuinely dead token looked
   identical to the person holding the link, and a transient failure has a
   fix (retry) that the expired-link card doesn't offer. Fixed by
   `lib/invite-error.ts`'s `isInviteInvalidError`, which recognizes only
   404 `INVITATION_NOT_FOUND` and 410 `INVITATION_EXPIRED` as "this link is
   dead"; everything else now renders a distinct, retryable state.
2. **`e2e/invitations.spec.ts` never opened an invite link.** It asserted the
   shape of the returned `inviteUrl` and stopped there — the one test that
   would have answered "does this actually work" in seconds didn't exist.
   The suite now covers create → open `/dash0/invite/:token` logged out →
   see the join card (org name, role, masked email) → accept as a new user →
   land in the org, plus the negative case (a bogus token shows the invalid
   card).

## If you see this report again

Before assuming a regression, check in order:

1. **Is the token actually being opened, byte-for-byte, as issued?** Compare
   the URL against the `inviteUrl` returned by
   `POST /api/v1/orgs/:org/invitations` — email client link-wrapping and
   copy/paste truncation are the usual suspects, not the backend.
2. **What does `GET /api/v1/auth/invite/<token>` return directly** (curl, not
   the browser)? A 404/410 is a real dead link; anything else (429, 5xx,
   a timeout) is the transient case item 1 above already handles — confirm
   the retryable card renders instead of "invalid or expired" for that case.
3. **Was the invite actually still within its TTL** at open time? Expirations
   come from a whitelist (`getAllowedInviteExpirations()`,
   `server/internal/handlers/auth/service.go`), so a "0 TTL" bug is not
   possible from that path.

If the token genuinely round-trips and the endpoint still 404s for a
non-expired invite, that is a real regression worth its own investigation —
just don't start from the SQLite/Postgres expiry-comparison theory again
without first confirming which dialect and which HTTP response are actually
in play, the way this report's follow-up should have but couldn't (the
reporter closed the loop before that data was captured).
