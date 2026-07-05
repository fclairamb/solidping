# Slack multi-org workspaces (1/2): org-scoped installs and connections

## Problem

Installing the Slack app from `/orgs/webingenia/integrations/new` for a
workspace that is already connected to another org lands the user on
`/orgs/webingenia2` — the *other* org — and no integration is created in
`webingenia`. One Slack workspace is effectively locked to a single
SolidPing org, which breaks the common "one company Slack, several
monitored products/orgs" setup.

Root-cause chain:

1. **The new-integration CTA drops the org context.** The Slack tile does a
   full-page redirect to `/api/v1/integrations/slack/install?source=dashboard`
   with no `org` param (`web/dash0/src/routes/orgs/$org/integrations.new.tsx:219`),
   unlike the channel-edit CTA which passes `org` + `channelUid`
   (`web/dash0/src/components/integrations/integration-form.tsx:1141`).
2. **The callback then resolves the org from the workspace identity.** With no
   `targetOrgSlug` in the OAuth state, `HandleOAuthCallback` falls back to
   `findOrCreateOrganizationByTeamID`
   (`server/internal/integrations/slack/service.go:476`), whose primary lookup
   is `organization_providers` by `(slack, team_id)` — the org the workspace
   was *first* installed into. Login tokens are minted for that org and
   `/dash0/auth/slack/complete` navigates there
   (`web/dash0/src/routes/auth.slack.complete.tsx:65`).
3. **Connections are globally unique per team, by accident.** Even when the
   org slug *is* passed, `createOrUpdateConnection`
   (`server/internal/integrations/slack/service.go:421`) looks up the existing
   connection by `team_id` **across all orgs** (`GetChannelByProperty` has no
   `organization_uid` filter — `server/internal/db/postgres/postgres.go:2937`,
   `server/internal/db/sqlite/sqlite.go:2910`) and, when found, silently
   *updates the other org's integration* and returns its UID instead of
   creating one in the current org.

### Security problem (must be fixed in the same change)

The install entry point is **unauthenticated by design** (Marketplace installs
have no session — `server/internal/integrations/slack/handler.go:87`), yet it
honors `org=<slug>` and `channelUid=<uid>` query params and stashes them into
the OAuth state. On callback:

- `ensureOrganizationMembership` **auto-joins the OAuth-ing Slack user into the
  target org** (role `user`, or `admin` if the org has no members —
  `server/internal/integrations/slack/service.go:598`), and mints login tokens
  for it.
- `updateExistingChannel` **overwrites the target channel's settings** (bot
  token, team) without checking that the channel belongs to the resolved org
  (`server/internal/integrations/slack/service.go:365`).

So today, anyone can craft
`/api/v1/integrations/slack/install?org=<victim-slug>` (or
`channelUid=<victim-channel>`), complete OAuth with *their own* Slack
workspace, and end up a member of the victim org / rewrite a victim channel.
Making org-targeted installs first-class must not amplify this: org targeting
has to come from an authenticated session, not a query param.

## Product decision

- One Slack workspace can be connected to **any number of orgs**. Each org gets
  its **own** `integration_connections` row (same `team_id`, own settings).
  Slack is fine with this: one bot token per (app × workspace); a second OAuth
  run re-issues credentials for the same bot, and both rows hold valid tokens.
- `organization_providers` keeps its meaning as the workspace's **home org**
  (first install wins): it drives Marketplace-install landing and
  Sign-in-with-Slack routing (`server/internal/handlers/auth/slack_service.go:377`).
  Unchanged in this spec.
- Inbound routing (slash commands, mentions, uninstall fan-out) with multiple
  connections per team is **spec 2026-07-05-02** — this spec only makes
  outbound/notification connections org-scoped.

## Proposal

### 1. Authenticated, org-scoped install-URL endpoint

New endpoint, behind the normal org auth chain (`RequireAuth` + org context
middleware, which already enforces membership):

```
POST /api/v1/orgs/:org/integrations/slack/install-url
Body: { "channelUid": "<uid>" }   // optional
→ 200 { "url": "https://slack.com/oauth/v2/authorize?..." }
```

- Handler lives in the slack package, wired in `server/internal/app/server.go`
  next to the other org-scoped integration routes.
- Takes the org from the **authenticated route context** (never from the
  body). When `channelUid` is present, verify the channel exists, is a Slack
  integration, and **belongs to that org** before stashing it; otherwise 404.
