---
model: opus
effort: high
---

# Session replay sends OAuth access and refresh tokens to PostHog

## Problem

Every federated login lands the browser on a dashboard URL that carries the
session in the query string:

```
/d/orgs/<slug>?access_token=…&expires_in=…&org=…&refresh_token=…
/d/no-org?access_token=…&expires_in=…&membershipPending=…
```

The redirect is built by `buildSuccessRedirect` in every provider handler
(`server/internal/handlers/auth/{google,github,gitlab,microsoft,discord,slack,oidc,saml}.go`)
and by `pendingMembershipRedirect` in
[join_policy.go](server/internal/handlers/auth/join_policy.go).

`main.tsx` strips the params with `history.replaceState` before React mounts
([oauth-handoff.ts](web/dash0/src/lib/oauth-handoff.ts)), so `$pageview`
events are clean: a HogQL count of events whose `$current_url` contains
`access_token=` returns 0.

Session replay is a different story. The `rrweb/network@1` plugin records the
document's navigation entry, and that entry keeps the original URL. Replay is
fully unmasked on purpose ([analytics.ts](web/dash0/src/lib/analytics.ts)), so
the URL goes to PostHog as-is. Checked on 2026-09-25 against the 05:59 UTC
sessions:

- a Google login recorded `…/orgs/default?access_token=…&expires_in=…&org=…&refresh_token=…`
- an org-less Google login recorded `…/no-org?access_token=…&expires_in=…&membershipPending=…`

Refresh tokens do not rotate (see [token-refresh.ts](web/dash0/src/lib/token-refresh.ts)),
so every recorded OAuth/SSO login since replay was turned on has probably put a
long-lived credential in PostHog. Anyone with read access to the PostHog project
can replay it and take over the session.

The masking decision in `analytics.ts` was about UI content (typed values,
clicked text). It was never meant to cover credentials, and nothing in the
current setup keeps them out.

## Proposal

### 1. Redact credentials from replay network capture (dash0)

In `initAnalytics`, pass
`session_recording.maskCapturedNetworkRequestFn`. It rewrites the request
`name`/URL, replacing the value of these query params with `REDACTED`:
`access_token`, `refresh_token`, `token`, `code`, `state`, `tempToken`,
`id_token`. Apply it to every captured request, including the navigation entry.
Also drop `Authorization` from captured request headers, if header capture is
ever turned on.

Put the redaction in a small pure function (`redactUrl`) with unit tests, so
the param list is easy to extend.

Keep the rest of the replay unmasked. This is a credentials filter, not a
return to masking.

### 2. Same filter on event properties

Add a `before_send` hook that applies the same `redactUrl` to `$current_url`,
`$referrer`, `$initial_current_url` and `$initial_referrer`. Today these are
clean only because `main.tsx` runs first. Don't depend on that ordering.

### 3. Remediation of what is already stored (operator steps)

Record these in the spec's implementation notes, and run them once part 1 is
deployed to prod:

1. Revoke every refresh token issued by a federated login before the deploy.
   Simplest option: revoke all sessions created through a provider callback
   since session replay was first enabled on prod, and let those users sign
   in again. Write down the exact SQL/command used.
2. Ask the operator whether to delete the affected recordings in PostHog.
   Once the tokens are revoked they are harmless, so deleting them is optional
   cleanup, not the fix.

Do not delete anything in PostHog automatically.

### 4. Fix it for good

Moving tokens out of the URL is spec `2026-09-25-12`. This spec is the
immediate stopgap, and it stays useful after that spec lands, for any other
token-bearing URL.

## Tests

- `redactUrl` unit tests: each listed param redacted, other params and the
  fragment untouched, relative URLs, URLs with no query, repeated params.
- `analytics.test.ts`: `initAnalytics` passes a `maskCapturedNetworkRequestFn`
  and a `before_send`. Fed a navigation entry for
  `/d/orgs/acme?access_token=a&refresh_token=b&org=acme`, the result contains
  neither `a` nor `b`, and still contains `org=acme` (positive control).
- Manual check on dev (`solidping.k8xp.com`): do a Google login, download the
  recording's snapshots from the PostHog API, and grep for `refresh_token=`
  followed by anything other than `REDACTED`. Expect zero.

## Implementation Plan

### Where the leak goes through (verified in posthog-js 1.409.5)

- `session_recording.maskCapturedNetworkRequestFn` is called for every
  network entry the `rrweb/network@1` plugin records, **including the
  navigation entry** (whose `name` keeps the original, pre-`replaceState`
  URL). posthog-js also routes the rrweb Meta event `href` and
  `$url_changed` / `$pageview` replay custom events through the same function
  (called with a partial `{ name: url }`), so one hook covers every URL replay
  stores. Returning `null`/`undefined` drops the entry, so the hook must always
  return the (redacted) request.
