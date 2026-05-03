# Rename `gameserver` → `a2s` and add `minecraft` checker

## Overview

The current `gameserver` check is misleadingly named: it only speaks Valve's **A2S (Source Engine Query)** protocol on the Steam game query port (27015), which covers Counter-Strike, Team Fortress 2, Garry's Mod, Rust, ARK, etc. — but it has nothing to say about the entire universe of non-Source games (Minecraft, Quake, Battlefield, etc.).

This spec does two things, in order:

1. **Rename** `CheckTypeGameServer` → `CheckTypeA2S` (and the `checkgameserver` package → `checka2s`) so the type identifier matches the protocol it actually implements, consistent with the rest of the codebase (`http`, `mqtt`, `grpc`, `mssql`, …).
2. **Add** a new `checkminecraft` package that monitors Minecraft servers via the Server List Ping protocol (Java Edition over TCP/25565 and Bedrock Edition over RakNet UDP/19132).

**Out of scope — explicitly NOT doing:**
- No migration of existing `checks` rows whose `type = 'gameserver'`. This is a clean break. Any pre-existing checks of that type will fail validation after the rename and must be recreated. The user has confirmed they don't care about migrating existing data.
- No deprecated `gameserver` alias. The string `gameserver` disappears entirely.
- No shared "game server" abstraction. A2S and Minecraft are separate check types with separate packages, separate configs, separate metrics. Same as the rest of the registry — one protocol per package.

---

## Part A — Rename `gameserver` → `a2s`

### Naming chosen

| Old | New |
|---|---|
| `checkerdef.CheckTypeGameServer` (`"gameserver"`) | `checkerdef.CheckTypeA2S` (`"a2s"`) |
| package `checkgameserver` | package `checka2s` |
| `GameServerChecker` | `A2SChecker` |
| `GameServerConfig` | `A2SConfig` |
| Sample slug `game-server` | Sample slug `a2s-server` |
| Sample name `Game Server` | Sample name `A2S Server` |
| Sample default name `game-` + host | `a2s-` + host (in `resolveSlug`) |
| Locale key `gameserver` / `gameserverDescription` | `a2s` / `a2sDescription` |

`a2s` matches the codebase convention of short technical protocol identifiers. `sourcequery` was considered but is less precise (the protocol is technically named "A2S" by Valve in the spec).

### Backend file changes

| File | Change |
|---|---|
| `server/internal/checkers/checkgameserver/` | Rename directory to `checka2s/` |
| `server/internal/checkers/checka2s/checker.go` | `package checka2s`, rename `GameServerChecker` → `A2SChecker`, update doc comment to "Package checka2s provides Source engine A2S protocol monitoring." Also update the doc comment on `Type()` and `Validate()`. |
| `server/internal/checkers/checka2s/config.go` | `package checka2s`, rename `GameServerConfig` → `A2SConfig`. Update `resolveSlug` prefix from `"game-"` to `"a2s-"`. |
| `server/internal/checkers/checka2s/samples.go` | `package checka2s`. Sample now: `Name: "A2S Server"`, `Slug: "a2s-server"`. |
| `server/internal/checkers/checkerdef/types.go` | Replace `CheckTypeGameServer CheckType = "gameserver"` with `CheckTypeA2S CheckType = "a2s"`, update its doc comment, update the registry entry in `checkTypesRegistry` (description: `"Monitor Source engine game servers via the A2S query protocol"`), update the entry in `ListCheckTypes`. |
| `server/internal/checkers/registry/registry.go` | Replace import path, replace both switch cases (`GetChecker` and `ParseConfig`) to reference `checka2s.A2SChecker{}` / `checka2s.A2SConfig{}` keyed on `checkerdef.CheckTypeA2S`. |

### Frontend file changes (`web/dash0`)

