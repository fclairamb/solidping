---
model: opus
effort: high
pr: batch
automerge: false
---

# SSO providers saved in Server Settings > Authentication are not live until a restart

GitHub Issue: https://github.com/fclairamb/solidping/issues/1027

## Source

Jared-Is-Coding, v0.54.1, self-hosted Docker + PostgreSQL. He configured the Generic OIDC
provider on `/d/orgs/default/server/auth`, enabled it and saved. Then:

1. Switching local login off fails with `invalid parameter value: local login cannot be
   disabled while no SSO provider is configured and enabled`.
2. The login page shows no OIDC button.

Florent replied that he would look into the OIDC part, and that disabling local login must be
possible as soon as at least one other mechanism is defined (linked to #1026).

The report is right on both symptoms; they share one root cause below. The lock-out guard
itself (spec 2026-11-21-02) is correct: it only sees a stale config.

## Problem

Every SSO setting stored from the dashboard is a system parameter (`auth.oidc.*`,
`auth.google.*`, `auth.saml.*`, … in `server/internal/systemconfig/systemconfig.go:143`). Their
`ApplyFunc` overlays are run on `*config.Config` **only once, at boot**, by
`systemconfig.Service.Initialize` (`server/internal/systemconfig/systemconfig.go:1601`, called
from `server/internal/app/server.go:4568`). `handlers/system.Service.SetParameter`
(`server/internal/handlers/system/service.go:317`) writes the DB row and returns: nothing
re-applies it.

So, after saving the OIDC card without restarting:

- `config.Config.SSOProviders()` (`server/internal/config/sso_providers.go:19`) still sees
  `OIDC.Enabled=false`. It feeds both `GET /auth/providers`
  (`server/internal/handlers/auth/providers_available.go:66`), hence no button, and
  `systemconfig.ValidateLocalLoginParameter` (`server/internal/systemconfig/local_login.go:85`),
  hence the error.
- The `/auth/oidc/login` and `/auth/oidc/callback` routes are only mounted when the boot
  config has OIDC enabled (`server/internal/app/server.go:1094`); same for Google, GitHub,
  Microsoft, GitLab, Discord, Slack and SAML just above and below it. Even with a fresh
  `SSOProviders()`, the button would lead to a 404 until a restart.
- The page copy is wrong: `server:auth.helpText`
  (`web/dash0/src/locales/en/server.json:91`) says "The Enabled toggle takes effect immediately
  for the login page once saved". It does not, for any provider.
- Minor inconsistency: the OIDC route guard (`server.go:1094`) does not require the client
  secret, `OIDCOAuthConfig.Configured()` (`server/internal/config/oidc_oauth.go:62`) does.

After a restart the reporter's setup should work, if issuer URL, client ID and secret are all
set (to confirm in the test below).

## Why a spec

- Correctness hinges on concurrency and authorization scope: the auth config is read by every
  login request while a super admin rewrites it.
- Cross-cutting: systemconfig, the system parameter handler, route mounting of eight
  providers, the providers endpoint, the local-login guard and the dashboard copy.

## Proposal

1. **Live SSO config.** After a successful `SetParameter` / `DeleteParameter` on an `auth.*`
   key, rebuild the SSO part of the config from env > DB > startup config (the same precedence
   as `Initialize`; `EffectiveEmailConfig` at `systemconfig.go:1814` already builds a temporary
   config this way) and publish it atomically (`atomic.Pointer` snapshot of the provider
   configs, or a resolver function like `SetLocalLoginResolver`). Readers
   (`SSOProviders`, the OAuth/OIDC/SAML services, `ValidateLocalLoginParameter`) read the
   snapshot, never the boot struct mutated in place. Multi-instance deployments: each instance
   re-resolves per request or on a short TTL (no in-process notification reaches the others);
   pick one and document it.
2. **Routes always mounted.** Mount `/auth/<provider>/login|callback` unconditionally; the
   handler answers 404 (or redirects to the login page with an error code) while the provider
   is not configured in the current snapshot. Align the OIDC check on `Configured()` (client
   secret required).
3. **Local login guard.** Keep `ValidateLocalLoginParameter`, now fed by the live snapshot.
   In the dashboard, disable the local-login switch while no SSO provider is configured and
   enabled, with a hint saying why, instead of letting the save fail.
4. **Copy.** Rewrite `server:auth.helpText` in the five locales to match the new behaviour
   (or, if the open question below goes the other way, say that a restart is required for
   every change).
5. **Tests.** Go: save the OIDC parameters through the system service, then assert
   `GET /auth/providers` lists `oidc`, `/auth/oidc/login` redirects to the issuer, and
   `auth.local_login.enabled=false` is accepted; disable OIDC again and assert the reverse.
   Run it under `-race` with concurrent `ListProviders` calls. Playwright: configure a provider
   on the auth page, sign out, see its button.

## Open questions

1. Live reload or an honest "restart required"? **Recommended: live reload**, which is what
   the page already promises and what an operator expects from a settings screen. Trade-off:
   a restart banner is a one-file change, but leaves the lock-out guard and the login page
   inconsistent with what was just saved until the restart.

## Resolved open questions

1. Apply SSO settings live, without a restart: saving re-applies them to the running server, the OIDC routes are always mounted, and the lock-out guard and the login page reflect what was just saved. (autopilot: recommended answer)

## Closing the issue

This spec closes #1027. The implementing PR body **must** carry a `Closes #1027` line so the
squash-merge closes it. When the spec is archived to `specs/done/`, verify the issue is
closed and, if the merge did not close it, close it by hand:

    gh issue close 1027 --comment "Implemented by <PR or commit>; spec: <archived spec path>"
