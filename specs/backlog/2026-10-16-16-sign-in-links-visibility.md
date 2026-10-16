---
model: opus
effort: high
---

# Users cannot see or remove the sign-in methods linked to their account, and are not told when one is added

**Status:** backlog. Split out of `specs/done/2026/10/2026-10-16-01-microsoft-signin-unverified-email-linking.md` (resolved open question 3).

## Problem
A federated identity (Google, GitHub, GitLab, Microsoft, Discord, Slack, OIDC, SAML, LDAP) is linked to a local account through a `user_providers` row, sometimes automatically by matching email. The user has no page that lists these links, no way to remove one, and receives no email when a new one is created. A link created through an attack (the Microsoft "nOAuth" case fixed in 2026-10-16-01, or any future connector bug) is invisible to the victim.

## Proposal
1. Account page section "Sign-in methods": list every linked provider (type, display identifier, linked at, last used), plus password and passkeys.
2. Unlink action per provider, refused when it would leave the account with no way to sign in.
3. Email the user whenever a provider is linked to their account, naming the provider and identifier, with a link to the account page.
4. API: `GET /api/v1/auth/me/providers`, `DELETE /api/v1/auth/me/providers/$uid` (camelCase, `{ "data": [...] }`).

## Tests
- Listing shows every link type; unlink removes the row and the next federated sign-in no longer matches it.
- Unlinking the last sign-in method is refused.
- Linking sends exactly one notification email; a sign-in through an existing link sends none.