| File | Change |
|---|---|
| `web/dash0/src/components/shared/check-form.tsx` | Replace `"gameserver"` with `"a2s"` in the `CheckType` union (line 33), in the dropdown options array (line 71 — relabel to `{ value: "a2s", label: "A2S Game Server", description: "Monitor Source engine game servers via A2S" }`), and in all three `case "gameserver":` switch arms (lines ~488, ~657, ~1085). |
| `web/dash0/src/api/hooks.ts` | Replace `"gameserver"` with `"a2s"` in both `CheckType` union literals (lines 41 and 77). |
| `web/dash0/src/locales/en/checks.json` | Rename keys `gameserver`/`gameserverDescription` → `a2s`/`a2sDescription`. New values: `"A2S Game Server"` / `"Monitor Source engine game servers via A2S"`. |
| `web/dash0/src/locales/fr/checks.json` | Same rename. Suggested values: `"Serveur de jeu A2S"` / `"Surveiller les serveurs de jeu Source via le protocole A2S"`. |
| `web/dash0/src/locales/de/checks.json` | Same rename. Suggested values: `"A2S-Gameserver"` / `"Source-Engine-Gameserver per A2S-Abfrage überwachen"`. |
| `web/dash0/src/locales/es/checks.json` | Same rename. Suggested values: `"Servidor de juego A2S"` / `"Monitorice servidores de juego Source mediante el protocolo A2S"`. |

### Documentation file changes

| File | Change |
|---|---|
| `docs/architecture.md` | Replace `checkgameserver/       # Game server queries` with `checka2s/             # Source engine A2S query` (line ~172). |
| `docs/conventions/checker-config.md` | Replace the `### `gameserver` -- Game server A2S query` section header (line ~471) with `### `a2s` -- Source engine A2S query`. Update any inline references. |

### Verification commands for the rename

```bash
# Should all return zero matches when the rename is complete:
grep -r "gameserver" --include="*.go" --include="*.ts" --include="*.tsx" --include="*.json" --include="*.md" .
grep -r "GameServer" --include="*.go" --include="*.ts" --include="*.tsx" .
grep -r "checkgameserver" .
```

(The `specs/done/2026/03/2026-03-28-01-steam-gameserver-monitoring.md` file legitimately contains historical references and must NOT be modified — it documents the original implementation.)

---

## Part B — Add `minecraft` checker

### Why a separate checker

Minecraft Server List Ping is a fundamentally different protocol from A2S:
- Different transport (TCP/25565 for Java; RakNet UDP/19132 for Bedrock) vs. A2S UDP/27015.
- Different handshake (varint-prefixed JSON status request for Java; RakNet unconnected ping for Bedrock).
- Different metric shape: Minecraft exposes MOTD, version + protocol number, sample player names, modded-server detection (Forge/Fabric); A2S exposes map name, VAC status, bot count.

So this lands as its own package, not as a sub-mode of A2S.

### Library choice

**`github.com/dreamscached/minequery/v2`** — supports both Java Edition (modern + legacy 1.6 / 1.4 / beta protocols) and Bedrock, MIT-licensed, actively maintained, no transitive runtime requirements.

Add to `go.mod` via `go get github.com/dreamscached/minequery/v2`.

### Type identifier

Add to `checkerdef/types.go`:

```go
// CheckTypeMinecraft performs Minecraft server health checks (Java + Bedrock editions).
CheckTypeMinecraft CheckType = "minecraft"
```

Append to `checkTypesRegistry`:

```go
{Type: CheckTypeMinecraft, Labels: []string{labelSafe, labelStandalone, labelCatOther}, Description: "Monitor Minecraft servers (Java + Bedrock)"},
```

Append to `ListCheckTypes` immediately after `CheckTypeA2S`.

### Package layout: `server/internal/checkers/checkminecraft/`

Mirror the structure of `checka2s/`:

```
checkminecraft/
├── checker.go    # MinecraftChecker
├── config.go     # MinecraftConfig
├── samples.go    # GetSampleConfigs
└── checker_test.go
```

### Config

```go
type MinecraftConfig struct {
    Host       string        `json:"host"`
    Port       int           `json:"port,omitempty"`       // default depends on Edition
    Edition    string        `json:"edition,omitempty"`    // "java" (default) or "bedrock"
    Timeout    time.Duration `json:"timeout,omitempty"`    // default 10s, max 30s
    MinPlayers int           `json:"minPlayers,omitempty"` // 0 disables lower bound
    MaxPlayers int           `json:"maxPlayers,omitempty"` // 0 disables upper bound
}
```

