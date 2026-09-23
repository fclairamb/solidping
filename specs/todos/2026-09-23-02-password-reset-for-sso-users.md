---
model: sonnet
effort: medium
---

# "Forgot password" silently sends nothing to a user who signed up through SSO

## Problem

A user who joined through SSO (Google, GitHub, Microsoft, SAML, OIDC, LDAP…) has a
nil `users.password_hash`. When that user forgets how they signed in and uses
"Forgot password", `RequestPasswordReset` returns the generic success message and
does nothing:

```go
// server/internal/handlers/auth/service.go:2667
user, _ := s.db.GetUserByEmail(ctx, req.Email)
if user == nil || user.PasswordHash == nil || *user.PasswordHash == "" {
    return successMsg, nil
}
```

The dashboard says "If an account exists with that email, a reset link has been
sent", no email ever arrives, and the user is stuck. They don't know they have an
account through SSO, or which provider it was. This is a recurring support
confusion.

Today the only way for an SSO-only user to get a password is the authenticated
`POST /auth/change-password` path (`handler.go:672`, "or sets an initial one for
an SSO-only account"), which requires already being logged in, i.e. already
knowing how to log in.

## Proposal

### 1. Password reset always works for SSO-only users

In `RequestPasswordReset`, stop treating "no password hash" as "no account". A user
that exists but has no password goes through the same token flow as a password
user: same rate limits (`bumpResetIPCounter`, `bumpResetUserCounter`), same hashed
state entry, same 1h TTL, same `ResetPassword` confirmation. `ResetPassword`
already works for a nil hash: it writes one, clears `must_change_password`, revokes
refresh tokens and sends `password-changed.html`. The demo guard in `ResetPassword`
stays as is.

There is no org-level switch for this. It is always on. Restricting how members of
an org sign in is a separate concern, tracked in
`specs/backlog/2026-09-23-03-org-enforced-sso-provider.md`.

A user with neither a password nor any `user_providers` row keeps getting nothing
(no account to recover, and nothing to point them to).

### 2. The email says how they signed in until now

The plain reset email is not enough: the user is confused about *how* they log in,
not only about a password. For an SSO-only user the email adds:

> You usually sign in to SolidPing with **Google**. You can keep doing that, or
> set a password with the link below.

The provider list comes from the user's `user_providers` rows (`models.UserProvider`,
`models/auth.go`), mapped to a human label ("Google", "GitHub", "Microsoft",
"GitLab", "Slack", "Discord", "LDAP"…). SAML and OIDC use their configured
`DisplayName` (see `config/saml_auth.go`, `config/oidc_oauth.go`), falling back to
"SSO". Several providers are listed joined ("Google or GitHub").

Either extend `password-reset.html` with an optional `Providers` block or add a
dedicated `password-reset-sso.html`. Implementer's call, but the email a password
user receives must not change.

Add the new or extended template to the email preview fixtures
(`handlers/emailpreview/fixtures.go`).

### 3. Unchanged behavior

- The HTTP response stays the same generic success message in every branch
  (unknown email, password user, SSO-only user). The API response must not reveal
  whether an account uses SSO.
- Password users keep today's exact flow and email.
- A user with both a password and linked providers is treated as a password user
  (today's email). Mentioning their providers too is fine but not required.

### Tests

- SSO-only user → token stored, reset email enqueued with the provider label(s);
  `ResetPassword` with that token sets the hash, and the user can then log in with
  email + password. The SSO login still works afterwards.
- SSO-only user with two providers → both labels in the email.
- SAML/OIDC user → the configured display name, and "SSO" when it is blank.
- Password user → email identical to today (no provider block).
- User with no password and no provider → nothing stored, nothing sent (positive
  control: the SSO-only case above does store and send).
- The per-user and per-IP rate limits apply to SSO-only users.
- Response body identical across unknown / password / SSO-only emails.
