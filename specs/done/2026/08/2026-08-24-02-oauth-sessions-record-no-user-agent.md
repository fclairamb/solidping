---
model: sonnet
effort: medium
---

# Federated (OAuth/SSO) sessions are stored without a user agent, so the sessions page shows "Unknown device"

## Problem

On the account **Sessions** page (`/orgs/$org/account/sessions`), a session created by
signing in through an OAuth/SSO connector renders as:

```
🖵  Unknown device   [Current session]  [OAuth]
    Unknown
    Connected 2d ago
    Last active 44m ago
    Expires 8/30/2026
```

— no browser, no OS, no IP address, even though the login happened in a normal Chrome
browser that sent a perfectly ordinary `User-Agent` header. It should read something like
`Chrome 128 on macOS` with the desktop icon and the source IP.

The frontend is not at fault. [account.sessions.tsx:84](web/dash0/src/routes/orgs/$org/account.sessions.tsx:84)
parses `session.createdWith?.userAgent` with [parseUserAgent](web/dash0/src/lib/user-agent.ts:59)
and falls back to `sessions.unknownDevice` / `sessions.unknownAgent` only when that string is
empty — which it is, because the backend never recorded it.

**Root cause.** Every federated callback funnels through `CompleteOrgLogin`, which mints the
session with an **empty** `auth.Context`:

- [join_policy.go:261](server/internal/handlers/auth/join_policy.go:261) —
  `s.GenerateTokensForOAuth(ctx, user, org, role, resolved.method, Context{})`

The comment just above it explains that the request-meta middleware already parked the source
IP and user agent on `ctx` — and that is true for the **audit** event, which reads them back via
`audit.ActorFromContext` in [audit.go:38](server/internal/handlers/auth/audit.go:38). But the
session row's `properties.created_with` is built from the *explicit* `Context` argument:
[service.go:2170](server/internal/handlers/auth/service.go:2170) calls `authContext.ToMap()`,
and `ToMap` ([service.go:222](server/internal/handlers/auth/service.go:222)) unconditionally
writes `userAgent: ""` / `remoteAddr: ""`. So the row is persisted with both fields blank, and
`extractCreatedWith` ([service.go:1690](server/internal/handlers/auth/service.go:1690)) faithfully
returns them empty to the API.

This affects **every** federated connector, since they all share that tail:
[google_service.go:188](server/internal/handlers/auth/google_service.go:188),
[github_service.go:197](server/internal/handlers/auth/github_service.go:197),
[gitlab_service.go:188](server/internal/handlers/auth/gitlab_service.go:188),
[microsoft_service.go:204](server/internal/handlers/auth/microsoft_service.go:204),
[discord_service.go:229](server/internal/handlers/auth/discord_service.go:229),
[slack_service.go:380](server/internal/handlers/auth/slack_service.go:380),
[oidc_service.go:306](server/internal/handlers/auth/oidc_service.go:306),
[saml_service.go:563](server/internal/handlers/auth/saml_service.go:563),
[integrations/slack/service.go:465](server/internal/integrations/slack/service.go:465).

Password, 2FA and passkey logins are fine — their handlers build the `Context` from the request
([handler.go:119](server/internal/handlers/auth/handler.go:119),
[handler.go:1192](server/internal/handlers/auth/handler.go:1192),
[passkey_handler.go:137](server/internal/handlers/auth/passkey_handler.go:137)).

Two adjacent paths have the same blank-context defect:

- **Registration** — [service.go:2427](server/internal/handlers/auth/service.go:2427) writes
  `created_with: {method: "registration"}` only, then `startSession(..., Context{})`.
- **Invitation acceptance** — [service.go:3396](server/internal/handlers/auth/service.go:3396),
  same shape with `method: "invitation"`.

And a smaller, separate display gap: `methodLabel`
([account.sessions.tsx:66](web/dash0/src/routes/orgs/$org/account.sessions.tsx:66)) only knows
`password` / `oauth` / `passkey`. A session recorded as `google`, `github`, `slack`, `oidc`,
`saml`, `registration` or `invitation` — all real values emitted by `authMethods()`
([audit.go:180](server/internal/handlers/auth/audit.go:180)) — renders **no badge at all**.

