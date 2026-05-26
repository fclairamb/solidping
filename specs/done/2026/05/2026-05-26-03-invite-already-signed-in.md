# Invite page shows "Creating account for…" when already signed in

## Problem

When a logged-in user opens an invitation link (`/invite/$token`), the "Join"
card displays **"Creating account for fl***@…"** even though the user already has
an account and is authenticated. This message is confusing and incorrect — no
account is being created for them.

Current display (authenticated user):
```
[Logo]
Join <org>
You've been invited to join as user
✉ Creating account for fl***@clairambaultfr   ← wrong when signed in
[Accept Invitation]
```

Expected display (authenticated user):
```
[Logo]
Join <org>
You've been invited to join as user
You're already signed in                       ← clarifies situation
[Accept Invitation]
```

### Root cause

In `web/dash0/src/routes/invite.$token.tsx` (lines 127–136), the email block
renders `auth:invite.creatingAccountFor` unconditionally whenever `inviteInfo.email`
is truthy. It sits **above** the `isAuthenticated` branch at line 146, so it
appears in both the authenticated and unauthenticated paths. "Creating account for…"
is only correct for the unauthenticated create-account flow.

## Change

In `AcceptInvitePage` (`web/dash0/src/routes/invite.$token.tsx`), gate the email
note on authentication state:

- **Not authenticated** (`!isAuthenticated && inviteInfo.email`): keep the existing
  `Mail` icon + `auth:invite.creatingAccountFor` block — unchanged.
- **Authenticated** (`isAuthenticated`): replace the block with a short muted
  paragraph using a new key `auth:invite.alreadySignedIn` ("You're already signed
  in"). Do **not** show "Creating account for {email}".

The `isAuthenticated` check (`!!getToken()`, line 28), both accept handlers,
the "Accept Invitation" button, and the unauthenticated create-account form are
all untouched.

## Scope

- `web/dash0/src/routes/invite.$token.tsx` — gate the email block on `!isAuthenticated`.
- `web/dash0/src/locales/en/auth.json` — add `"alreadySignedIn"` to the `invite` object.
- `web/dash0/src/locales/fr/auth.json` — same.
- `web/dash0/src/locales/de/auth.json` — same.
- `web/dash0/src/locales/es/auth.json` — same.

No backend, no API, no membership detection.

## Acceptance criteria

- Opening an invite link while **signed in** shows "You're already signed in" (not
  "Creating account for {email}") with the "Accept Invitation" button below it unchanged.
- Opening an invite link while **signed out** is unchanged: "Creating account for {email}"
  and the name/password create-account form both appear.
- The heading ("Join {org}") and "invited to join as {role}" line are unchanged in
  both states.
- New `alreadySignedIn` key is present in all four locale `auth.json` files.
- Build and lint pass.

## Implementation Plan

1. **Add i18n key to all locale files.** Inside each `"invite"` object, add
   `"alreadySignedIn"` after `"creatingAccountFor"`:

   `web/dash0/src/locales/en/auth.json`:
   ```json
   "alreadySignedIn": "You're already signed in"
   ```
   `web/dash0/src/locales/fr/auth.json`:
   ```json
   "alreadySignedIn": "Vous êtes déjà connecté(e)"
   ```
   `web/dash0/src/locales/de/auth.json`:
   ```json
   "alreadySignedIn": "Sie sind bereits angemeldet"
   ```
   `web/dash0/src/locales/es/auth.json`:
   ```json
   "alreadySignedIn": "Ya has iniciado sesión"
   ```

2. **Gate the email note on auth state** in `web/dash0/src/routes/invite.$token.tsx`.
   Replace the current unconditional block (lines 127–136):
   ```tsx
   {inviteInfo.email && (
     <div className="mt-3 flex items-center justify-center gap-2 text-sm">
       <Mail className="h-4 w-4 shrink-0 text-muted-foreground" />
       <span className="break-all" title={inviteInfo.email}>
         {t("auth:invite.creatingAccountFor", { email: inviteInfo.email })}
       </span>
     </div>
   )}
   ```
   With a conditional that branches on `isAuthenticated`:
   ```tsx
   {isAuthenticated ? (
     <p className="mt-3 text-sm text-muted-foreground">
       {t("auth:invite.alreadySignedIn")}
     </p>
   ) : inviteInfo.email ? (
     <div className="mt-3 flex items-center justify-center gap-2 text-sm">
       <Mail className="h-4 w-4 shrink-0 text-muted-foreground" />
       <span className="break-all" title={inviteInfo.email}>
         {t("auth:invite.creatingAccountFor", { email: inviteInfo.email })}
       </span>
     </div>
   ) : null}
   ```

3. **Verify build** — `cd web/dash0 && bun run build`.

4. **Verify lint** — `cd web/dash0 && bun run lint`.

## Non-goals

- Membership-aware UI (e.g. "You're already a member of {org}" + Go-to-org button).
- Detecting/warning when signed in as a different account than the invited email
  (the invited email is masked server-side, making frontend-only comparison unreliable).
- Any backend or API changes.