- `before_send` runs on every captured event (array form supported, run in
  order). The initial-URL properties live in `$set_once`/`$set`, not only in
  `properties`.
- posthog-js already redacts a deny list of headers (authorization, cookie,
  …). We redact `Authorization`/`Cookie`/`Set-Cookie` again ourselves so the
  guarantee does not depend on the library's list.

### Code (dash0)

1. New pure module `web/dash0/src/lib/analytics-redaction.ts` (no posthog
   import, so it is safe in the main bundle and trivially testable):
   - `REDACTED = "REDACTED"`, `CREDENTIAL_QUERY_PARAMS` = `access_token`,
     `refresh_token`, `token`, `code`, `state`, `tempToken`, `id_token`
     (case-insensitive match).
   - `redactUrl(url)`: string-level rewrite that preserves every
     non-sensitive byte (no `URL` normalization). Handles absolute and
     relative URLs, no query, repeated params, percent-encoded keys, and a
     token-bearing fragment (`#access_token=…&refresh_token=…` or
     `#/path?token=…`); a plain fragment (`#section`) stays untouched. A
     non-sensitive param whose decoded value is itself a token-bearing URL
     (e.g. `returnTo=%2F…%3Faccess_token%3D…`) is redacted recursively.
     Beyond the spec's param list, the single-use path tokens of
     `/reset-password/<token>`, `/invite/<token>` (page and
     `/api/v1/auth/invite/<token>`) and `/confirm-registration/<token>` are
     replaced by `REDACTED` too (same leak class: they are credentials in a
     URL that pageviews and replay record).
   - `redactCapturedNetworkRequest(req)`: the `maskCapturedNetworkRequestFn`.
     Redacts `name` (and `url` if present) with `redactUrl`, drops
     `Authorization`/`Proxy-Authorization`/`Cookie`/`Set-Cookie` from
     request/response headers, and drops any request/response body (fail
     closed). Audit correction: supplying a mask fn turns OFF posthog-js's
     built-in `scrubPayloads`, and a key-name filter would miss
     `recoveryCodes`, `signingSecret` or a heartbeat URL inside a value, so
     bodies are removed rather than filtered.
   - `redactCredentialsBeforeSend(event)`: the `before_send` hook. Applies
     `redactUrl` to `$current_url`, `$referrer`, `$initial_current_url`,
     `$initial_referrer`, `$session_entry_url`, `$session_entry_referrer`,
     `$pathname`, `$initial_pathname`, `$session_entry_pathname`,
     `$external_click_url`, `$prev_pageview_pathname` in `properties`,
     `$set` and `$set_once`. Never returns `null` for a non-null event.
   - Documented as the reusable hook spec `2026-09-25-13` composes with:
     `initAnalytics` passes `before_send` as an array whose first entry is
     `redactCredentialsBeforeSend`; 13 appends its noise filter after it.
2. `analytics.ts` `initAnalytics`: pass
   `session_recording: { maskCapturedNetworkRequestFn: redactCapturedNetworkRequest, recordHeaders: false, recordBody: false }`
   (a client-side `false` wins over the PostHog project settings) and
   `before_send: [redactCredentialsBeforeSend]`. Update the "No field
   obfuscation" header comment: UI content stays unmasked, credentials are
   filtered.
3. Docs `web/docs/docs/configuration/analytics.md`: the "From the browser"
   section is stale (says replay is disabled and URLs are templated). Rewrite
   it to match the code and state exactly what is filtered (URLs; header
   and body capture pinned off) and what is not (credentials displayed as
   page text in replay, heatmap data, autocapture link hrefs, web-vitals'
   nested URL). Page-content masking is out of scope for this spec.

Behavior change other suites might see: a URL-carried `state=` value (e.g. an
incidents `?state=` filter) now reaches PostHog as `state=REDACTED`, because
the spec lists `state`. Nothing in the app itself changes; only what PostHog
receives.

### Tests

- `analytics-redaction.test.ts`: each listed param redacted; other params,
  plain fragment, relative URLs, no-query URLs untouched; repeated params;
  realistic JWT access token + opaque refresh token; `#access_token=…`
  fragment; `?code=`/`?token=`/`?state=`; nested encoded `returnTo`; path
  tokens; headers dropped; bodies dropped (credential and harmless alike);
  before_send over
  `properties`/`$set_once`; negative controls (harmless values unchanged,
  `solidping_org`-style values, `expires_in`, `org=acme`).