- Delegates to `BuildInstallURL` (`server/internal/integrations/slack/service.go:168`)
  unchanged — state minting, TTL, and payload keys stay as they are.

The public `GET /api/v1/integrations/slack/install` remains for the
Marketplace flow but **stops honoring `org` and `channelUid`** — it only keeps
`source`. This closes the membership-grant and channel-takeover vectors above.

### 2. Org-scoped connection upsert

`createOrUpdateConnection` (`service.go:421`) switches its existing-connection
lookup to an org-scoped variant:

- New DB method on the `db.Service` interface (`server/internal/db/service.go:361`):
  `GetChannelByPropertyForOrg(ctx, orgUID, connType, propertyName, propertyValue)`,
  implemented for **both** backends (`postgres.go`, `sqlite.go`) — same query
  as `GetChannelByProperty` plus `organization_uid = ?`.
- Same org re-installs → update that org's row (idempotent, token refresh).
- Different org installs the same workspace → **new** integration row in that
  org; the first org's row is untouched.

The global `GetChannelByProperty` stays for inbound team-ID routing until
spec 02 reworks it.

### 3. Land the user where they started

For dashboard-origin installs (state payload has an org), set the exchange
payload's channel UID to the created/updated connection UID so
`/dash0/auth/slack/complete` navigates to
`/orgs/$org/integrations/$integrationUid` (`auth.slack.complete.tsx:58`) —
the integration the user just installed — instead of the org home page.
Marketplace installs (no org in state) keep landing on `/orgs/$org` for
onboarding.

### 4. Frontend

Both install CTAs switch from a raw `window.location.href` query-param link to:
`apiFetch POST .../install-url` (with `channelUid` on the edit-page CTA), then
`window.location.href = url`:

- `web/dash0/src/routes/orgs/$org/integrations.new.tsx:215` (Slack tile CTA)
- `web/dash0/src/components/integrations/integration-form.tsx:1138`
  (unconnected-channel CTA)

Keep the `slack-install` test IDs.

## Out of scope

- Inbound command/mention/interaction routing and `app_uninstalled` fan-out
  with multiple orgs per team → **2026-07-05-02**.
- Sign-in-with-Slack routing and `organization_providers` semantics (home org
  stays first-install-wins).
- Slack token rotation support.

## Acceptance criteria

- From `/orgs/A/integrations/new`, installing a workspace already connected to
  org B creates a **new** integration in A, leaves B's integration untouched,
  and the browser ends on A's new integration page — never on B.
- Re-installing from the same org updates that org's existing row (no
  duplicate rows per `(org, team_id)`).
- `GET /api/v1/integrations/slack/install?org=X&channelUid=Y` ignores both
  params: completing that flow never grants membership in X nor touches Y.
- `POST /api/v1/orgs/:org/integrations/slack/install-url` returns 401 without
  auth, 403/404 for non-members, and 404 for a `channelUid` outside the org.
- Backend tests green on **both** PostgreSQL and SQLite; dash0 e2e
  `channels-slack-install.spec.ts` updated (CTA now calls `install-url`, then
  navigates to the returned URL) and green.

## Implementation plan

- [ ] DB: add `GetChannelByPropertyForOrg` to the interface + postgres +
      sqlite, with table-driven tests for both backends.
- [ ] Service: org-scope the lookup in `createOrUpdateConnection`; extend
      `service_oauth_test.go` with the two-orgs-one-workspace and
      same-org-reinstall cases.
- [ ] Handler + routing: add the authenticated `install-url` endpoint (org
      from route, channel-ownership check); strip `org`/`channelUid` from the
      public `Install` handler; handler tests for the authz matrix.
- [ ] Callback UX: return the connection UID as the exchange `channelUid` for
      dashboard-origin installs.
- [ ] Frontend: switch both CTAs to the minted URL; update
      `channels-slack-install.spec.ts`.
- [ ] Run `make test`, `make lint`, `make test-dash`.

## Implementation Plan

Concrete file-level plan, mapped to the codebase as it exists today (line
numbers verified against `main` before starting).

### Step 1 — DB: `GetChannelByPropertyForOrg`

- `server/internal/db/service.go`: add to the `Service` interface right after
  `GetChannelByProperty` (~line 363):
  ```go
  GetChannelByPropertyForOrg(
      ctx context.Context, orgUID, connType, propertyName, propertyValue string,
  ) (*models.Integration, error)
  ```
