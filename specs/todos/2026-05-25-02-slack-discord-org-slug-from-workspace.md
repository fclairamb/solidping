# Slack/Discord org slug from workspace identity

## Context

When a user signs in with Slack and no org is yet linked to their Slack team, we
auto-create an organization and generate its slug from the Slack **team name**.
In practice this frequently produces generic slugs like `org`, `org2`, `org3`
instead of a slug matching the Slack workspace (e.g. `acme` for `acme.slack.com`).

Root cause: the slug is derived from `oauthResp.Team.Name`, which is often empty
in the user-token sign-in flow. When the normalized name is shorter than 3 chars
the generator falls back to the literal base `"org"`, and the uniqueness loop then
appends `2`, `3`, … producing `org`, `org2`, `org3`.

Meanwhile, Slack's `openid.connect.userInfo` endpoint (already called by both
flows) returns the real workspace identity as custom claims — `team_name` and
`team_domain` (the workspace subdomain, e.g. `acme`). The integration package
already decodes these (`server/internal/integrations/slack/client.go:276-279`),
but the sign-in flow's local `OpenIDUserInfo` struct discards them.

Affected org-creation paths (all share a duplicated `generateUniqueSlug`):
- Slack sign-in OAuth — `server/internal/handlers/auth/slack_service.go:342-425`
  (called from `HandleCallback` at line 302; `userInfo` is in scope).
- Slack bot-install integration — `server/internal/integrations/slack/service.go:337-425`
  (called from `HandleOAuthCallback` at line 237; `userInfo` fetched at line 219).
- Discord OAuth — `server/internal/handlers/auth/discord_service.go:362-447`
  (called at lines 204/207).

### Honest opinion (recorded at planning time)

- **Slack is the real fix.** Using `team_domain` as the slug exactly matches the
  user's ask ("same slug as the one from Slack") and eliminates the `org`/`org2`
  outcome, because `team_domain` is already a valid slug and is reliably present
  with the `profile` scope (both flows request `openid,email,profile`).
- **Discord has no equivalent.** Discord's `/users/@me/guilds` returns only
  `id`/`name`/`icon` (`discord_service.go:67-71`) — there is no workspace
  subdomain/handle. Guild *vanity URLs* exist only for boosted servers and require
  elevated permissions + extra API calls, so they are out of scope. For Discord we
  therefore keep deriving the slug from the **guild name**; the only honest
  consistency win is sharing the improved generator so all three paths behave
  identically given their inputs. Discord's generic-`org` fallback can still occur
  for emoji-only / non-Latin guild names — unavoidable without transliteration,
  which we explicitly do not add.
- **Worth de-duplicating.** Three identical ~50-line copies of `generateUniqueSlug`
  exist. Since the new "try several candidate sources in priority order" behavior
  must be identical across all three, extracting one shared helper is justified
  (not premature) and is the mechanism that delivers the requested consistency.

## Goal

- New orgs created from Slack use the Slack workspace slug (`team_domain`) as their
  org slug, falling back to workspace `team_name`, then `oauthResp.Team.Name`, then
  `"org"` only as a last resort.
- New orgs created from Slack use the workspace `team_name` (claim) as the org
  display name when available, instead of a possibly-empty `Team.Name`.
- All three flows (Slack sign-in, Slack integration, Discord) share one slug
  generator with identical normalization, length rules, and collision handling.
- Slugs produced satisfy the existing org-slug rules (3–20 chars, `[a-z0-9]`
  start/end, `[a-z0-9-]` body) — matching `orgSlugRegex` in
  `server/internal/handlers/auth/service.go:2152`.

## Non-goals

- No DB schema/migration changes.
- No change to the manual "create org" HTTP path (`Service.CreateOrg`), which
  already takes a user-supplied slug.
- No Discord vanity-URL lookup or transliteration of non-Latin names.
- No re-slugging of already-existing orgs; this only affects newly created orgs.

## Backend

### 1. Shared slug helper (new)

Add a small package `server/internal/orgslug/orgslug.go`:

```go
package orgslug

// Finder is satisfied by db.Service.
type Finder interface {
    GetOrganizationBySlug(ctx context.Context, slug string) (*models.Organization, error)
}

// Slugify normalizes one candidate to a valid slug base, or "" if nothing usable.
// lowercase → spaces to '-' → keep [a-z0-9-] → collapse '--' → trim '-' →
// require len >= 3 (else "") → cap at 20 → trim trailing '-'.
func Slugify(s string) string

// GenerateUnique returns the first candidate that Slugifies to a non-empty base,
// else "org". It then ensures uniqueness via Finder.GetOrganizationBySlug,
// appending 2,3,… on collision (capping total length at 20).
func GenerateUnique(ctx context.Context, f Finder, candidates ...string) string
```

Behavioral notes:
- A candidate that normalizes to < 3 chars is skipped (we move to the next
  candidate) rather than forcing `"org"` immediately.
- The collision loop keeps the existing semantics (treat a lookup error as
  "available"), but caps `base+suffix` to 20 chars by truncating `base` so very
  long workspace domains don't overflow the slug rules.

