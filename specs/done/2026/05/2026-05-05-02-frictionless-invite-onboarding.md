# Frictionless invite onboarding — drop the redundant email field

## Context

When an admin invites a new user, the recipient lands on `/invite/$token`
(`web/dash0/src/routes/invite.$token.tsx`). The page already shows the invited
email address in the header (line 138–143, with the mail icon), then asks the
user to re-type that same email into a required `<Input type="email">` (line
182–193) before they can finish creating their account.

That second step is doing no work. The backend's `AcceptInviteRequest`
(`server/internal/handlers/auth/service.go:2188-2193`) only declares
`Token`, `Name`, `Password` — there is no `Email` field. The email sent by the
frontend in `useAcceptInvite` (`web/dash0/src/api/hooks.ts:1568-1580`) is
silently discarded by the JSON decoder. The account is always created with the
email that was bound to the invitation token at creation time
(`stateValue["email"]`, service.go:2234). So the field is not a security check,
not a confirmation, not a typo guard — it's pure ceremony.

Removing it is a one-screen win for first-time conversion, and it brings the
flow in line with how Slack, Linear, Notion, and GitHub all handle invite
acceptance.

## Goal

A first-time invitee accepting an invitation should never type their own email
address. The `/invite/$token` page should display the invited email read-only
in the header, then ask only for the credentials needed to authenticate the
new account (name optional, password, confirm password).

## Scope

In scope:
1. **Frontend — invite page form** (`web/dash0/src/routes/invite.$token.tsx`):
   - Remove the email `<Input>` block (lines 182–193) and the `email` state
     (line 34).
   - Keep the existing email display in the card header (line 138–143). Make
     it slightly more prominent: not just a hint, but a clear "Creating
     account for <email>" line so the user always sees which address they're
     activating.
   - Drop the `email` argument from the `acceptInvite.mutateAsync({...})` call
     in `handleAcceptNewUser` (line 71–77).
2. **Frontend — hook signature** (`web/dash0/src/api/hooks.ts:1568-1580`):
   - Remove `email?: string` from the `useAcceptInvite` mutation input. Since
     the backend already ignores it, there's no compatibility concern.
3. **i18n** (`web/dash0/src/locales/{en,fr,de,es}/auth.json`, `invite` block):
   - Add a key like `invite.creatingAccountFor: "Creating account for {{email}}"`
     used in the header.
   - Remove now-unused keys, if any. (`auth:emailPlaceholder` is shared with
     other forms — leave it.)
   - All four locales land in the same PR — partial translations break the
     fallback chain (`2026-05-02-08`).
4. **Tests**:
   - Update / add a Playwright e2e test in `web/dash0/e2e/` covering the
     invite flow: an invited new user reaches the page, sees their email
     pre-filled, fills only name + password, and lands inside the org. The
     existing tests (whichever already cover invite) need their email-typing
     step deleted, not adapted.
5. **Authenticated-user branch** (line 153–167): unchanged. Already does the
   right thing.

Out of scope (own future spec — see "Honest opinion" below):
- Magic-link / passwordless invite acceptance (no password at all on first
  visit, set one later from settings).
- Removing the "confirm password" field.
- Auditing the rest of the auth flows (`confirm-registration.$token.tsx`,
  `forgot-password.tsx`, `reset-password.$token.tsx`) for similar paper cuts.
- Letting an invitee accept the invitation under a different email. The
  product stance is: if the invited email is wrong, an admin re-issues the
  invitation. We are not building a "change my email at acceptance time" path.

## Edge cases

- **Invitation expired or token unknown.** Already handled by `infoError ||
  !inviteInfo` (line 104–122). Unchanged.
- **User is already authenticated.** Already handled — they get a single
  "Accept invitation" button, no form. Unchanged.
- **Backend ever decides to validate email at accept time.** Not part of this
  spec, but if a future spec wants belt-and-braces, it should compare the
  request's email (if present) to the token-bound email *server-side* and
  reject mismatches — not put the burden on the user. This spec deletes the
  client-side field; it does not preclude that future check.
- **Email rendering width.** Long addresses (`firstname.lastname@long-corporate-domain.example.com`)
  must not break the card layout. Use `break-all` or `truncate` with a title
  attribute; verify in the e2e test viewport.

## Test plan

- [ ] Manual: invite a brand-new email from `admin@solidping.com`. Open the
      invite link in a fresh incognito window. Confirm the email is shown,
      not asked. Submit name + password + confirm. Land in the org dashboard.
- [ ] Manual: invite an email belonging to an existing logged-in user. Same
      flow, single-button branch. Should still work end-to-end.
- [ ] Manual: open an expired invite link. Confirm the existing
      `invite.invalid` card renders.
- [ ] Manual: invite an email with a long local-part + domain (>40 chars).
      Confirm header layout doesn't break.
- [ ] e2e: extend `web/dash0/e2e/` to cover the new-user invite path without
      typing an email.
- [ ] `make lint` + `make test` clean.

## Files touched (estimate)

- `web/dash0/src/routes/invite.$token.tsx` — form simplification
- `web/dash0/src/api/hooks.ts` — drop `email` from `useAcceptInvite` input
- `web/dash0/src/locales/{en,fr,de,es}/auth.json` — copy adjustments
- `web/dash0/e2e/<invite-spec>.ts` — flow coverage
- No backend changes. (The redundancy proves the backend is already the
  source of truth.)
