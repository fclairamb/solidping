---
model: opus
effort: high
pr: own
---

# GitLab sign-in must not link or create an account from an unverified email

## Problem

From security advisory GHSA-c7jr-53g7-65q8.

**Severity: High.** An unauthenticated attacker can take over an existing account, under two conditions:
- the deployment has GitLab sign-in enabled (it is off by default);
- the configured GitLab instance lets an account carry an email its owner never confirmed. GitLab's self-managed email confirmation can be `off` or `soft`, and an instance admin can set any address on any account.

**The flaw.**
- GitLab sign-in trusts the `email` field of `/api/v4/user` with no verification signal.
  - `GitLabUserInfo` decodes no verification field (`server/internal/handlers/auth/gitlab_service.go:36-44`).
  - The only gate is `userInfo.Email == ""` (`gitlab_service.go:161-163`).
  - The handler still reports that case as "No verified email found in GitLab profile" (`gitlab.go:130-132`).
- `findOrCreateUser` (`gitlab_service.go:280-345`) works in three steps:
  1. It looks up the GitLab user ID.
  2. On a miss, it calls `GetUserByEmail(email)` (`:312`).
  3. It writes `CreateUserProvider` (`:336-342`). This binds the GitLab identity to that user permanently, with no notice.
- With no existing user, it creates one with `EmailVerifiedAt = now` (`:318-330`). That address then drives admission in `CompleteOrgLogin` (`:181`, `join_policy.go`): invitations for it are consumed, and email-domain auto-join applies.
- Every other connector requires a verification signal: Google `email_verified`, Discord `verified`, Slack, Microsoft (fixed 2026-10-16, spec `2026-10-16-01`), OIDC `email_verified`, and GitHub through its verified-email list. GitLab is the only gap.

**Impact.**
- The attacker's later GitLab sign-ins land in the victim's account: all of their organizations, at their role.
- A victim with TOTP is still asked for it (federated sign-in TOTP, spec `2026-10-16-14`). A victim without TOTP is taken over directly.
- A new account is admitted into the organizations whose invitations or domain rules match the claimed address.

**Affected:** every release with GitLab sign-in, up to v0.47.0. It is not fixed on main at `e59310e0`.

## Proposal

1. **Require a verification signal** (`gitlab_service.go`).
   - Decode `confirmed_at` from `/api/v4/user` into `GitLabUserInfo`, and refuse with `ErrEmailNotVerified` when it is null or absent.
   - Also fetch `/api/v4/user/emails`, and require the primary `email` to be listed there with a non-null `confirmed_at`. A response without that field is treated as unverified.
   - This needs the `read_user` scope, which the flow already requests. Check it under To verify.
2. **Do not link an existing local account by email on a first GitLab sign-in** (`findOrCreateUser`, `:310-342`).
   - On an email match with no provider link, refuse with a new `ErrGitLabLinkRequiresSignIn`, mapped to an OAuth error code such as `account_exists_sign_in_first`.
   - The user links GitLab from their account settings while signed in, the same way as the other providers' link flow, if one exists (To verify). Otherwise, the refusal message tells them to sign in with their existing method.
   - This is the Microsoft precedent (spec `2026-10-16-01`): the GitLab instance admin controls emails, so a verified flag alone is not proof that the address belongs to the SolidPing account owner.
3. **Only mark a new account verified when step 1 passed.** It always has, once step 1 refuses otherwise. Keep the assertion explicit in the code.
4. **Email the account owner when a sign-in provider is linked to their account.** Reuse the Discord link notice (`server/internal/handlers/auth/discord_link_notice.go:27`) for GitLab.
5. **Fix the error text at `gitlab.go:130-132`** so it matches the new checks.
6. **Docs:** in `web/docs/docs/configuration/authentication.md` (GitLab section, `:207`), say that SolidPing requires a confirmed GitLab email and that an existing account must be linked while signed in. Add a `CHANGELOG.md` entry under Security, with no exploit detail.

## Tests

`server/internal/handlers/auth/gitlab_service_test.go`, with a fake GitLab server (`httptest`) as the existing `setupGitLabTestService` does:
- **Regression (fails on main today):**
  - an unconfirmed profile (`confirmed_at: null`) whose email matches an existing local user creates no `user_providers` row, and the callback refuses with the email-not-verified code;
  - a confirmed profile whose email matches an existing local user, with no existing link, is refused with the sign-in-first code, and creates no `user_providers` row.
- **Unverified email entry:** a confirmed account whose primary email is not a confirmed entry of `/user/emails` is refused.
- **Positive controls:**
  - a confirmed profile with no matching local user creates an account with `EmailVerifiedAt` set and signs in;
  - a GitLab identity that is already linked keeps signing in.
- **Admission:** an unconfirmed profile whose email has an outstanding invitation consumes no invitation.

## To verify

- The exact fields GitLab returns on `/api/v4/user` and `/api/v4/user/emails` for the token's own user, for both GitLab.com and self-managed (`confirmed_at` per email). Keep the check fail-closed when a field is missing.
- Whether a signed-in "link a sign-in provider" flow exists for any connector today. If none does, the refusal in step 2 only points to the existing sign-in method, and linking from settings stays out of scope.