### 2. Slack sign-in flow (`server/internal/handlers/auth/slack_service.go`)

- Extend the local `OpenIDUserInfo` (lines 72-80) with the workspace claims:
  ```go
  SlackTeamName   string `json:"https://slack.com/team_name"`
  SlackTeamDomain string `json:"https://slack.com/team_domain"`
  ```
- In `HandleCallback` (line 302), thread the workspace identity into org creation:
  prefer `userInfo.SlackTeamName` for the org name (fallback `oauthResp.Team.Name`),
  and pass the slug candidates.
- Replace `findOrCreateOrganization(ctx, teamID, teamName)` with a version that also
  receives the candidate slug sources, and replace the local `generateUniqueSlug`
  body with a call to `orgslug.GenerateUnique(ctx, s.db, teamDomain, teamName, fallbackName)`.
  Delete the now-dead local `generateUniqueSlug`.

### 3. Slack integration flow (`server/internal/integrations/slack/service.go`)

- `userInfo` is already in scope at the `findOrCreateOrganizationByTeamID` call
  (line 237). Pass `userInfo.SlackTeamDomain` / `userInfo.SlackTeamName` through and
  call `orgslug.GenerateUnique(...)`. Delete the local `generateUniqueSlug`.

### 4. Discord flow (`server/internal/handlers/auth/discord_service.go`)

- No workspace-domain source exists. Route the existing inputs through the shared
  helper for consistency: `orgslug.GenerateUnique(ctx, s.db, guildName)` for the
  guild case (line 204) and `orgslug.GenerateUnique(ctx, s.db, userInfo.DisplayName())`
  for the no-guild fallback (line 207). Delete the local `generateUniqueSlug`.
  Behavior is unchanged except for the shared min-length/collision rules.

## Files to create / modify

### New files
- `server/internal/orgslug/orgslug.go` — `Slugify` + `GenerateUnique`.
- `server/internal/orgslug/orgslug_test.go` — table-driven unit tests.

### Modified files (Backend)
- `server/internal/handlers/auth/slack_service.go` — add claim fields; thread
  domain/name; use shared helper; drop local generator.
- `server/internal/integrations/slack/service.go` — thread domain/name; use shared
  helper; drop local generator.
- `server/internal/handlers/auth/discord_service.go` — use shared helper; drop
  local generator.

## Tests

- `orgslug_test.go` (table-driven, `t.Parallel()`, `testify/require`):
  - `Slugify`: `"Acme Corp" → "acme-corp"`, `"acme" → "acme"`, `"🚀" → ""`,
    `"a" → ""` (too short), `"--Foo--" → "foo"`, 30-char input → capped at 20 with no
    trailing `-`.
  - `GenerateUnique`: picks first usable candidate (`["", "acme", "x"] → "acme"`);
    skips a too-short first candidate; returns `"org"` when all fail; appends `2`/`3`
    on collision using a fake `Finder`; long base + suffix stays ≤ 20 chars.
- Slack sign-in service test: an `OpenIDUserInfo` with `SlackTeamDomain:"acme"`
  yields org slug `acme` (not `org`); empty domain + `team_name:"Acme Corp"` yields
  `acme-corp`.

## Verification

```bash
make lint test
```

Manual (dev server on :4000, requires a real Slack app):
1. `make dev` and start Sign-in-with-Slack from the login page.
2. After callback, confirm the redirect `?org=` value equals the Slack workspace
   subdomain, and the new org's slug matches it (check via the orgs API).
3. Re-run sign-in for a *second* distinct workspace; confirm its slug is that
   workspace's domain, not `org2`.

If a live Slack app isn't available, rely on the service-level unit tests above
(decode a canned `openid.connect.userInfo` JSON body with the `team_domain` claim).

## Risk log

| Risk | Mitigation |
|---|---|
| `team_domain` claim absent (scope/app config) | Fallback chain: domain → team_name → `Team.Name` → `"org"`. |
| Desired workspace slug already taken by another org | Existing collision loop appends numeric suffix. |
| Workspace domain longer than 20 chars | `Slugify` caps at 20 and trims trailing `-`; collision suffix capped to keep ≤ 20. |
| Discord seen as "fixed" but still emits `org` for emoji names | Honest-opinion section documents the limitation; no transliteration added. |
| Import cycle from new package | `orgslug` imports only `models`; `models` has no `db` dependency. |

## Implementation Plan

1. Create `server/internal/orgslug/` with `Slugify` + `GenerateUnique` and tests;
   run `make lint test`.
2. Update Slack sign-in flow (struct fields, `HandleCallback`, helper call); update
   its service test.
3. Update Slack integration flow to thread domain/name and use the helper.
4. Update Discord flow to use the helper; delete all three local `generateUniqueSlug`.
5. `make lint test` (+ `make test-dash` if any frontend touched — none expected).
6. QA per Verification, completeness audit, then archive the spec to
   `specs/done/2026/05/` and merge.