- `analytics.test.ts`: `initAnalytics` passes both hooks and pins
  `recordBody`/`recordHeaders` to `false`; feeding the
  navigation entry `/d/orgs/acme?access_token=a&refresh_token=b&org=acme`
  through the configured `maskCapturedNetworkRequestFn` yields a URL without
  `a`/`b` and with `org=acme`; same for `$current_url` through `before_send`.
  Positive control: the same assertion run against the raw entry (what an
  identity hook would send) finds the tokens, so the test fails if the hook
  is removed or becomes a pass-through.
- Manual check on dev (Google login, grep snapshots) stays a manual step; it
  needs a real PostHog project.

### Operator remediation (§3), to run once part 1 is deployed to prod

Do NOT run from this batch. Session replay was turned on in v0.27.1
(commit `751ecac16`, 2026-09-09). Federated refresh tokens are `user_tokens`
rows with `type = 'refresh'` and `properties->'created_with'->>'method'` set to
the provider (`google`, `github`, `gitlab`, `microsoft`, `discord`, `slack`,
`oidc`, `saml`) or to the generic `oauth` (older rows). Revocation is a soft
delete (`deleted_at`), which every lookup already honors. Org-less
(`/no-org`) logins only carry a short-lived access token, nothing to revoke.

Revoking only the federated rows is not enough. A leaked session can mint
**derived** sessions: switch-org (`service.go`, `AuthMethodSwitchOrg`) and the
org-session mint in `org_profile.go` (`AuthMethodOrgSession`) each insert a
fresh `type = 'refresh'` row whose `created_with` has **no** `method` key. So
the revoke targets every `type = 'refresh'` row, whatever its method, of every
user who has a federated row in the exposure window. Derived rows have no upper
time bound: a leaked token could still have been used to switch org after the
deploy. Affected users simply sign in again, including any legitimate session
they opened since.

```sql
-- Affected users: a federated refresh row created while replay was recording.
-- Replace <DEPLOY_TIME> with the prod deploy time of this fix (UTC).
-- 1. Preview: every live session row that step 2 will revoke, by method.
with affected_users as (
  select distinct user_uid
  from user_tokens
  where type = 'refresh'
    and created_at >= '2026-09-09 00:00:00+00'
    and created_at <  '<DEPLOY_TIME>'
    and properties->'created_with'->>'method' in
        ('oauth','google','github','gitlab','microsoft','discord','slack','oidc','saml')
)
select coalesce(t.properties->'created_with'->>'method', '(derived: no method)') as method,
       count(*) as sessions,
       count(distinct t.user_uid) as users
from user_tokens t
join affected_users a on a.user_uid = t.user_uid
where t.type = 'refresh'
  and t.deleted_at is null
  and t.created_at >= '2026-09-09 00:00:00+00'
group by 1
order by 2 desc;

-- 1b. Review only (NOT revoked by step 2): PATs and MCP OAuth grants the same
-- users created in the window. A stolen session could have minted either;
-- check them with the users rather than revoking integrations blindly.
with affected_users as (
  select distinct user_uid
  from user_tokens
  where type = 'refresh'
    and created_at >= '2026-09-09 00:00:00+00'
    and created_at <  '<DEPLOY_TIME>'
    and properties->'created_with'->>'method' in
        ('oauth','google','github','gitlab','microsoft','discord','slack','oidc','saml')
)
select t.type, t.user_uid, t.uid, t.created_at, t.properties->'created_with' as created_with
from user_tokens t
join affected_users a on a.user_uid = t.user_uid
where t.type in ('pat', 'oauth_refresh')
  and t.deleted_at is null
  and t.created_at >= '2026-09-09 00:00:00+00'
order by t.created_at;

-- 2. Revoke (same predicate as step 1).
with affected_users as (
  select distinct user_uid
  from user_tokens
  where type = 'refresh'
    and created_at >= '2026-09-09 00:00:00+00'
    and created_at <  '<DEPLOY_TIME>'
    and properties->'created_with'->>'method' in
        ('oauth','google','github','gitlab','microsoft','discord','slack','oidc','saml')
)
update user_tokens t
set deleted_at = now(), updated_at = now()
from affected_users a
where a.user_uid = t.user_uid
  and t.type = 'refresh'
  and t.deleted_at is null
  and t.created_at >= '2026-09-09 00:00:00+00';
```

The `affected_users` CTE deliberately does not filter on `deleted_at`: a
federated row the user already logged out of still means its token was
recorded, and a derived session minted from it may still be live.

Affected users are signed out at their next refresh (access tokens expire on
their own) and sign in again. Then ask the operator whether to delete the
affected PostHog recordings; once the tokens are revoked this is optional
cleanup. Nothing in PostHog is deleted automatically.