## Proposal

### 1. Capture the request provenance on every session row (backend, the actual fix)

The middleware already has the data on `ctx` for every request
([middleware/audit.go:23](server/internal/middleware/audit.go:23), wired globally at
[server.go:590](server/internal/app/server.go:590)). Read it back instead of persisting blanks.

Add a helper in the `auth` package — mirroring what `auditActorCtx` already does:

```go
// contextFromRequestMeta falls back to the request provenance the audit
// middleware parked on ctx when the caller had no explicit Context (every
// federated callback: it is several frames away from the *http.Request).
func contextFromRequestMeta(ctx context.Context, explicit Context) Context {
    actor := audit.ActorFromContext(ctx)
    if explicit.UserAgent == "" {
        explicit.UserAgent = actor.UserAgent
    }
    if explicit.RemoteAddr == "" {
        explicit.RemoteAddr = actor.SourceIP
    }
    return explicit
}
```

Apply it at the point where `created_with` is built, so **all** session-minting paths inherit
the fix rather than each connector remembering to thread a `Context`:

- `GenerateTokensForOAuth` ([service.go:2170](server/internal/handlers/auth/service.go:2170)) —
  resolve `authContext` through the helper before `ToMap()` and before `startSession`.
- The registration ([service.go:2427](server/internal/handlers/auth/service.go:2427)) and
  invitation ([service.go:3396](server/internal/handlers/auth/service.go:3396)) session rows —
  build `created_with` from the resolved context plus their `method`, and pass the resolved
  context to `startSession` so the audit event carries it too.

Prefer an explicitly-passed non-empty value over the ctx fallback (same precedence as
`auditActorCtx`), so nothing that already works changes behavior.

Also make `ToMap` omit empty fields rather than writing `"userAgent": ""` — a row with genuinely
no provenance should be indistinguishable from a pre-feature row, and `extractCreatedWith`
should be able to return `nil` for it instead of an all-empty struct.

While here, consider recording the same provenance on the OAuth 2.1 **grant** rows minted by
[oauth/service.go:433](server/internal/oauth/service.go:433) (`mintTokens`) — those `oauth_refresh`
rows carry no `created_with` at all today, so an MCP/CLI grant listed on the same page is
likewise a blank "Unknown device". Recording at least the method (`oauth`) and the client's UA
makes the row identifiable. This is a *nice-to-have*; if it complicates the change, note it and
leave it out rather than half-doing it.

### 2. Label every auth method the backend can emit (frontend)

Extend `methodLabel` + `web/dash0/src/locales/*/account.json` to cover the full
`authMethods()` set — `password`, `ldap`, `passkey`, `oauth`, `google`, `github`, `gitlab`,
`microsoft`, `discord`, `slack`, `oidc`, `saml`, `invitation`, `registration`, `switch_org`,
`org_session` — plus the `"<base>+totp"` / `"<base>+recovery_code"` second-factor suffixes
(`SecondFactorTOTP`, [audit.go:163](server/internal/handlers/auth/audit.go:163)): split on `+`
and label the base, optionally showing the second factor. Unknown values should fall back to a
readable form of the raw string, never to no badge at all.

**Add locale keys for all six locales** — a new key present only in `en` is a regression the
dash0 unit tests catch.

### 3. Tests

- Backend: a table test over the session-minting paths asserting `properties.created_with`
  carries the UA/IP parked on ctx — with a **positive control** (a request whose ctx has UA/IP →
  they land in the row) and a **negative control** (no request meta on ctx → the fields are
  omitted, not written as `""`). Cover at least one federated connector end-to-end through
  `CompleteOrgLogin`, plus registration and invitation.
- Frontend: unit-test `methodLabel` over the full method list, including a `+totp` suffix and an
  unknown value.
- E2E (`web/dash0/e2e/`): if a session fixture can be minted with a known UA, assert the row
  renders the parsed device label rather than "Unknown device".

### Note on existing rows

This is not backfillable — a session already stored with a blank UA stays "Unknown device"
until the user signs in again. That's acceptable; do **not** invent a migration that guesses.