#### Default port resolution

| Edition | Default port |
|---|---|
| `java` (and unset) | 25565 |
| `bedrock` | 19132 |

#### Validation rules

| Field | Rule |
|---|---|
| `host` | required, non-empty |
| `port` | 0–65535 (0 means "use default for edition") |
| `edition` | empty / `"java"` / `"bedrock"` only — anything else is a config error |
| `timeout` | 0 (use default) or `> 0 && <= 30s` |
| `minPlayers` | `>= 0` |
| `maxPlayers` | `>= 0` |

#### `FromMap` / `GetConfig`

Match the pattern in `checkgameserver/config.go`: handle both `int` and `float64` variants for numeric fields (since YAML/JSON unmarshalling can produce either), parse `timeout` as a duration string, omit defaulted values from the round-trip output.

### Checker behavior

```go
func (c *MinecraftChecker) Type() checkerdef.CheckType {
    return checkerdef.CheckTypeMinecraft
}
```

`Validate(spec)` follows the A2S pattern — fill default `spec.Name` and `spec.Slug` if empty (slug prefix: `mc-` + sanitized host).

`Execute(ctx, config)`:

1. Resolve target host/port via the edition default.
2. Construct `minequery.NewPinger(...)` with `WithTimeout(timeout)`.
3. Branch on edition:
   - `java`: `pinger.Ping17(host, port)` (Modern Minecraft 1.7+ status protocol). On success, returned struct exposes `OnlinePlayers`, `MaxPlayers`, `Version.Name`, `Version.Protocol`, `Description.String()`, `SamplePlayers`.
   - `bedrock`: `pinger.PingBedrock(host, port)`. Returned struct exposes `OnlinePlayers`, `MaxPlayers`, `MOTD`, `ServerName`, `GameMode`, `ProtocolVersion`.
4. On query error: return `StatusDown` (or `StatusTimeout` if `ctx.Err() != nil`).
5. On success: build metrics (`query_time_ms`, `players`, `maxPlayers`, `protocol`) and output (`host`, `port`, `edition`, `motd`, `version`, `players`, `maxPlayers`, plus `gameMode` for Bedrock).
6. Apply the same `MinPlayers` / `MaxPlayers` thresholds as the A2S checker — out-of-band player counts produce `StatusDown` with `output["error"]` set.

### Samples

```go
func (c *MinecraftChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
    return []checkerdef.CheckSpec{
        {
            Name:   "Minecraft Java Server",
            Slug:   "minecraft-java",
            Period: 5 * time.Minute,
            Config: (&MinecraftConfig{
                Host:    "play.example.com",
                Edition: "java",
            }).GetConfig(),
        },
        {
            Name:   "Minecraft Bedrock Server",
            Slug:   "minecraft-bedrock",
            Period: 5 * time.Minute,
            Config: (&MinecraftConfig{
                Host:    "bedrock.example.com",
                Edition: "bedrock",
            }).GetConfig(),
        },
    }
}
```

### Registry wiring

In `server/internal/checkers/registry/registry.go`:

- Add import for `checkminecraft`.
- Add `case checkerdef.CheckTypeMinecraft: return &checkminecraft.MinecraftChecker{}, true` to `GetChecker`.
- Add `case checkerdef.CheckTypeMinecraft: return &checkminecraft.MinecraftConfig{}, true` to `ParseConfig`.

### Frontend wiring

