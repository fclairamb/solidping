---
model: opus
effort: high
---

# Ask for the TOTP code on SSO / OAuth sign-in, through the existing 2FA temp-token step

## Problem
Found by the security scan of 2026-10-15 (finding E1). **Low on its own; it amplifies D1.** Users who enable TOTP get no second factor on any federated sign-in.
- **Who can trigger it:** anyone who controls the victim's account at an enabled identity provider: Google, GitHub, GitLab, Microsoft, Discord, Slack, OIDC or SAML. Any deployment mode.
- **The D1 case:** with D1 (Microsoft "nOAuth", 2026-10-14), anyone on SaaS who owns an Entra tenant. D1 becomes a TOTP-proof takeover.
- **Why it matters:** a password reset by email alone would still stop at the TOTP prompt. "Sign in with Google" does not.

**Where TOTP is and isn't checked.**
- **Password login:** checked once, after the password. `Login` returns `requires2Fa` and a temp token (`server/internal/handlers/auth/service.go:698-714`).
- **LDAP:** goes through the same `Login`, so it is gated as well (`:681-690`, then `:699`).
- **Passkeys:** skip it on purpose, with a comment saying why (`passkey_service.go:404-407`, "a passkey is already MFA-equivalent").
- **Every federated callback:** ends in `CompleteOrgLogin` (`join_policy.go:316-373`), which mints a full session through `GenerateTokensForOAuth` (`service.go:2331-2391`). It never looks at `user.TOTPEnabled`, and there is no comment explaining the skip. The pending branches mint without the check too:
  - `fallbackMemberSession` (`join_policy.go:380-403`): a full session on another org;
  - `pendingSession` (`:458-474`): an org-less access token.
- **Callers:** `google_service.go:181`, `github_service.go:190`, `gitlab_service.go:181`, `microsoft_service.go:197`, `discord_service.go:230`, `slack_service.go:371`, `oidc_service.go:299`, `saml_service.go:556`, and the Slack app install (`integrations/slack/service.go:490`).

**What the docs promise.** `web/docs/docs/configuration/authentication.md:326`: "At login, users with 2FA enabled provide a code from their authenticator app (or a recovery code)". The sentence makes no SSO exception.

**Why a fix fits the existing machinery.**
- `TwoFAClaims` already carries the org, the role and the first-factor method (`service.go:413-425`).
- `Verify2FA` / `Recovery2FA` (`:4608-4714`) finish through `completeLoginAfter2FA` (`:4747`), which mints the session on the org and role named in the claims. The audit method becomes `google+totp` and so on (`withSecondFactor`, `audit.go:260-266`).
- The handoff exchange already answers in the `LoginResponse` shape, which has `Requires2FA` and `TempToken` fields (`service.go:399-411`, `handoff_exchange.go:30-40`).
- The dashboard's `applyLoginResponse` already recognizes `requires2Fa` (`web/dash0/src/contexts/AuthContext.tsx:394-401`).
- **One trap.** If the server sent such a response today, `/auth/complete` would dereference `data.user.mustChangePassword` (`web/dash0/src/routes/auth.complete.tsx:75-79`) and show "exchange failed". So the server and dashboard changes must ship together.

**Dependency on H1 (2026-10-11).** Until H1 is fixed, the 2FA temp token is itself accepted as an access token: `ValidateToken` (`service.go:1483-1506`) checks neither purpose nor type. Handing a temp token to the browser would then protect nothing.

## Proposal
0. **Land after the C2/H1 token-type fix (`specs/todos/2026-10-16-02-token-purpose-separation.md`), or together with it.** The H1 regression test below pins this.
1. **Gate in `CompleteOrgLogin`** (`join_policy.go:316-373`).
   - Keep admission as it is (`JoinOrgViaLogin` and `autoJoinMatchingOrgs`). Then resolve the session the login would get:
     - the target org and its role;
     - for a pending login, the fallback member org and its role, picked by `fallbackMemberOrg` (`:413-452`);
     - org-less (empty slug and role) when the user belongs to no org.
   - Split `fallbackMemberSession` (`:380-403`) into "choose" and "mint" so the gate sits between them.
   - When `user.TOTPEnabled`, mint **no** token. Call `generate2FATempToken(user.UID, slug, role, resolved.method)` (`service.go:4453`), and return a `ProviderLoginResult` with `Requires2FA: true`, `TempToken`, plus the unchanged `Pending` / `FallbackOrgSlug` / `PendingOrgSlug`.
   - Comment that passkeys are the only sign-in that skips TOTP, and why.
   - With an empty slug, `completeLoginAfter2FA`'s no-org branch (`service.go:4783-4797`) yields the same org-less token `pendingSession` gives today.