- `server/internal/db/postgres/postgres.go` (~line 2937): copy
  `GetChannelByProperty`, add `.Where("organization_uid = ?", orgUID)`.
- `server/internal/db/sqlite/sqlite.go` (~line 2910): same, sqlite uses
  `json_extract` instead of `->>` (already the existing difference between the
  two `GetChannelByProperty` bodies) — copy that difference forward.
- Tests: `server/internal/db/service_test.go` has a shared
  `testService(t, svc)` harness run against both engines via
  `TestPostgresService` / `TestSQLiteService` / `TestSQLiteServiceInMemory`.
  Add a new `t.Run("ChannelByPropertyForOrg", …)` calling a new
  `testChannelByPropertyForOrg(ctx, t, svc)` covering: same team_id in two
  orgs → org-scoped lookup returns each org's own row; no row in the queried
  org → `sql.ErrNoRows`; soft-deleted row is excluded.

### Step 2 — Service: org-scope `createOrUpdateConnection`

- `server/internal/integrations/slack/service.go:421-471`: swap the
  `s.db.GetChannelByProperty(ctx, slack, "team_id", teamID)` lookup for
  `s.db.GetChannelByPropertyForOrg(ctx, orgUID, slack, "team_id", teamID)`.
  Behavior unchanged otherwise (existingConn nil → create in orgUID; found →
  update that same row).
- `GetConnectionByTeamID` (line 147) and `HandleAppUninstalled` (line 658) stay
  on the org-agnostic `GetChannelByProperty` — those are spec 02's job.
- Tests: extend `service_oauth_test.go` (or a new
  `service_connection_test.go` in the same package) with direct unit tests of
  `createOrUpdateConnection` — it needs no real Slack API call, just a fake
  `*OAuthResponse`. Cases: (a) two-orgs-one-workspace — call twice with the
  same `Team.ID` but different `orgUID`s, assert two distinct rows exist, each
  scoped to its org, first row untouched after the second call; (b)
  same-org-reinstall — call twice with the same `orgUID` + `Team.ID`, assert
  one row, `UpdatedAt`/settings refreshed, UID stable.

### Step 3 — Handler + routing: authenticated `install-url` endpoint

- New method on `*slack.Service` (service.go, near `BuildInstallURL`):
  ```go
  func (s *Service) BuildOrgInstallURL(ctx context.Context, orgUID, orgSlug, channelUID string) (string, error)
  ```
  When `channelUID != ""`, first load the channel (`s.db.GetChannel`), confirm
  `conn.Type == models.ConnectionTypeSlack` and
  `conn.OrganizationUID == orgUID` (mirror the ownership check already used in
  `GetDestinations`, service.go:901) — return a new `ErrConnectionNotFound` on
  mismatch/not-slack so the handler maps it to 404. Then delegate to the
  existing `BuildInstallURL(ctx, "dashboard", channelUID, orgSlug)`.
- New handler method on `*slack.Handler` (handler.go, near `Install`):
  ```go
  // POST /api/v1/orgs/:org/integrations/slack/install-url
  func (h *Handler) BuildInstallURL(writer http.ResponseWriter, req bunrouter.Request) error
  ```
  Reads `org` from `mw.GetOrganizationFromContext(req.Context())` (populated
  by `RequireOrgAccess`) — never from the body. Decodes an optional
  `{ "channelUid": string }` JSON body (empty body is fine, mirror
  `StartFreeboxPairing`'s `!errors.Is(err, io.EOF)` tolerance in
  `handlers/integrations/handler.go:213-218`). Calls `svc.BuildOrgInstallURL`,
  maps `ErrConnectionNotFound` → 404 `base.ErrorCodeIntegrationNotFound`,
  writes `{"url": authorizeURL}` on success.
- Route wiring in `server/internal/app/server.go`, next to the existing
  `slackOrgRoutes` group (~line 1075):
  ```go
  slackOrgIntegrationRoutes := api.NewGroup("/orgs/:org/integrations/slack").
      Use(authMiddleware.RequireAuth, authMiddleware.RequireOrgAccess)
  slackOrgIntegrationRoutes.POST("/install-url", slackHandler.BuildInstallURL)
  ```
  (`RequireOrgAccess` is the org-membership middleware — 401 no-auth,
  403 non-member/org-mismatch, 404 unknown org — confirmed at
  `middleware/auth.go:143-204`, same pairing as `orgJobsGroup` at
  `server.go:570`.)
