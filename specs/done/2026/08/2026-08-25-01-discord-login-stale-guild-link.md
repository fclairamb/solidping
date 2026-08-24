---
model: opus
effort: high
---

# Discord login fails permanently with "sql: no rows in result set" when the guild's linked organization no longer resolves

## Problem

Discord login on production (`solidping.io`) is broken for at least one user. A HAR
capture from 2026-08-24 ~20:41 UTC (`failed_discord_auth.priv.har`, local only, not
committed) shows:

1. The Discord side of the flow completes normally — `GET`/`POST`
   `discord.com/api/v9/oauth2/authorize` both return 200, and Discord hands back
   `https://solidping.io/api/v1/auth/discord/callback?code=…&state=…`.
2. The callback then bounces the browser to the SPA with:
   `/dash0/orgs/default?error=OAUTH_FAILED&error_description=OAuth+failed:+failed+to+find/create+organization:+sql:+no+rows+in+result+set`
3. The SPA loads with no session — every org API call 401s and the user lands back
   on the login page.

The error text pins the failing statement precisely. In
[discord_service.go:371](server/internal/handlers/auth/discord_service.go:371),
`findOrCreateOrganization` returns the org lookup **bare** when a provider link
exists:

```go
orgProvider, err := s.db.GetOrganizationProviderByProviderID(
    ctx, models.ProviderTypeDiscord, guildID,
)
if err == nil && orgProvider != nil {
    return s.db.GetOrganization(ctx, orgProvider.OrganizationUID)   // ← line 371
}
```

Every other error path in that function wraps with a prefix
(`failed to get organization provider:`, `failed to create organization:`, …).
The observed message has **no** such prefix — only the `HandleCallback` wrapper
`failed to find/create organization:` — so the failure is exactly this line: the
`organization_providers` row for the guild exists and is live, but
`GetOrganization` on its `organization_uid` returns `sql.ErrNoRows`.

How that state arises: `organization_providers.organization_uid` has
`on delete cascade` ([001_v0_1_0.up.sql:100](server/internal/db/postgres/migrations/001_v0_1_0.up.sql:100)),
which only covers **hard** deletes. Both tables carry `deleted_at` — a
**soft-deleted** organization leaves its guild link alive, so the next Discord
login for anyone in that guild finds the stale link, fails the org fetch, and the
login is permanently bricked (no retry can ever succeed; the stale row wins the
`idx_org_providers_type_id` unique lookup every time).

Same latent pattern elsewhere:

- [discord_service.go:408](server/internal/handlers/auth/discord_service.go:408) —
  `findOrCreateUser` returns `s.db.GetUser(ctx, provider.UserUID)` bare after a
  `user_providers` hit.
- [slack_service.go:410](server/internal/handlers/auth/slack_service.go:410) —
  identical org-link pattern in the Slack flow.
- The other providers (github/gitlab/google/microsoft) should be swept for the
  same shape.

Two secondary issues visible in the HAR:

- **Raw internals leak to the user**: the redirect puts
  `sql: no rows in result set` verbatim in `error_description`, shown in the
  login-page URL/toast. Internal errors should map to a generic user-facing
  message and be logged server-side.
- **Error params compound on retry**: the retry's
  `/api/v1/auth/discord/login?redirect_uri=…` already carried the previous
  attempt's `error=OAUTH_FAILED&error_description=…` inside the `redirect_uri` —
  the SPA uses the current URL (errors included) as the return target, so failures
  nest URL-encoded into each other on every retry.

## Proposal