| File | Change |
|---|---|
| `web/dash0/src/components/shared/check-form.tsx` | Add `"minecraft"` to the `CheckType` union (line 33), add a dropdown entry `{ value: "minecraft", label: "Minecraft", description: "Monitor Minecraft servers (Java + Bedrock)" }` (after the `"a2s"` entry), and add a `case "minecraft":` arm in the three switch statements that build the config payload, render the form fields, and produce the summary view. The form should expose: `host`, `port` (optional), `edition` (select: java/bedrock), `minPlayers`, `maxPlayers`. |
| `web/dash0/src/api/hooks.ts` | Add `"minecraft"` to both `CheckType` union literals. |
| `web/dash0/src/locales/{en,fr,de,es}/checks.json` | Add `minecraft` and `minecraftDescription` entries alongside the new `a2s` keys. EN: `"Minecraft"` / `"Monitor Minecraft servers (Java + Bedrock)"`. FR: `"Minecraft"` / `"Surveiller les serveurs Minecraft (Java + Bedrock)"`. DE: `"Minecraft"` / `"Minecraft-Server (Java + Bedrock) überwachen"`. ES: `"Minecraft"` / `"Monitorice servidores Minecraft (Java + Bedrock)"`. |

### Documentation

- `docs/architecture.md` — add a `checkminecraft/       # Minecraft Server List Ping` line in the checkers tree near `checka2s/`.
- `docs/conventions/checker-config.md` — add a `### `minecraft` -- Minecraft Server List Ping` section after the `a2s` one, documenting the config fields and edition selector.

---

## Testing

### Backend unit tests (mirror existing `checkers/*/checker_test.go` style)

`server/internal/checkers/checka2s/checker_test.go` — port any tests that exist in the old file (the directory currently has no `_test.go`; if absent, write a fresh minimal table-driven test for `Validate`).

`server/internal/checkers/checkminecraft/checker_test.go` — table-driven, `t.Parallel()`, `testify/require`. Cover:

| Test | What it asserts |
|---|---|
| `TestMinecraftConfig_Validate` | host required; bad port rejected; bad edition rejected; bad timeout rejected; min/max player negative rejected. |
| `TestMinecraftConfig_FromMap_DefaultsAndCoercion` | int / float64 coercion for numeric fields; timeout parsed as duration; unknown edition flagged. |
| `TestMinecraftConfig_GetConfig_RoundTrip` | defaults are omitted; explicit values preserved. |
| `TestMinecraftChecker_Type` | returns `CheckTypeMinecraft`. |
| `TestMinecraftChecker_Validate_FillsDefaults` | name/slug populated from host when blank. |

`Execute` against a real server is best left to manual verification (no test container for Minecraft). If a future test wants to exercise the wire format, `minequery` ships an in-memory mock pinger.

### Registry tests

If `server/internal/checkers/registry/registry_test.go` exists, add coverage that `GetChecker(CheckTypeA2S)` and `GetChecker(CheckTypeMinecraft)` both return non-nil. Same for `ParseConfig`.

### Manual verification

```bash
# After rebuild — confirm the new types appear and the old one is gone:
TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"org":"default","email":"admin@solidping.com","password":"solidpass"}' \
  'http://localhost:4000/api/v1/auth/login' | jq -r '.accessToken')

curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:4000/api/v1/check-types' | jq '.data[].type' | grep -E 'a2s|minecraft|gameserver'
# Expected: "a2s" and "minecraft" present, "gameserver" absent.

# Create an A2S check (renamed):
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"CS2 server","slug":"cs2","type":"a2s","config":{"host":"<test-host>","port":27015}}' \
  'http://localhost:4000/api/v1/orgs/default/checks' | jq '.'

# Create a Minecraft Java check:
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"Hypixel","slug":"hypixel","type":"minecraft","config":{"host":"mc.hypixel.net","edition":"java"}}' \
  'http://localhost:4000/api/v1/orgs/default/checks' | jq '.'

# Create a Minecraft Bedrock check:
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"Bedrock test","slug":"bedrock","type":"minecraft","config":{"host":"<bedrock-host>","edition":"bedrock"}}' \
  'http://localhost:4000/api/v1/orgs/default/checks' | jq '.'

# Trigger a one-shot run via the test admin page or wait for the scheduled period,
# then verify results contain players / version / motd output:
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:4000/api/v1/orgs/default/results?checkUid=<uid>&limit=1' | jq '.data[0].output'
```

### Frontend verification

