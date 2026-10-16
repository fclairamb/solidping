---
model: opus
effort: high
---

# Microsoft sign-in must not link or create an account from an unverified email ("nOAuth")

## Problem
Found by the security scan of 2026-10-14 (finding D1). **High: unauthenticated account takeover that also skips the victim's TOTP.** Anyone who administers any Entra tenant can trigger it, on every instance with Microsoft sign-in enabled and no pinned tenant. The tenant defaults to `common` (`server/internal/config/microsoft_oauth.go:8`, `server/internal/handlers/auth/microsoft.go:98-100`, `microsoft_service.go:221-224`), and the docs present `common` as the default (`web/docs/docs/configuration/authentication.md:183-193`). On SaaS, a multi-tenant app is the expected setup.

**The flaw.**
- The email comes from Graph `/me`: `mail`, with a fallback to `userPrincipalName` (`microsoft_service.go:160-174`). Graph returns no verification flag.
- `mail` is a directory attribute. A tenant admin can set it to any address, and nobody checks that they own it. This is the 2023 "nOAuth" class. Microsoft's own mitigation (the `email` token claim with `xms_edov`) does not cover the Graph `mail` attribute. The "KNOWN LIMITATION" comment at `:162-170` talks about UPN duplicates, not takeover.
- `findOrCreateUser` (`microsoft_service.go:310-370`) works in three steps:
  1. It looks up the Microsoft ID (`:316-336`).
  2. On a miss, it calls `GetUserByEmail(email)` (`:339`).
  3. It writes `CreateUserProvider` for that user (`:360-367`). From then on, the attacker's Microsoft identity is **permanently** bound to the victim's account.
- `CompleteOrgLogin` (`join_policy.go:316-373`) then mints a full session, with no TOTP check (finding E1).

**The attack does not even need the victim's org slug.** Suppose the target org does not admit the user. The login is then pending, and `fallbackMemberSession` (`join_policy.go:380-403`) mints a normal session on an org the victim *is* a member of.

**The link is invisible and permanent.** No route lists or removes a user's sign-in links. The only `DeleteUserProvider` caller is the stale-link heal (`provider_links.go:118`). The victim is not notified.

**Creating an account is just as unsafe as linking one.**
- With no existing user, a new account is created with `EmailVerifiedAt = now` (`microsoft_service.go:345-358`).
- That email then drives admission:
  - an outstanding invitation for it is consumed (`join_policy.go:203-215`, rule 3);
  - `registration.email_pattern` auto-joins it (`:237-244`, rule 5);
  - so does `autoJoinMatchingOrgs` (`:329`).
- So an attacker can:
  - claim an invite sent to `alice@acme.com`;
  - auto-join acme's org;
  - or pre-register a victim's address before the victim signs up. The Microsoft link then survives the victim's later password reset.

**How the other connectors decide whether to link by email** (this fix must match them):

| Connector | Rule | Where |
|---|---|---|
| Google | reject the login unless `email_verified` | `google_service.go:161` |
| Discord | reject unless `verified` | `discord_service.go:196` |
| Slack | reject unless `email_verified` | `slack_service.go:325-331` |
| OIDC | reject unless `email_verified` is literally `true` | `oidc_service.go:270-281`, `:342-348` |
| GitHub | `/user` public email used as-is; `primary && verified` only on the `/user/emails` fallback | `github_service.go:159-172`, `:314` |
| GitLab | only checks that the email is non-empty | `gitlab_service.go:160-163` |
| SAML, LDAP | trust the admin-configured IdP or directory, by design | `saml_service.go:118-138`, `ldap_service.go:350-366` |
| Microsoft | no check at all | this spec |

**Corrections to the report.**
- `config.go:1449-1452` ("any installing tenant is accepted") documents the MS Teams **bot** (`MSTeamsConfig`), not Microsoft sign-in. The sign-in default is the `microsoft_oauth.go:8` comment and the docs above. The conclusion is unchanged.
- GitHub is "`Primary && Verified`" only on the fallback path. The 2026-10-15 report corrected this, and GitHub only publishes verified addresses, so there is no known exploit.
- GitLab has no verified check either. That is out of scope here; it needs its own spec.

## Proposal
1. **Read the ID token the token endpoint already returns.**
   - The scope already includes `openid email profile` (`microsoft.go:106`). Add an `IDToken` field (JSON `id_token`) to `MicrosoftTokenResponse` (`microsoft_service.go:45-52`).
   - Decode it with golang-jwt/v5 (already imported by `service.go:21`), using `jwt.NewParser().ParseUnverified`, into `{oid, tid, email, xms_edov, aud, exp}`. Skipping the signature check is sound here: the token comes over the back channel from the token endpoint, over TLS, in exchange for the client secret, which OIDC Core §3.1.3.7 (6) accepts in place of a signature check.
   - Still require `aud == cfg.Microsoft.ClientID`, an unexpired `exp`, and `oid ==` the Graph `id`. A mismatch is `ErrMicrosoftAPI`, with no link written.
