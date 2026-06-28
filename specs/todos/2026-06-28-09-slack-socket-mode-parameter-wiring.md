# Slack Socket Mode — make the toggle actually settable (parameter key wiring fix)

## Context

Slack **Socket Mode** (the app opens an outbound WebSocket to Slack with an
`xapp-…` App-Level Token instead of receiving inbound HTTPS webhooks — the right
transport for self-hosted instances behind a firewall) is **already fully built**
end-to-end; it shipped in the bulk feature commit `a976856d` (#73):

| Layer | Where | State |
|---|---|---|
| Supervisor (WebSocket, reconnect, dispatch) | `server/internal/integrations/slack/socketmode.go` (+ `socketmode_test.go`) | ✅ complete, tested |
| Server wiring | `server/internal/app/server.go` — supervisor constructed when `cfg.Slack.Enabled && cfg.Slack.SocketModeEnabled` (≈:1011), `Run()` goroutine started (:1735) | ✅ |
| Config struct | `cfg.Slack.SocketModeEnabled` / `cfg.Slack.AppToken` — `server/internal/config/config.go:301` | ✅ |
| Param + env plumbing | `auth.slack.socket_mode_enabled` (`SP_SLACK_SOCKET_MODE_ENABLED`) and `auth.slack.app_token` (`SP_SLACK_APP_TOKEN`, secret) — `server/internal/systemconfig/systemconfig.go:55` | ✅ |
| Status endpoint | `GET /api/v1/integrations/slack/socket/status` | ✅ |
| Dashboard settings page | `web/dash0/src/routes/orgs/$org/server.slack.tsx` — toggle + token field + live status card | ⚠️ **mis-wired** |

So "allow Slack to work in Socket Mode as a settable parameter" is ~90% done.
The **env-var path works** (`SP_SLACK_SOCKET_MODE_ENABLED=true` + `SP_SLACK_APP_TOKEN=xapp-…`).
What's broken is the **dashboard parameter**: the operator UI writes keys the
backend never reads, so the in-app toggle does nothing.

**Scope (confirmed):** fix the wiring so the parameter is genuinely settable from
the UI, and add the tests that should have caught this. **Restart-to-apply stays
as-is** — making the toggle take effect without a server restart (hot-reload) is
explicitly **out of scope** (separate spec).

---

## Root cause (verified against source)

The dashboard page keys off the **bare `slack.*`** namespace, but the backend
only knows the **`auth.slack.*`** namespace:

| Concept | Frontend writes/reads | Backend reads | Match? |
|---|---|---|---|
| Slack enabled (gate) | `slack.enabled` — `server.slack.tsx:30` | `auth.slack.enabled` → `cfg.Slack.Enabled` (`systemconfig.go:476`) | ❌ |
| Socket mode on | `slack.socket_mode_enabled` — `:31` | `auth.slack.socket_mode_enabled` → `cfg.Slack.SocketModeEnabled` (`:55,:386`) | ❌ |
| App-level token | `slack.app_token` — `:32` | `auth.slack.app_token` → `cfg.Slack.AppToken` (`:56,:395`) | ❌ |

Why the mismatch silently breaks instead of erroring:

1. **`SetParameter` does not validate keys** — `server/internal/handlers/system/service.go:193`
   just writes whatever key it is given. So saving `slack.app_token` **succeeds**
   (HTTP 200), it simply lands under a key nobody loads.
2. **`systemconfig.Initialize` matches by exact key** — `systemconfig.go:600-616`
   iterates the *known* (`auth.slack.*`) params and looks each up in the DB map
   (`paramMap[def.Key]`). A row keyed `slack.socket_mode_enabled` is never
   matched → never applied to `cfg`.
3. **The page gate reads `slack.enabled`**, which nothing ever writes (the auth
   provider page `server.auth.tsx:75` writes `auth.slack.enabled`). So
   `slackEnabled` is always falsy and the page renders the **"not enabled" hint**
   (`server.slack.tsx:136-145`) — the form is effectively unreachable on a normal
   install.

Net effect: even an operator who *finds* the page can't enable Socket Mode from
the UI. Only env vars / a direct API write to `auth.slack.*` work today.

### Why it slipped through CI

`web/dash0/e2e/slack-socket-mode.spec.ts` **mocks the entire parameters API**
(`page.route(SYSTEM_PARAMS_URL, …)`) and asserts the PUT bodies use
`slack.app_token` / `slack.socket_mode_enabled` (`:146-152`). Because the real
backend is mocked away, the key contract is never exercised — the test passes
while **encoding the bug**. A purely-mocked E2E structurally cannot catch a
front/back key-contract mismatch; that is the lesson this spec must bake in.

---

## The fix

### 1. Correct the key constants — `web/dash0/src/routes/orgs/$org/server.slack.tsx`

```ts
const KEY_ENABLED = "auth.slack.enabled";
const KEY_SOCKET_ENABLED = "auth.slack.socket_mode_enabled";
const KEY_APP_TOKEN = "auth.slack.app_token";
```

This is the **entire functional change**. After it:
- The gate reads the same flag the **Authentication settings** page writes
  (`auth.slack.enabled`) and that drives `cfg.Slack.Enabled` — i.e. the exact
  condition the supervisor is constructed under. This matches the existing
  `notEnabledHint` copy, which already tells operators to *"Configure it under
  Authentication settings or set `SP_SLACK_ENABLED=true`"* (`en/server.json:147`).
- Toggling Socket Mode writes `auth.slack.socket_mode_enabled`; the token writes
  `auth.slack.app_token` (secret) — both keys `systemconfig.Initialize` actually
  applies to `cfg.Slack` on next startup.

No other module references the bare `slack.*` keys (verified repo-wide — only
this file and its E2E), so no other consumer breaks.

> Keep the token's `secret: true` on write (already correct, `:91`) so it lands in
> the encrypted/secret param path matching `KeySlackAppToken{Secret: true}`.

### 2. Backend regression guard — new `systemconfig` test

There are currently **no `systemconfig` tests covering Slack** (verified). Add a
table-driven test (`server/internal/systemconfig/systemconfig_test.go`,
`testify/require`, `t.Parallel()`) asserting that `Service.Initialize` applies DB
params to config:

- `auth.slack.enabled = true` → `cfg.Slack.Enabled == true`
- `auth.slack.socket_mode_enabled = true` → `cfg.Slack.SocketModeEnabled == true`
- `auth.slack.app_token = "xapp-x"` → `cfg.Slack.AppToken == "xapp-x"`
- env precedence: `SP_SLACK_SOCKET_MODE_ENABLED` / `SP_SLACK_APP_TOKEN` override the
  DB value (mirror the `env > db > default` rule the loop implements at `:602-616`).

This guards the **backend half** of the contract the UI now depends on, so a
future rename on either side fails a test rather than silently breaking the
toggle.

### 3. E2E — fix the keys *and* close the mock gap — `web/dash0/e2e/slack-socket-mode.spec.ts`

Two parts:

**(a) Stop encoding the bug.** Update every mocked row and PUT assertion from
`slack.*` to `auth.slack.*` (`:69-71`, `:146-152`, `:169-182`, `:207-215`). The
"can enable socket mode and save token" test must now assert PUTs to
`auth.slack.socket_mode_enabled` and `auth.slack.app_token`. Keep the
status-card render tests (connected / disconnected / team count / last error) —
those are legitimately mock-driven and don't touch the key contract.

**(b) Add one un-mocked round-trip test** that exercises the *real* backend so the
key contract is actually verified (this is the test that would have caught the
bug):

- Against the live `make dev-test` backend (org `test`, `test@test.com`/`test`),
  with Slack enabled (seed `auth.slack.enabled=true`, or skip via env), open
  `/orgs/test/server/slack`, toggle Socket Mode on, enter an `xapp-…` token, save.
- Assert via `GET /api/v1/system/parameters` that `auth.slack.socket_mode_enabled`
  is `true` and `auth.slack.app_token` exists with `secret: true` (value masked).
- Assert the page re-renders the masked-token state and the raw token never
  appears in the DOM (the existing security assertion at `:159`).

> If wiring a fully un-mocked test is impractical in the harness, the **minimum
> bar** is part (a) plus a check that the page reads `auth.slack.enabled` to
> un-gate — but prefer the real round-trip; a mocked-only suite is exactly what
> let this regress. Treat any flake as a bug to root-cause, never re-run blindly
> ([[feedback_flaky_tests_are_bugs]]).

### 4. Restart-to-apply hint (small UX, not a feature)

Because `Initialize` runs at startup and the supervisor is constructed at startup
from `cfg.Slack.SocketModeEnabled`, **saving in the UI does not connect until the
server restarts** — the status card will keep showing *Disconnected*. Add a short
inline note near the save button / status card so operators aren't confused, e.g.
`server:slack.socketMode.restartHint` = *"Changes take effect after the server
restarts."* Add the key to all locales (`en` authoritative; `fr`, `de`, `es`).
This is copy only — **no** hot-reload logic.

---

## Key files

| File | Change |
|---|---|
| `web/dash0/src/routes/orgs/$org/server.slack.tsx` | **~** 3 key constants `slack.*` → `auth.slack.*`; add restart hint |
| `server/internal/systemconfig/systemconfig_test.go` | **+** new test: `auth.slack.{enabled,socket_mode_enabled,app_token}` DB→cfg apply + env precedence |
| `web/dash0/e2e/slack-socket-mode.spec.ts` | **~** retarget mocks/asserts to `auth.slack.*`; **+** un-mocked round-trip test |
| `web/dash0/src/locales/{en,fr,de,es}/server.json` | **+** `slack.socketMode.restartHint` |
| `server/internal/integrations/slack/*` | **No change** — supervisor/dispatch/config are all correct |
| `server/internal/handlers/system/*` | **No change** — `SetParameter`'s lack of key validation is intentional (generic store) |

---

## Verification

```bash
make dev-test   # backend + dash0, SP_RUNMODE=test, port 4000
# 1. Enable Slack: Authentication settings → enable Slack (writes auth.slack.enabled),
#    or start with SP_SLACK_ENABLED=true.
# 2. Open /dash0/orgs/test/server/slack — the FORM renders (not the "not enabled" hint).
# 3. Toggle Socket Mode on, paste an xapp-… token, Save.
# 4. Confirm it persisted to the keys the backend reads:
TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"org":"test","email":"test@test.com","password":"test"}' \
  'http://localhost:4000/api/v1/auth/login' | jq -r '.accessToken')
curl -s -H "Authorization: Bearer $TOKEN" 'http://localhost:4000/api/v1/system/parameters' \
  | jq '.data[] | select(.key|test("auth\\.slack\\.(socket_mode_enabled|app_token)"))'
# Expect: auth.slack.socket_mode_enabled = true, auth.slack.app_token secret=true (masked).
# 5. Restart the server with a VALID xapp- token → status card flips to Connected.
make test-dash   # Playwright E2E (incl. the new round-trip)
make gotest      # systemconfig test
make lint        # keep dash0 to no-NEW lint errors ([[project_dash0_eslint_debt]])
```

Verify on **mobile width** too — the page must stay fully usable (per `CLAUDE.md`).

---

## Out of scope

- **Hot reload** — flipping the toggle / changing the token taking effect without
  a server restart (re-applying params live and starting/stopping the supervisor).
  Worth a follow-up spec; not here.
- **Per-org Socket Mode / multiple app tokens** — Socket Mode uses a single global
  App-Level Token (`cfg.Slack.AppToken`); this stays a system-level parameter.
- Any change to the supervisor, event/command/interaction dispatch, OAuth, or the
  HTTP webhook fallback — all already correct.

---

## Implementation Plan

Verified against source first:
- Backend reads `auth.slack.enabled` (`KeySlackEnabled`, systemconfig.go:65),
  `auth.slack.socket_mode_enabled` (`KeySlackSocketModeEnabled`, :55), and
  `auth.slack.app_token` (`KeySlackAppToken`, secret, :56). `Initialize` matches DB
  rows by exact key and applies env > db > default (:602-616).
- Frontend `server.slack.tsx:30-32` writes the bare `slack.*` namespace — never read.
- Repo-wide grep: the bare keys appear ONLY in `server.slack.tsx` (+ its E2E). No
  other consumer breaks when corrected.

Steps:
1. **Frontend key fix (core)** — `web/dash0/src/routes/orgs/$org/server.slack.tsx`:
   `KEY_ENABLED` → `auth.slack.enabled`, `KEY_SOCKET_ENABLED` →
   `auth.slack.socket_mode_enabled`, `KEY_APP_TOKEN` → `auth.slack.app_token`.
   Token write keeps `secret: true` (already correct).
2. **Restart-to-apply hint** — add `slack.socketMode.restartHint` to all four locales
   (`en` authoritative; `fr`/`de`/`es`) and render it via the shipped `Alert` +
   `AlertDescription` primitive near the save button.
3. **Backend regression guard** — new test in `systemconfig_test.go` using a real
   in-memory SQLite `db.Service` (`sqlite.New(ctx, sqlite.Config{InMemory: true})`,
   the standard pattern). Seed `auth.slack.{enabled,socket_mode_enabled,app_token}`
   via `SetSystemParameter`, run `Service.Initialize`, assert they land on
   `cfg.Slack.{Enabled,SocketModeEnabled,AppToken}`; plus an env-precedence case
   (`SP_SLACK_SOCKET_MODE_ENABLED` / `SP_SLACK_APP_TOKEN` override the DB value).
4. **E2E** — `web/dash0/e2e/slack-socket-mode.spec.ts`: retarget every mocked row
   and PUT assertion from `slack.*` to `auth.slack.*` (closes the mock gap that let
   the wrong keys pass CI), and add an un-mocked round-trip test against the live
   `make dev-test` backend that toggles Socket Mode, saves an `xapp-…` token, and
   asserts via `GET /api/v1/system/parameters` that `auth.slack.socket_mode_enabled`
   is persisted true and `auth.slack.app_token` exists with `secret: true`.

QA gates: `make build-backend lint-back test` (backend) + `make build-dash0` and
`bun run lint` (dash0, no NEW errors). E2E authored; run if a `SP_RUNMODE=test`
backend is reachable, otherwise authored-but-not-run.
