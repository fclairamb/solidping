---
model: opus
effort: medium
---

# Failed sign-in events don't say which account was tried

GitHub Issue: https://github.com/fclairamb/solidping-business/issues/59

## Source

In-app report on the org dashboard (v0.44.3), "Recent activity" card: a column of
`Sign-in Failed` rows, actor `System`, Related empty. Nothing says which account the
attempt was against. Requested: show the account name; org admins see every failed
attempt, other members see only the attempts against their own account.

## Problem

- **Display**: `server/internal/audit/loginfailed.go:169` records
  `auth.login_failed` with `target_name` = the typed email and a payload carrying
  `email`, `reason`, and the fold counter `count` / `first_at` / `last_at`. The
  shared `EventLogTable` (`web/dash0/src/components/dashboard/event-log-table.tsx:76`,
  `getEventDetail`) has no branch for `auth.*`, so the row shows only its label. The
  actor is `System` because there is no authenticated principal on a failed login
  (`server/internal/handlers/auth/audit.go:89`). The same gap exists on the Events
  page, which uses the same table.
- **Visibility**: `server/internal/handlers/events/service.go:33`
  (`RestrictedEventTypePrefixes = ["auth"]`) excludes the whole `auth` family for
  non-admins (`:162`). So a member today sees none of their own failed sign-ins.
  The admin half of the request is already the behaviour; the member half is not.

## Why a spec

- Correctness hinges on authorization scope: exposing part of the restricted `auth`
  family to non-admins. The restriction exists so the trail is not an enumeration
  oracle or a map of colleagues' habits (comment at `events/service.go:20`).

## Proposal

1. **Display (all roles that can see the row)**: in `getEventDetail`, for
   `auth.login_failed`, render the tried account (`payload.email`, falling back to
   `target_name`) and, when `count > 1`, `× <count>` (i18n key in `events`, all
   locales). For `auth.login_succeeded`, the actor already names the user; nothing
   to add. If the email matches an org member, the Related column may show the
   member's name; otherwise show the email as typed. `data-testid="event-detail-auth-account"`.
2. **Member visibility**: a non-admin caller sees `auth.login_failed` rows whose
   target is **their own account** (case-insensitive match of the normalized email
   against the caller's user email; `audit.go:118` already normalizes it), and still
   none of the other `auth` rows. Implement as a filter clause, not a post-filter,
   so pagination stays correct: `ExcludeEventTypePrefixes = ["auth"]` plus an
   `IncludeOwnFailedLogins{email}` OR-branch in both `db/postgres` and `db/sqlite`
   `ListEvents`. Source IP stays admin-only (`toResponse` already withholds it).
   The `type=` / `actor=` / `target=` filters must not let a member widen this.
3. **Dashboard card**: no change beyond 1; it already lists what `/events` returns.
4. **Tests**:
   - Go (`events` service, both DBs): a member sees a failed login against their
     email and not one against another member's; an admin sees both; a member's
     `?type=auth.login_failed` and `?target=<other email>` return nothing else.
   - Vitest or Playwright: an `auth.login_failed` row renders the account and the count.

## Open questions

- **Name or email?** Recommended: the email as typed (it is what was tried, and it
  may not match any member: no enumeration from the display), plus the member's name
  in brackets when it does match and the reader is an admin. Trade-off: a name-only
  display would hide attempts against non-existent accounts, which is exactly what
  credential stuffing looks like.

## Closing the issue

This spec closes fclairamb/solidping-business#59. The implementing PR body **must**
carry `Closes fclairamb/solidping-business#59`. When the spec is archived to
`specs/done/`, verify the issue is closed and, if the merge did not close it, close
it by hand:

    gh issue close 59 --repo fclairamb/solidping-business --comment "Implemented by <PR or commit>; spec: <archived spec path>"

## Resolved open questions

- Show the email as typed, plus the member's name in brackets when it matches and the reader is an admin. (autopilot: recommended answer)