2. **Carry the challenge through the handoff.**
   - Add `Requires2FA bool` and `TempToken string` to:
     - `ProviderLoginResult` (`join_policy.go:63-85`);
     - `ProviderOutcome` (`:810-828`);
     - `authhandoff.Session` (`server/internal/authhandoff/authhandoff.go:71-86`). The session is AES-GCM sealed and single use, with a 60 s TTL (`:47`); the temp token lives 5 min (`service.go:4436`).
   - Add a helper `(*ProviderLoginResult).Outcome(orgSlug, userUID string) *ProviderOutcome`. Switch the eight `&…OAuthResult{…}` literals to it (`google_service.go:187`, `github_service.go:196`, `gitlab_service.go:187`, `microsoft_service.go:203`, `discord_service.go:236`, `slack_service.go:377`, `oidc_service.go:305`, `saml_service.go:562`), so no connector can drop the flag.
   - The Slack install copies the fields by hand twice (`integrations/slack/service.go:532-542`, `handler.go:225-234`). Add the two fields there too.
   - `handoffSession` (`join_policy.go:834-851`): copy both fields, and leave the access and refresh tokens empty.
   - `RedirectWithHandoff` (`:870-885`): skip `setAccessTokenCookie` (`:881`) when there is no access token.
   - `authhandoff.Issue` (`:92`) and `Redeem` (`:171`): accept a session that has **exactly one** of `AccessToken` / `TempToken`. Today `Redeem` rejects an empty `AccessToken`.
   - `ExchangeHandoffCode` (`handoff_exchange.go:49-90`): for a 2FA session, return `requires2Fa`, `tempToken`, `returnTo` and `membershipPending`, with **no** user or organization info, the same disclosure as the password path (`service.go:710-713`).
   - `ExchangeHandoff` (`:117-124`): set no cookie when there is no access token.
   - `Verify2FA`, `Recovery2FA`, their handlers and their per-user throttle stay unchanged. The `/2fa/verify` handler sets the cookie once the code is accepted (`handler.go:1380`).
3. **Dashboard.**
   - In `web/dash0/src/routes/auth.complete.tsx:73-110`: when the exchange returns `requires2Fa` and `tempToken`, keep `returnTo` / `membershipPending` in state and show the code step instead of landing. On success, feed the `verify2FA` / `useRecoveryCode` response to `applyLoginResponse`, then land through `resolveHandoffLanding` with the kept values.
   - Extract the TOTP / recovery form from `web/dash0/src/routes/orgs/$org/login.tsx` (state `:134-135`, routing `:290-293`, submit `:470-497`, form `:661-720`) into one shared component, for example `web/dash0/src/components/shared/two-factor-challenge.tsx`, and use it on both pages. Reuse the `twoFactor.*` strings, including the 429 message. Add the component to the design reference if it is reusable (root `AGENTS.md` rule).
   - In `web/dash0/src/lib/auth-handoff.ts:25-46`, make `user` optional and add `requires2Fa` and `tempToken`.
4. **Docs and changelog.**
   - In `authentication.md:320-327`, state that the code is asked on every sign-in: password, LDAP and every SSO / OAuth connector. Only passkeys skip it, because a passkey is already a second factor.
   - Add a `CHANGELOG.md` entry.

## Tests
- **Gate unit tests** (`server/internal/handlers/auth/join_policy_test.go`), table-driven over four outcome shapes: admitted member, super admin, pending with a fallback org, pending with no org. For each, with `TOTPEnabled`:
  - `Requires2FA` is true, and the access and refresh tokens are empty;
  - `validate2FATempToken` yields the slug, role and method the session would have had;
  - no `refresh` row is created for the user.
  - This fails before the fix.
  - **Positive control:** the same table without TOTP returns today's tokens.