Run `make dev-test`, open the dash0 app, and verify:
- The "Add check" type dropdown shows **A2S Game Server** (no longer "Game Server") and **Minecraft**, with description text rendered from the locale file.
- Submitting an A2S check produces a stored check with `type: "a2s"`.
- Submitting a Minecraft check renders the edition selector and produces `type: "minecraft"` with the correct config map.
- Switch the UI language to FR / DE / ES and confirm both new entries are translated.

---

## Files affected — checklist

### Renamed / modified (Part A)
- [ ] `server/internal/checkers/checkgameserver/` → `server/internal/checkers/checka2s/` (directory rename)
- [ ] `server/internal/checkers/checka2s/checker.go` (package + symbol rename)
- [ ] `server/internal/checkers/checka2s/config.go` (package + symbol rename, slug prefix)
- [ ] `server/internal/checkers/checka2s/samples.go` (package + sample name/slug)
- [ ] `server/internal/checkers/checkerdef/types.go` (constant rename, registry entry, list)
- [ ] `server/internal/checkers/registry/registry.go` (import + 2 switch cases)
- [ ] `web/dash0/src/components/shared/check-form.tsx` (5 occurrences)
- [ ] `web/dash0/src/api/hooks.ts` (2 occurrences)
- [ ] `web/dash0/src/locales/en/checks.json`
- [ ] `web/dash0/src/locales/fr/checks.json`
- [ ] `web/dash0/src/locales/de/checks.json`
- [ ] `web/dash0/src/locales/es/checks.json`
- [ ] `docs/architecture.md`
- [ ] `docs/conventions/checker-config.md`

### New (Part B)
- [ ] `server/internal/checkers/checkminecraft/checker.go`
- [ ] `server/internal/checkers/checkminecraft/config.go`
- [ ] `server/internal/checkers/checkminecraft/samples.go`
- [ ] `server/internal/checkers/checkminecraft/checker_test.go`
- [ ] `go.mod` / `go.sum` — adds `github.com/dreamscached/minequery/v2`

### Modified for Part B
- [ ] `server/internal/checkers/checkerdef/types.go` (new constant, registry entry, list)
- [ ] `server/internal/checkers/registry/registry.go` (import + 2 switch cases)
- [ ] `web/dash0/src/components/shared/check-form.tsx` (union, dropdown entry, 3 switch arms)
- [ ] `web/dash0/src/api/hooks.ts` (2 unions)
- [ ] `web/dash0/src/locales/{en,fr,de,es}/checks.json` (2 new keys each)
- [ ] `docs/architecture.md`, `docs/conventions/checker-config.md`

### Lint / build verification

After all changes:

```bash
make lint
make test
make build
```

All three must pass with zero warnings; if `golangci-lint` flags anything, fix the code rather than relaxing the linter (per project policy).

---

**Status**: Todo | **Created**: 2026-05-02

---

## Implementation Plan

1. **Part A (rename)**: rename `checkgameserver/` package directory to `checka2s/`, rename `GameServerChecker`→`A2SChecker`, `GameServerConfig`→`A2SConfig`, update slug prefix `game-`→`a2s-`, update sample name/slug, update `CheckTypeGameServer`→`CheckTypeA2S` (`"a2s"`) in `checkerdef/types.go`, update registry switches, update frontend `check-form.tsx` / `hooks.ts` / locale JSON for en/fr/de/es, update docs (`architecture.md`, `checker-config.md`).
2. **Part B (minecraft)**: add `github.com/dreamscached/minequery/v2` dependency, create `checkminecraft/` package with `MinecraftChecker`, `MinecraftConfig` (host, port, edition, timeout, minPlayers, maxPlayers), samples (java + bedrock), and unit tests; add `CheckTypeMinecraft` constant + registry entry + list, register in switches, wire frontend (union, dropdown, switch arms, locale strings), update docs.
3. **Tests**: add minimal `checker_test.go` for `checka2s` (validate-only) and a comprehensive table-driven test for `checkminecraft` covering Validate, FromMap, GetConfig round-trip, Type, and default-fill behavior.
4. **QA**: run `make build-backend build-client lint-back test`; fix anything that fails.