2. **Decide whether the email is trusted**, in a small helper in `microsoft_service.go`:
   - **Pinned tenant:** `TenantID` is non-empty and not `common` / `organizations` / `consumers` (case-insensitive). Keep today's `mail` → UPN email and trust it. Only that tenant's users can get a token, and its admins are the operator's own, which is the same admin-established trust boundary SAML documents (`saml_service.go:118-138`). Say so in a comment.
   - **Multi-tenant:** trusted only when the ID token has a non-empty `email` and `xms_edov` is the JSON boolean `true`. Accept nothing else, as OIDC does for `email_verified` (`oidc_service.go:342-348`). In that case, use the ID-token `email`, not Graph `mail`, for the lookup and for account creation.
   - Anything else is untrusted.
3. **Gate only the email paths** in `findOrCreateUser` (`microsoft_service.go:310-370`):
   - Keep the Microsoft-ID lookup first (`:316-336`), so already-linked users keep signing in whatever their email status.
   - On a miss with an untrusted email, return `ErrEmailNotVerified` **before** `GetUserByEmail` (`:339`) and before the create branch (`:345-358`).
   - The effect: no link, no account, no session, and no invitation or auto-join consumed. This matches Google and OIDC, which also refuse outright rather than create.
4. **Error surface.**
   - `microsoft.go:135-137` maps the error to `EMAIL_NOT_VERIFIED`. Change its description (today "No email found in Microsoft profile") to say that Microsoft did not confirm the address.
   - Log a WARN with `tid` and `oid` (not the email) that names the missing `xms_edov` claim, so an operator can find the configuration fix.
5. **Comments.** Rewrite the "KNOWN LIMITATION" block (`microsoft_service.go:160-170`) to state the trust rule. Keep the UPN-duplicate note.
6. **Docs and changelog.**
   - In `web/docs/docs/configuration/authentication.md:178-200`, add that a multi-tenant registration (`common` / `organizations` / `consumers`) must add the `email` and `xms_edov` optional claims to the **ID token** (app registration, Token configuration). Without them, already-linked users still sign in, but first sign-ins are refused. A pinned tenant needs nothing.
   - Add a `CHANGELOG.md` entry: security fix and behaviour change.

## Tests
All in `server/internal/handlers/auth/microsoft_service_test.go`. They drive the real `HandleCallback` through the existing httptest harness (`testMicrosoftCallbackWithMockServersErr`, `:425-437`). The token stub returns an `id_token` signed with any HS256 key. Table-driven:
- **Regression, link.**
  - Setup: an existing local user `alice@acme.com` (password set, admin of an org). Tenant `common`. Graph returns a new `id` with `mail: alice@acme.com`.
  - Run it once for each `id_token` variant: none, no `xms_edov`, `xms_edov: false`, `xms_edov: "true"` (a string).
  - Expect `ErrEmailNotVerified`, no `user_providers` row for that Microsoft ID, and no refresh-token row for alice. This fails before the fix.
- **Regression, create and invite.**
  - Setup: no user `bob@acme.com`, but a pending invitation for him in an org. Tenant `common`, unverified email.
  - Expect `ErrEmailNotVerified`, `GetUserByEmail` still nil, and the invitation still present.
- **Regression, token binding.** An `aud` that is not our client ID, or an `oid` that differs from the Graph `id`: expect an error and no link.
- **Positive controls.**
  - `xms_edov: true` and `email: alice@acme.com` link to the existing alice and return tokens.
  - A pinned tenant (`TenantID` set to a GUID) with no `xms_edov` links as today.
  - An already-linked Microsoft ID with an unverified email still signs in.
- **Existing subtests.** Rewrite the subtests that pin the old behaviour by calling `findOrCreateUser` directly with no trust decision: "email-based user matching" (`:220-247`) and "new user creation" (`:249-266`). Each gets a trusted and an untrusted case.

## To verify
- Check how `xms_edov` behaves for personal Microsoft accounts (`tid` `9188040d-6c67-4c5b-b112-36a304b66dad`) and for B2B guests. Check that Entra emits it only once configured as an optional claim, and that it is a JSON boolean. Do this with a live test app before relying on the rule for `consumers`.
- Check whether the ID-token `email` differs from Graph `mail` for some users. If it does, an existing user matched today by `mail` would no longer match.
- Which `SP_MICROSOFT_TENANT_ID` prod and `solidping.k8xp.com` use. If it is unpinned, run a read-only query (from the `k8xp` repo) for `user_providers` rows of type `microsoft` whose user existed before the link (`users.created_at < user_providers.created_at`), to see whether this was ever exploited.

## Open questions
1. **Multi-tenant installs without the optional claim** will refuse every first Microsoft sign-in until the operator adds `xms_edov`. Already-linked users are unaffected. Recommended: accept this and fail closed, with the docs step and changelog entry above.
2. **Existing links that may have been made through an unverified email.** Recommended: no automatic unlinking. Publish the read-only query above in the changelog / an operations runbook so operators can review.
3. **Visibility of sign-in links.** Users cannot see or remove linked sign-in methods, and are not emailed when one is added. Recommended: a separate spec that covers every connector (a list and unlink action on the account page, plus a notification email).

## Related
E1 (2026-10-15): federated sign-in never asks for TOTP, which is what makes D1 TOTP-proof.