- **Real connector, end to end** (`oidc_service_test.go`, using `fakeOIDCIdP` at `:48-160`). A TOTP user signs in through `HandleCallback` and gets the challenge.
  - `Verify2FA` with `totp.GenerateCode` gives a full session on the org, and `auth.login_succeeded` records `oidc+totp`.
  - A wrong code gives `ErrInvalid2FACode` and no session.
  - A recovery code works too.
- **Handoff** (`handoff_callback_test.go`). `finishProviderCallback` with a 2FA outcome redirects with a code and sets no session cookie. `ExchangeHandoffCode` then returns `requires2Fa` and `tempToken`, with no access token and no user, and `ExchangeHandoff` sets no cookie.
  - In `server/internal/authhandoff/authhandoff_test.go`, add cases to `handoffCases()`, which run on both engines:
    - a temp-token-only session round-trips;
    - a session with neither token is rejected;
    - a session with both tokens is rejected.
- **H1 dependency.** The temp token from a federated callback is refused as a bearer token: `GET /api/v1/auth/me` returns 401. This stays red until H1 is fixed.
- **Controls.**
  - Passkeys need no new test: `FinishLogin` calls `completeLogin` directly (`passkey_service.go:404-407`), not `CompleteOrgLogin`, so the gate cannot reach it. No unit test drives `FinishLogin` today, and none is needed for this change.
  - An LDAP login of a TOTP user returns `requires2Fa` (`ldap_service_test.go`; no such test exists today).
- **Playwright** (`web/dash0/e2e/sso-handoff.spec.ts`; skips without the fake IdP, like its OIDC cases). Enable TOTP for an account, as `login-2fa.spec.ts` does, then sign in through the fake OIDC IdP.
  - `/d/auth/complete` shows `2fa-login-code`.
  - A valid code lands on the org.
  - No URL carries a token.

## To verify
- That the auth-method catalog test (`login_audit_test.go:318-320`) accepts `<connector>+totp` and `<connector>+recovery_code`. `audit.go:204-206` says second factors are suffixes, not values of their own.
- Slack app install (`integrations/slack/handler.go:176-245`). The bot connection is written right after `CompleteOrgLogin` (`service.go:496-510`), so with the gate it is written before the code is entered. That already happens today for a pending installer. For org-scoped installs, the installer was already signed in when calling `install-url`. Check that the public marketplace `Install` (`handler.go:112`) adds no path where a TOTP user's install writes org data that it should not before the code.

## Open questions
1. **Enforce TOTP on federated sign-in, or document the skip as deliberate?** Recommended: **enforce.**
   - The docs already promise it, and TOTP is a per-user opt-in, so only users who asked for it are affected.
   - A compromised IdP account, and D1, are exactly what a second factor is for.
   - Documenting the skip would mean telling users their TOTP does not protect SSO sign-ins.
2. **Trust the IdP's own MFA instead** (OIDC `amr` containing `mfa`, SAML `AuthnContextClassRef`)? Recommended: not in this spec. Always ask when the user enabled TOTP. An org-level "trust IdP MFA" option belongs with backlog spec `2026-09-23-03-org-enforced-sso-provider`.
3. **Admission happens before the code.** Membership requests, invitation consumption and auto-join run before the code is checked, as they already do for LDAP. Recommended: keep it so. The membership goes to the real user, and no session is issued without the code.

## Resolved open questions
Decided 2026-10-16 (all recommendations accepted):
1. Enforce TOTP on every federated sign-in for users who enabled it.
2. Do not trust IdP MFA (`amr` / `AuthnContextClassRef`) in this spec; that belongs with backlog spec `2026-09-23-03-org-enforced-sso-provider`.
3. Keep admission (membership requests, invitation consumption, auto-join) before the code check, as for LDAP; no session is issued without the code.

## Related
D1 (2026-10-14, Microsoft email linking, which this turns from TOTP-proof into TOTP-gated), H1 and C2 (2026-10-11, token-type confusion; a prerequisite).