- Strip `org`/`channelUid` honoring from the public handler: in
  `handler.go`'s `Install` (line 92-109), stop reading `channelUid`/`org` from
  the query string; only `source` remains. `BuildInstallURL` (service.go:168)
  keeps its `channelUID, orgSlug string` signature (still used internally by
  the new org-scoped path) but the public `Install` handler now always passes
  `"", ""` for those two params.
- Handler tests (new `handler_test.go` cases or extend existing): 401 without
  a token, 403 for a member of a different org (token org ≠ route org), 404
  when `channelUid` belongs to another org, 200 + URL body for a same-org
  member with and without `channelUid`. Also a regression test on `Install`
  proving `?org=` and `?channelUid=` are now ignored (state payload has no
  `installOrg`/`channelUid` after going through the public endpoint).

### Step 4 — Callback UX: land on the created/updated integration

- `HandleOAuthCallback` (service.go:223-299) already threads
  `targetChannelUID` through when the install came from a channel-edit page.
  For the *new* org-scoped-but-no-specific-channel path (Slack tile CTA, no
  `channelUid`), `createOrUpdateConnection`'s return value (the connection
  UID) needs to end up in `OAuthResult.ChannelUID` too — today that field is
  only set from `targetChannelUID` (line 293). Change: after the
  `createOrUpdateConnection` branch (line 269), when `targetOrgSlug != ""`
  (i.e. this was a dashboard-origin, org-scoped install, not a bare
  Marketplace install), set `OAuthResult.ChannelUID = connUID`. Marketplace
  installs (`targetOrgSlug == ""`) keep `ChannelUID` empty so
  `auth.slack.complete.tsx` falls through to its existing "land on `/orgs/$org`"
  branch — no frontend change needed there, it already branches on
  `data.channelUid` (auth.slack.complete.tsx:58-70).
- Test: extend the OAuth-callback tests to cover that
  `targetOrgSlug != "" && targetChannelUID == ""` still yields a populated
  `ChannelUID` on the `OAuthResult` (can be asserted through
  `createOrUpdateConnection`'s direct unit test from Step 2 plus a thin check
  in `HandleOAuthCallback` once mockable, or documented as covered
  indirectly if full OAuth exchange can't be faked in tests — call this out
  honestly if it ends up untested at the `HandleOAuthCallback` level).

### Step 5 — Frontend: switch both CTAs to `install-url`

- `web/dash0/src/routes/orgs/$org/integrations.new.tsx` (Slack tile CTA,
  ~line 215-220): replace the `window.location.href = "/api/v1/integrations/slack/install?source=dashboard"`
  literal with an async click handler that does
  `const { url } = await apiFetch<{ url: string }>(`/api/v1/orgs/${org}/integrations/slack/install-url`, { method: "POST", body: JSON.stringify({}) })`
  then `window.location.href = url`. Wrap in try/catch → `toast.error(...)` on
  failure (mirror existing toast usage in this file/package). Keep
  `data-testid="slack-install"`.
- `web/dash0/src/components/integrations/integration-form.tsx` (unconnected
  channel CTA, ~line 1138-1149): same pattern, but body is
  `JSON.stringify({ channelUid })` when `channelUid` is set. Keep
  `data-testid="slack-install"`.
- `web/dash0/e2e/channels-slack-install.spec.ts`: update both tests' route
  stubs from `**/api/v1/integrations/slack/install*` to
  `**/api/v1/orgs/test/integrations/slack/install-url` (POST), fulfilling
  `{ url: "https://slack.com/oauth/v2/authorize?..." }`, and assert the test
  then follows through to that URL (or, since a real cross-origin navigation
  can't be asserted in Playwright, assert the POST body/method and that
  `window.location.href` was set to the stubbed URL — check how other e2e
  specs in this repo assert on `window.location` navigation, e.g. via
  `page.evaluate` polling or intercepting via `page.route` on the follow-up
  URL, and mirror that convention).

### Step 6 — QA

- `make build-backend lint-back test` (backend Go).
- `make build-dash0` then `cd web/dash0 && bun run lint` (frontend; gate is
  no NEW lint errors in touched files, base is already red).
- Re-run `channels-slack-install.spec.ts` specifically; author-but-report if
  the local devloop isn't in `SP_RUNMODE=test`.