1. **Self-heal stale provider links.** In `findOrCreateOrganization` (Discord and
   Slack alike), when the provider link resolves but the org lookup returns
   `sql.ErrNoRows`, treat the link as stale: soft-delete the stale
   `organization_providers` row and fall through to the normal create path (the
   partial unique index `… where deleted_at is null` then permits the re-link).
   Log a warning with the guild/org UIDs so operators can see it happened.
   Deliberately do **not** resurrect the soft-deleted org — deletion was an
   explicit act; the guild gets a fresh org. (If resurrection is preferred,
   that's a product decision to surface, not a default.)
2. **Same treatment for user links**: `findOrCreateUser`'s `GetUser` after a
   `user_providers` hit gets the same stale-link fallback (clear link, fall
   through to email lookup / create). Sweep the remaining provider services for
   the bare-return pattern and wrap every error with context.
3. **Stop leaking internals**: in the callback error redirect, map unexpected
   errors to a generic `error_description` ("Sign-in failed, please try again")
   and keep the detailed error in server logs only. The existing typed errors
   (`ErrEmailNotVerified`, invalid state, …) keep their specific user-facing text.
4. **Frontend**: when building `redirect_uri` for a provider login, strip
   `error` / `error_description` from the current URL first so retries don't
   nest previous failures.
5. **Tests**: backend test reproducing the exact failure — create org + Discord
   guild link, soft-delete the org, run the callback path, assert login now
   succeeds with a freshly created org and the stale link is cleared; cover the
   guild-org path and the personal-org (`discord-user-<id>`) path, plus the Slack
   twin. Both SQLite and Postgres backends.

## Open questions

- Production also needs the **data fix** for the already-stale link (the spec fixes
  code; the affected guild's row heals itself on the user's next login attempt
  once deployed — verify that actually happens).
- Should `GetOrganizationProviderByProviderID` join on live orgs instead
  (filter `organizations.deleted_at is null` in the query)? That would prevent the
  class rather than heal it, but hides the stale row; healing + logging is more
  observable. Implementer may choose either as long as the login succeeds.

## Implementation Plan

### 0. Decision on the open question
Take the **heal + log** option (the spec's stated preference), not a
`join organizations on deleted_at is null` filter inside
`GetOrganizationProviderByProviderID`. Reasons: the DB layer stays a dumb
accessor (every other `Get*ByProviderID` is a plain row read), the stale row is
actively cleaned rather than made invisible, and the heal emits a
`WARN` line naming the guild/team + org UIDs so an operator can see it happened.
No migration, no manual SQL: the affected production row heals on the next
Discord login for that guild.

### 1. Shared healing helpers — `server/internal/handlers/auth/provider_links.go` (new)
Two package-level helpers, so all eight provider services share one
implementation instead of copy-pasting the fallback:

- `resolveLinkedOrganization(ctx, dbSvc, providerType, providerID, link) (*models.Organization, error)`
  - `GetOrganization(link.OrganizationUID)` → return it on success.
  - On `sql.ErrNoRows`: `slog.WarnContext` (provider type, provider id, org uid,
    link uid), `DeleteOrganizationProvider(link.UID)` (soft delete — the partial
    unique index `idx_org_providers_type_id … where deleted_at is null` then
    frees the `(provider_type, provider_id)` pair for the re-link), and return
    `(nil, nil)` meaning "caller falls through to the create path".
  - Any other error is wrapped and returned.
- `resolveLinkedUser(ctx, dbSvc, providerType, providerID, link) (*models.User, error)`
  - Same shape against `GetUser`; the stale row is removed with
    `DeleteUserProvider` (`user_providers` has no `deleted_at`, and its unique
    index `user_providers_provider_idx` is not partial, so it must be a hard
    delete for the re-link to be insertable). Returns `(nil, nil)` on heal so
    the caller falls through to email lookup / create.

If clearing the stale row itself fails, the wrapped error is returned rather than
falling through: the create path would otherwise trip the unique index and report
a confusing constraint violation instead of the real cause.

### 2. Wire the helpers into every provider service
- `discord_service.go` — `findOrCreateOrganization` (guild link, spec line 371)
  **and** `findOrCreateUser` (spec line 408).
- `slack_service.go` — `findOrCreateOrganization` (team link, spec line 410) and
  `findOrCreateUser` (line 443).
- Sweep of the bare `return s.db.GetUser(ctx, provider.UserUID)` after a
  `user_providers` hit: `github_service.go`, `gitlab_service.go`,
  `google_service.go`, `microsoft_service.go`, `oidc_service.go`,
  `saml_service.go`, `ldap_service.go`. Each also gains the missing
  `if err != nil && !errors.Is(err, sql.ErrNoRows) { return wrapped }` guard —
  today a real DB error on that lookup is silently swallowed into "create a new
  user".

### 3. Prevent the class at the source — `org_delete.go`
`DeleteOrg` soft-deletes the org but leaves its `organization_providers` rows
live; that is exactly how the production row went stale. Add a
`releaseOrgProviderLinks` step (list + soft-delete each link) alongside the
existing `ReleaseOrganizationPreviousSlugsForOrg` call. The heal in §1 stays —
it is what fixes rows already stale in the field.

### 4. Stop leaking internals to `error_description`
- `handler.go`: add `OAuthDescGenericFailure = "Sign-in failed, please try again"`
  and a shared `logOAuthFailure(req, provider, err) string` that emits
  `slog.ErrorContext` with the real error and returns the generic string.
- Replace the `default:` arm of every `handleOAuthError` /
  `handleSAMLError` (discord, github, gitlab, google, microsoft, oidc, slack,
  saml) — currently `fmt.Sprintf("OAuth failed: %v", err)`. The typed arms
  (`ErrInvalidOAuthState`, `ErrEmailNotVerified`, `ErrDiscordTokenExchange`,
  the SAML-specific ones …) keep their specific user-facing text.

### 5. Frontend — `web/dash0`
- `src/lib/login-destination.ts`: new `stripOAuthErrorParams(path)` that removes
  `error` and `error_description` from a relative path's query string (and drops
  a now-empty `?`), leaving every other param and the hash intact.
- `src/routes/orgs/$org/login.tsx:541`: wrap `currentPath` in it before
  `encodeURIComponent`, so a retry after a failure no longer nests the previous
  attempt's error params inside `redirect_uri`.
- Unit tests in `src/lib/login-destination.test.ts`.

### 6. Tests
Backend, in the `auth` package:
- `discord_service_test.go` (new). Discord's token/API URLs are compile-time
  constants, so — mirroring the Slack service, which already carries overridable
  `oauthURL`/`userInfoURL` fields — give `DiscordOAuthService` `tokenURL` /
  `apiBaseURL` fields defaulted to the constants, and point them at `httptest`
  stand-ins. That drives the **real** `HandleCallback`, not a re-implementation.
  Cases: stale guild-org link heals (login succeeds, fresh org, old link
  soft-deleted, new link live); stale link on the personal-org
  `discord-user-<id>` path (no guilds returned); stale `user_providers` link
  heals; happy-path idempotency control (a live link still returns the same org
  and creates no second one).
- `slack_service_test.go`: the Slack twin of the stale-org-link heal.
- The engine-agnostic bodies live in helpers taking a `db.Service`, so a new
  `provider_links_postgres_test.go` re-runs them on embedded Postgres
  (`portProviderLinksPG = 15501`, distinct from every port already claimed),
  skipping in `-short` mode like the other embedded-PG tests.
- `provider_links_test.go`: `handleOAuthError` with a wrapped
  `sql: no rows in result set` produces `error=OAUTH_FAILED` and an
  `error_description` that is exactly the generic message — asserting the raw
  string is absent (requirement 3), with a positive control that
  `ErrEmailNotVerified` still yields its specific text.

Frontend: vitest unit tests for `stripOAuthErrorParams`. No Playwright E2E — the
change is a pure URL transformation with no new rendered surface, and the local
`:4000` devloop is not in `SP_RUNMODE=test`.
