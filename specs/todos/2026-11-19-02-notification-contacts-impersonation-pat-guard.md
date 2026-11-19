---
model: opus
effort: medium
---

# An impersonation session or a PAT can add a paging contact to its target, which survives the session or the token

## Problem

Found by the security scan of 2026-11-19 (finding AR1). Severity: medium. Who can trigger it:

- a super admin impersonating a member (`auth.impersonation_enabled`);
- anyone holding a member's personal access token for one org, a viewer's included: the route is on `orgGroupSelf`, so viewers may edit their own contacts.

Both credential denylists already treat "bind a paging channel to the target" as forbidden.

- `server/internal/handlers/auth/impersonation_guard.go:81-86` refuses `POST …/users/me/discord/connect` and `POST …/users/me/telegram/link`: "the target's pages would land on an admin-controlled chat".
- `server/internal/handlers/auth/pat_guard.go:66-68` refuses the same pair for PATs. The table exists so that nothing a PAT does "survives the PAT's revocation" (`pat_guard.go:27-32`).

The generic contact routes reach the same result, and neither list covers them:

- `POST /api/v1/orgs/{org}/users/me/notification-contacts` (`server/internal/app/server.go:1777`, `CreateContact` in `server/internal/handlers/usernotifications/service.go:488`) adds a contact and its route.
- `email` and `webpush` contacts are created already verified (`service.go:545-550`). The comment says "Email is verified by sending", but nothing is sent on this path. `ntfy_topic` and `pushover_user` need no round trip either: only phone and WhatsApp do (`contactRequiresSetup`, `service.go:757-761`).
- `PATCH …/notification-routes/{routeUid}` (`server.go:1778`) re-enables a route the owner had disabled.
- `CreateContact` and `PatchRoute` have no service-level check either. `refuseImpersonation` (`usernotifications/discord.go:34`) is only called by the Discord and Telegram flows (`telegram.go:94`, `discord.go:155`), and nothing in the package checks `IsPATCredential`.

Impact:

- A contact added during an impersonation session, or with a PAT that is later revoked, keeps receiving the target's escalation pages for that org (`server/internal/jobs/jobtypes/job_escalation_step.go:533`).
- These pages carry incident titles, check names, error messages and links to the org. The owner gets no notice. The contact only shows up if they open their notification settings.

Secondary: since an email contact is born verified, any member can also point their own pages at an email address they don't own. That is a nuisance relay of the instance's mail to a stranger, not a data leak.

## Proposal

1. In `usernotifications`, refuse contact and route mutations from an impersonated or a PAT request, in the service where the sibling flows already do it:
   - add a helper next to `refuseImpersonation` (`discord.go:34`) that also refuses `auth.IsPATCredential(ctx)` (or the equivalent claim check used by `CreateTelegramLink` / device consent);
   - call it at the top of `CreateContact` (`service.go:488`), `PatchRoute` (`service.go:593`) and `VerifyContact` / `ConfirmVerify` (a verified phone number is a paging channel too);
   - `DeleteContact` may stay allowed: removing a channel never redirects pages.
   - Map the errors through `handler.go:52` (`IMPERSONATION_FORBIDDEN`) and the PAT equivalent (`PAT_FORBIDDEN`).
2. Add the same routes to `impersonationForbiddenRoutes` (`impersonation_guard.go`) and `patForbiddenRoutes` (`pat_guard.go`), so the guards' answer is the explicit code and the tables stay the documented source of truth:
   - `POST /api/v1/orgs/{org}/users/me/notification-contacts`
   - `PATCH /api/v1/orgs/{org}/users/me/notification-routes/{routeUid}`
   - `POST /api/v1/orgs/{org}/users/me/notification-contacts/{contactUid}/verify` and `…/verify/confirm`
3. Stop treating a new `email` contact as verified unless its address is the user's own verified sign-in email:
   - any other address goes through a confirm link (reuse the `/verify` + `/verify/confirm` round trip, with an emailed code);
   - escalation skips an unverified email contact, as it already skips unverified phone and WhatsApp contacts.
4. Send the owner a notice email when a contact is added to their account (same template family as the Discord link notice, `auth/discord_link_notice.go`).

## Tests

- `server/internal/handlers/auth/impersonation_guard_test.go` and the PAT guard test: the new rows refuse their routes, and a positive control shows that `GET …/notification-routes` and `DELETE …/notification-contacts/{uid}` still pass.
- `server/internal/handlers/usernotifications/service_test.go`:
  - `CreateContact` and `PatchRoute` with an impersonator in the context return `ErrImpersonationForbidden`; with PAT claims they return the PAT error. Both fail before the fix.
  - Positive control: the same calls with a normal session succeed.
- Email verification:
  - a new email contact for an address other than the user's sign-in email starts unverified and gets no escalation page until it is confirmed;
  - positive control: a contact for the user's own verified email is usable at once.
- Route-level test through the real router (like `server/internal/app/check_test_run_route_test.go`): a PAT-authenticated `POST …/notification-contacts` gets 403.

## Open questions

- Should a member's own session (not impersonated, not a PAT) still add a contact without re-authentication? Recommended: yes. The notice email from step 4 is enough there, as for Discord linking.
