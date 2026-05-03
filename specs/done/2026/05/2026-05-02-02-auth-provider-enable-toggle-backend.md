# Auth provider enable/disable — backend

## Context

OAuth provider visibility on the login page is currently a side-effect of whether credentials are filled in. `server/internal/handlers/auth/providers_available.go:39` returns a provider in the public `/api/v1/auth/providers` list as soon as both `ClientID` and `ClientSecret` are non-empty. There is no way to **keep credentials configured but not propose the provider to end users**.

The user wants to separate two concerns:

1. **Configured** — credentials are stored (so the admin doesn't have to re-enter them later).
2. **Enabled** — the provider is actually advertised on the login page and its OAuth login flow is reachable.

Today these are conflated. This spec adds an explicit `enabled` flag per OAuth provider and threads it through config loading, system-parameter overrides, the public providers endpoint, and the OAuth login/callback handlers.

A sibling spec (`2026-05-02-03-auth-provider-enable-toggle-frontend.md`) adds the toggle UI in the server admin page. That spec depends on this one shipping first because it persists `auth.<provider>.enabled` system parameters and expects the backend to honor them.

## Scope

Six OAuth providers are affected, all the ones listed in `server/internal/handlers/auth/providers_available.go`:

- Google, GitHub, GitLab, Microsoft, Slack, Discord.

Out of scope:

- Email/password auth (always available — no toggle needed).
- Per-organization toggling. The `auth_providers` table is org-scoped, but today the OAuth credentials live in *system* parameters (org-independent), and that's what the toggle controls. A future per-org override would be a separate spec.
- Disabling registration globally — `auth.registration_email_pattern` already does that.
- Removing existing user accounts that authenticated via a now-disabled provider. Disabling only blocks **new** OAuth flows; existing JWT sessions and refresh tokens continue to work until they expire (this is the right semantic — disabling ≠ revoking).

## Default value: `true`

When the `auth.<provider>.enabled` param is unset, treat as `true`. Filling credentials is itself an opt-in action — requiring a second click to actually use them would be needless friction for the common path. The toggle exists for the rarer "configured but hidden" case (e.g., staging the credentials before going live, or temporarily hiding a provider during an incident).

The project hasn't shipped, so there is **no backward-compatibility story** to preserve here — the default is chosen on UX grounds alone.

## Backend file changes

### 1. Config structs — add `Enabled bool`

Add an `Enabled` field to each OAuth config struct:

| File | Edit |
|---|---|
| `server/internal/config/oauth.go` | Add `Enabled bool` koanf:"enabled"\` to `GoogleOAuthConfig` |
| `server/internal/config/github_oauth.go` | Add `Enabled bool` to `GitHubOAuthConfig` |
| `server/internal/config/gitlab_oauth.go` | Add `Enabled bool` to `GitLabOAuthConfig` |
| `server/internal/config/microsoft_oauth.go` | Add `Enabled bool` to `MicrosoftOAuthConfig` |
| `server/internal/config/discord_oauth.go` | Add `Enabled bool` to `DiscordOAuthConfig` |
| `server/internal/config/config.go` | Add `Enabled bool` to `SlackConfig` (struct lives in `config.go` itself, lines 162–168) |

Position the field at the top of each struct (above `ClientID`) so it reads as the headline property.

### 2. Defaults — set `Enabled: true` in `config.Load()`

In `server/internal/config/config.go`, the `defaults := Config{ … }` block (line 216) currently does not initialize the OAuth provider structs at all (their zero values are fine when `Enabled` doesn't exist). Add explicit defaults so the `Enabled` field starts as `true`:

```go
Google:    GoogleOAuthConfig{Enabled: true},
GitHub:    GitHubOAuthConfig{Enabled: true},
GitLab:    GitLabOAuthConfig{Enabled: true},
Microsoft: MicrosoftOAuthConfig{Enabled: true},
Slack:     SlackConfig{Enabled: true},
Discord:   DiscordOAuthConfig{Enabled: true},
```

(Slack already has additional fields like `AppID` — only the `Enabled: true` is added; the other fields stay zero-valued.)

This is the same pattern as `Email: EmailConfig{ Port: 587, Protocol: "starttls", Enabled: false }` already present at line 240. Note the inverse default for OAuth (`true` vs email's `false`) — see the rationale in the *Default value* section above.

### 3. `systemconfig.go` — register the new keys (Discord credentials + six `enabled` toggles)

In `server/internal/systemconfig/systemconfig.go`:

**Add Discord credential constants** alongside the existing OAuth keys (lines 42–53). Discord is currently missing entirely from `getKnownParameters()` — there are no entries for `auth.discord.client_id`, `auth.discord.client_secret`, `auth.discord.bot_token`, or `auth.discord.redirect_url`, so DB-stored Discord config is silently ignored today. This spec fixes that gap as part of the same change since it touches the same file:

```go
KeyDiscordClientID     ParameterKey = "auth.discord.client_id"
KeyDiscordClientSecret ParameterKey = "auth.discord.client_secret"
KeyDiscordBotToken     ParameterKey = "auth.discord.bot_token"
KeyDiscordRedirectURL  ParameterKey = "auth.discord.redirect_url"
```

**Add the six `enabled` constants**:

```go
KeyGoogleEnabled    ParameterKey = "auth.google.enabled"
KeyGitHubEnabled    ParameterKey = "auth.github.enabled"
KeyGitLabEnabled    ParameterKey = "auth.gitlab.enabled"
KeyMicrosoftEnabled ParameterKey = "auth.microsoft.enabled"
KeySlackEnabled     ParameterKey = "auth.slack.enabled"
KeyDiscordEnabled   ParameterKey = "auth.discord.enabled"
```

**Add the matching `ParameterDefinition` entries** in `getKnownParameters()`. For Discord credentials:

```go
{
    Key:    KeyDiscordClientID,
    EnvVar: "SP_DISCORD_CLIENT_ID",
    Secret: false,
    ApplyFunc: func(cfg *config.Config, value any) {
        if v, ok := value.(string); ok {
            cfg.Discord.ClientID = v
        }
    },
},
{
    Key:    KeyDiscordClientSecret,
    EnvVar: "SP_DISCORD_CLIENT_SECRET",
    Secret: true,
    ApplyFunc: func(cfg *config.Config, value any) {
        if v, ok := value.(string); ok {
            cfg.Discord.ClientSecret = v
        }
    },
},
{
    Key:    KeyDiscordBotToken,
    EnvVar: "SP_DISCORD_BOT_TOKEN",
    Secret: true,
    ApplyFunc: func(cfg *config.Config, value any) {
        if v, ok := value.(string); ok {
            cfg.Discord.BotToken = v
        }
    },
},
{
    Key:    KeyDiscordRedirectURL,
    EnvVar: "SP_DISCORD_REDIRECT_URL",
    Secret: false,
    ApplyFunc: func(cfg *config.Config, value any) {
        if v, ok := value.(string); ok {
            cfg.Discord.RedirectURL = v
        }
    },
},
```

**Add the six `enabled` `ParameterDefinition` entries**. Pattern (Google shown, repeat for the other five):

```go
{
    Key:    KeyGoogleEnabled,
    EnvVar: "SP_GOOGLE_ENABLED",
    Secret: false,
    ApplyFunc: func(cfg *config.Config, value any) {
        cfg.Google.Enabled = parseBool(value, true)
    },
},
```

Add a small helper at the bottom of the file (after `generateSecureSecret`):

```go
// parseBool coerces a config value to bool. Accepts native bool, the strings
// "true"/"false"/"1"/"0"/"yes"/"no" (case-insensitive), and falls back to
// defaultValue on anything else (including empty string).
func parseBool(value any, defaultValue bool) bool {
    switch v := value.(type) {
    case bool:
        return v
    case string:
        switch strings.ToLower(strings.TrimSpace(v)) {
        case "true", "1", "yes":
            return true
        case "false", "0", "no":
            return false
        default:
            return defaultValue
        }
    default:
        return defaultValue
    }
}
```

The `defaultValue` argument exists because env-var coercion (`def.ApplyFunc(s.config, envVal)` at line 410) passes a `string` even when set to empty — `parseBool` keeps the existing default rather than collapsing to `false`. For DB-stored values, `paramMap[key]` is whatever JSON deserialization produced, so native `bool` is the common case.

(`strings` is already imported via the package; if not, add it.)

The fallback semantic — "if the param value is malformed, keep the existing default" — preserves the `Enabled: true` default set in `config.Load()`. Without the fallback, a missing/empty env-var would override the default to `false`.

### 4. `providers_available.go` — gate by `Enabled`

In `server/internal/handlers/auth/providers_available.go`, change each `if h.cfg.<Provider>.ClientID != "" && h.cfg.<Provider>.ClientSecret != ""` to also require `enabled`:

```go
if h.cfg.Google.Enabled && h.cfg.Google.ClientID != "" && h.cfg.Google.ClientSecret != "" {
    providers = append(providers, ProviderInfo{Name: "Google", Type: "google"})
}
```

Repeat for Slack, GitHub, Microsoft, GitLab, Discord. Order: keep the existing order in the file (Slack, Google, GitHub, Microsoft, GitLab, Discord) so the diff is small.

### 5. `server.go` — handler-level rejection of disabled providers

The OAuth route registration at `server/internal/app/server.go:292–344` already gates on `ClientID != ""`, so routes for unconfigured providers are never registered (404). When a provider is *configured but disabled*, the routes exist but should refuse to serve. Two options:

**Option A (preferred): also gate route registration on `Enabled`.** Simplest and matches the existing pattern. Change each block:

```go
if s.config.Google.Enabled && s.config.Google.ClientID != "" {
    googleOAuthService := …
    googleOAuthHandler := …
    googleAuth := api.NewGroup("/auth/google")
    googleAuth.GET("/login", googleOAuthHandler.Login)
    googleAuth.GET("/callback", googleOAuthHandler.Callback)
}
```

Repeat for Slack, GitHub, Microsoft, GitLab, Discord.

**Caveat — restart required to pick up toggle changes.** Today's `Enabled` value is read once at startup from `systemconfig.Initialize()`. Toggling `auth.google.enabled` from the admin UI will update the DB row but **the route will not appear/disappear until the server restarts**. The `/api/v1/auth/providers` endpoint reads from the same in-memory `cfg`, so it has the same caveat — but it's at least re-evaluated per request. So:

- Toggling **off**: route stays registered until restart, but the login page hides the button (good enough for typical admin flow).
- Toggling **on** for the first time: route stays *un*registered until restart. Admin must restart for the OAuth login to actually work.

This is consistent with how new credentials work today (entering a `ClientID` for the first time *also* requires a restart for the route to register — see the `if s.config.X.ClientID != ""` check at startup). The frontend spec should surface this with a "Restart required for changes to take effect" notice on the auth admin page if the credentials weren't previously configured.

**Option B (rejected): keep route registration as-is, add a runtime check inside `Login`/`Callback` handlers.** This avoids the restart caveat for *toggling*, but adds a check in six places, and toggling on for the first time still requires a restart (the route doesn't exist yet). Net: more code, same restart UX. Skip.

Going with **Option A**. Document the restart caveat in the frontend spec.

### 6. Tests

`server/internal/handlers/auth/providers_handler_test.go` (or wherever the providers handler is tested — check with `grep -rn "ListProviders" server/`):

Add table-driven cases:

| Configured | Enabled | Expected in response |
|---|---|---|
| no | true (default) | absent |
| yes | true (default) | present |
| yes | false | absent |
| no | false | absent |

For each provider, one row with `Enabled: false` is sufficient to cover the new branch.

For `systemconfig.Initialize()`, add a test that:

1. Pre-seeds `auth.google.enabled = false` in the system_parameters table.
2. Runs `Initialize`.
3. Asserts `cfg.Google.Enabled == false`.
4. Repeat with `true`, `"false"`, `"true"`, malformed value (asserts default is preserved).

Use `testify/require` and `t.Parallel()` per the testing conventions.

## API surface — no new endpoints

The toggle is read/written through the existing `/api/v1/system/parameters` endpoint (used by the admin UI). No new endpoint is added. Param keys: `auth.google.enabled`, `auth.github.enabled`, `auth.gitlab.enabled`, `auth.microsoft.enabled`, `auth.slack.enabled`, `auth.discord.enabled`. Values: native JSON booleans (`true` / `false`).

## Verification

1. `make build-backend` — compiles.
2. `make gotest` — all tests pass.
3. Run `make dev-test` and:
   ```bash
   TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
     -d '{"org":"default","email":"admin@solidping.com","password":"solidpass"}' \
     'http://localhost:4000/api/v1/auth/login' | jq -r '.accessToken')

   # Seed Google credentials.
   curl -s -X PUT -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
     -d '{"value":"test-id"}' \
     'http://localhost:4000/api/v1/system/parameters/auth.google.client_id'
   curl -s -X PUT -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
     -d '{"value":"test-secret","secret":true}' \
     'http://localhost:4000/api/v1/system/parameters/auth.google.client_secret'

   # Restart server (so route registration picks up the credentials).
   # …

   # Provider should appear.
   curl -s 'http://localhost:4000/api/v1/auth/providers' | jq '.data'
   # Expect: [{"name":"Google","type":"google"}]

   # Disable.
   curl -s -X PUT -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
     -d '{"value":false}' \
     'http://localhost:4000/api/v1/system/parameters/auth.google.enabled'

   # Provider should disappear.
   curl -s 'http://localhost:4000/api/v1/auth/providers' | jq '.data'
   # Expect: []
   ```

4. `SP_GOOGLE_ENABLED=false` env-var override should also hide the provider (env > db precedence per `systemconfig.Initialize`).

## Final grep check

After the change, this should return twelve matches — one per provider in `providers_available.go` and one per provider in `server.go`:

```bash
rtk grep -n '\.Enabled &&' server/internal/handlers/auth/providers_available.go server/internal/app/server.go
```

---

## Implementation Plan

1. Add `Enabled bool` field (with `koanf:"enabled"`) to the six OAuth config structs (Google/GitHub/GitLab/Microsoft/Slack/Discord); set the field at the top of each struct.
2. Set `Enabled: true` defaults in `config.Load()` for all six providers.
3. In `systemconfig.go`, add Discord credential param keys + apply funcs (currently missing) and add the six `auth.<provider>.enabled` keys with apply funcs that route through a new `parseBool` helper.
4. Update `providers_available.go` to gate every `if h.cfg.X.ClientID … && ClientSecret …` on `h.cfg.X.Enabled`.
5. Update `server/internal/app/server.go` OAuth route registration to gate on `Enabled`.
6. Tests: add table-driven cases to the providers handler test covering configured×enabled combinations, and a systemconfig test covering bool/true/"true"/false coercion + malformed-value default preservation.
7. Run `make build-backend lint test` and fix anything that breaks.
