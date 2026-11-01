---
model: sonnet
effort: medium
pr: batch
automerge: false
---

# Every org-less sign-in scans all invitations of the instance, and outstanding invitations are uncapped

## Problem

Found by the security scan of 2026-11-01 (finding AB4).

**Severity: low.** Any signed-in user can do it: on SaaS, anyone (open sign-up, the org creator is its admin). It degrades availability, and it takes sustained effort, so it would be noticed.

- `ensurePersonalOrg` (#716) runs at every sign-in of a user without an org: every new SaaS user at registration confirmation, federated first logins, and users whose orgs are gone. It calls `joinPendingInvitation` (`server/internal/handlers/auth/personal_org.go:90`), which:
  - lists **every** instance-wide invitation index entry (`ListStateEntries(ctx, nil, "invite:")`, no limit) and decodes each;
  - then, for each distinct org, lists all of that org's invitations again (`join_policy.go:609` `findOrgInvitationForEmail`) to compare emails in memory.
- `CreateInvitation` (`server/internal/handlers/auth/service.go:3883`) has no cap on outstanding invitations per org. Each one writes an org entry and a global index entry (`service.go:3941`) that live up to a week (`service.go:3777-3782`), and sends an email (`service.go:3961`).
- The attack:
  1. The attacker's org piles up invitations to addresses of their choosing, as fast as the API rate limit allows.
  2. Every new user's sign-in on the instance then reads and decodes all of them, twice.
  3. The cost grows linearly with the pile, on the sign-up path of every tenant.
- Side effect of the missing cap: the instance sends invitation emails, with an attacker-chosen org name, to any address at API speed. This is the same mail-reputation class as T7 (report test-send to any address).
- Same pattern, smaller: #727's per-IP sign-up limit lists every pending registration of the instance on each `Register` (`registration_ip_limit.go:66`) to count the caller's IP. It is bounded per IP (once a minute), not globally, so IP rotation grows it. `ConfirmRegistration` (`service.go:2751`) already scanned the same set before #727.

## Proposal

1. Cap outstanding invitations per org: refuse `CreateInvitation` with `409`/`422` and a clear code once the org has N live invitations. Count from the org's own entries (`ListStateEntries(ctx, &org.UID, "invite:")`, already scoped). Use a named constant, for example 50, or the org's seat cap when it is lower.
2. Replace the global scan in `joinPendingInvitation` with a keyed lookup:
   - write a second index entry keyed by the invitee's normalized email hash (`invite_email:<sha256(lower(email))>`), holding the org UID and the invite key, with the same TTL;
   - delete it with the invite (`deleteInvite`, `RevokeInvitation`, accept);
   - read it with one `GetStateEntry`.
   - An email can hold invites from several orgs: store a small list, or key by `email hash + org UID` with a prefix list bounded by the cap above.
3. Count pending registrations per IP without listing them all:
   - keep a per-IP state entry (`registration_ip_pending:<ip>`) holding the pending emails with their times, updated in `Register` and on confirmation;
   - or bound the existing scan with a `LIMIT` and treat hitting it as "over the limit".
   - `ConfirmRegistration` can look up by the token hash if the entry also gets a `registration_token:<hash>` key.

## Tests

- `server/internal/handlers/auth`:
  - creating the (N+1)th live invitation in one org is refused, and no email is enqueued (fails before the fix);
  - an expired or revoked invitation frees a slot (positive control).
- `personal_org_test.go`: a user with a pending invitation still joins the inviting org at sign-in (positive control, keyed lookup). A user with no invitation gets a personal org, with a query counter (like `invite_lookup_query_count_test.go`) asserting a constant number of state-table reads whatever the number of other orgs' invitations.
- `registration_ip_limit_test.go`: the per-IP limits still behave as today, with the same 429 codes, and the number of state-table reads does not grow with other IPs' pending registrations.

## To verify

- Whether `ListStateEntries` already has a `LIMIT`-taking variant in `server/internal/db`.
- Whether a migration is needed for existing invitations: index entries written before the change have no email key. Either backfill at startup, or fall back to the scan while old entries can still exist (one week).

## Open questions

- Cap value? **Recommended:** 50 live invitations per org, or the seat cap when lower. Raise it on request.
