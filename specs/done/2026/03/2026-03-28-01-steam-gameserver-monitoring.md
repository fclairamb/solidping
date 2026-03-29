# Steam Game Server Monitoring

## Overview

Add a game server health check that queries game servers using Valve's A2S (Any to Server) query protocol. This is the standard protocol used by Steam, Source Engine, and GoldSrc games. The check verifies the server is online, responding to queries, and reports server metadata (player count, map, game mode).

**Use cases:**
- Monitor dedicated game servers (CS2, TF2, Garry's Mod, Valheim, Rust, ARK, etc.)
- Track player count and alert when server is empty or full
- Monitor map rotation and game mode
- Detect crashed or hung servers that still have open ports but don't respond to queries
- Public status page for gaming communities showing server availability
- Alert when player count drops to 0 (server may need restart)

## Check Type
Type: `gameserver`

---

## Backend

### Package: `server/internal/checkers/checkgameserver/`

| File | Description |
|------|-------------|
| `config.go` | `GameServerConfig` struct with `FromMap()` / `GetConfig()` |
| `checker.go` | `GameServerChecker` implementing `Checker` interface |
| `errors.go` | Package-level error sentinel values |
| `samples.go` | `GetSampleConfigs()` with example configurations |
| `checker_test.go` | Table-driven tests |

### Configuration (`GameServerConfig`)

```go
type GameServerConfig struct {
    Host         string        `json:"host"`
    Port         int           `json:"port,omitempty"`
    Timeout      time.Duration `json:"timeout,omitempty"`
    MinPlayers   int           `json:"min_players,omitempty"`
    MaxPlayers   int           `json:"max_players,omitempty"`
}
```

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `host` | string | **yes** | — | Game server hostname or IP |
| `port` | int | no | `27015` | Query port (usually game port or game port + 1) |
| `timeout` | duration | no | `10s` | Query timeout |
| `min_players` | int | no | — | Minimum player count to be considered "up" (0 = don't check) |
| `max_players` | int | no | — | Alert if player count exceeds this (0 = don't check) |

### Validation Rules

- `host` is required
- `port` must be between 1 and 65535
- `timeout` must be > 0 and ≤ 30s
- `min_players` must be ≥ 0
- `max_players` must be ≥ 0
- Auto-generate `spec.Name` as `host:port` if empty
- Auto-generate `spec.Slug` as `game-{host}` if empty

### Execution Behavior

The A2S protocol is a UDP-based query protocol:

1. Create UDP connection to `host:port`
2. Set read deadline based on timeout
3. Record `t0`
4. Send A2S_INFO query packet (`\xFF\xFF\xFF\xFF\x54Source Engine Query\x00`)
5. Handle challenge response if received (A2S uses challenge-response since Dec 2020):
   - If response is `\xFF\xFF\xFF\xFF\x41` + 4-byte challenge, resend query with challenge appended
6. Parse A2S_INFO response:
   - Protocol version
   - Server name
   - Current map
   - Game directory
   - Game description
   - Player count / Max players
   - Bot count
   - Server type (dedicated/listen/proxy)
   - OS (Linux/Windows/macOS)
   - Password protected
   - VAC secured
7. Record `t1` — compute `query_time_ms`
8. Validate player count against `min_players` / `max_players` thresholds
9. Return result

**Status mapping:**

| Condition | Status |
|-----------|--------|
| Server responds to A2S_INFO | `StatusUp` |
| Player count below `min_players` | `StatusDown` |
| Player count above `max_players` | `StatusDown` |
| No response (server offline or firewalled) | `StatusDown` |
| Malformed response | `StatusDown` |
| Context deadline exceeded | `StatusTimeout` |
| Invalid configuration | `StatusError` |

### Metrics

| Key | Type | Description |
|-----|------|-------------|
| `query_time_ms` | float64 | A2S query response time |
| `players` | int | Current player count |
| `max_players` | int | Server max player slots |
| `bots` | int | Current bot count |

### Output

| Key | Type | Description |
|-----|------|-------------|
| `host` | string | Server address |
| `port` | int | Query port |
| `server_name` | string | Server name |
| `map` | string | Current map |
| `game` | string | Game name/directory |
| `players` | int | Player count |
| `max_players` | int | Max player slots |
| `bots` | int | Bot count |
| `server_type` | string | Server type (dedicated/listen) |
| `os` | string | Server OS |
| `password_protected` | bool | Whether server requires password |
| `vac_secured` | bool | Whether VAC is enabled |
| `error` | string | Error message if check failed |

### Go Driver

Use `github.com/rumblefrog/go-a2s` (Go implementation of A2S protocol). Add to `server/go.mod`.

```go
import "github.com/rumblefrog/go-a2s"

func queryServer(host string, port int, timeout time.Duration) (*a2s.ServerInfo, error) {
    client, err := a2s.NewClient(
        net.JoinHostPort(host, strconv.Itoa(port)),
        a2s.SetMaxPacketSize(14000),
        a2s.TimeoutOption(timeout),
    )
    if err != nil {
        return nil, err
    }
    defer client.Close()

    return client.QueryInfo()
}
```

### A2S Protocol Reference

The A2S protocol is documented at: https://developer.valvesoftware.com/wiki/Server_queries

Key packet types:
- `A2S_INFO` (0x54): Query server info
- `A2S_PLAYER` (0x55): Query player list (future enhancement)
- `A2S_RULES` (0x56): Query server rules/cvars (future enhancement)

### Supported Games (non-exhaustive)

Any game using Source Engine or implementing the A2S protocol:
- Counter-Strike 2 / CS:GO
- Team Fortress 2
- Garry's Mod
- Left 4 Dead 2
- Rust
- ARK: Survival Evolved
- Valheim (with A2S query enabled)
- DayZ
- Unturned
- 7 Days to Die

### Testing

**Test cases** (table-driven):
1. **Happy path** — mock A2S response, expect `StatusUp` with server info
2. **Player count within range** — expect `StatusUp`
3. **Player count below minimum** — expect `StatusDown`
4. **Player count above maximum** — expect `StatusDown`
5. **Challenge-response flow** — mock challenge, verify re-query
6. **No response** — timeout, expect `StatusTimeout`
7. **Malformed response** — garbage data, expect `StatusDown`
8. **Missing host** — validation error

For integration tests, use a public game server or run a lightweight Source dedicated server in a container.

### Limitations

- UDP-based protocol — firewalls may block queries even if game port is open
- Some games use non-standard query ports (port offset varies by game)
- No player list or rules query in initial implementation
- Anti-DDoS services on some game servers may rate-limit or block A2S queries
- GoldSrc (Half-Life 1 era) servers may use slightly different response format
- Some Rust servers disable A2S queries

### Future Enhancements

- A2S_PLAYER query for detailed player list
- A2S_RULES query for server configuration
- GameDig integration for non-Source games (Minecraft, FiveM, etc.)
- Player count trend alerting (empty for > N minutes)
- Multi-game protocol support via pluggable query backends

---

## Frontend

### Form Fields

```tsx
case "gameserver":
  return (
    <>
      <div className="space-y-2">
        <Label>Host</Label>
        <div className="flex gap-2">
          <Input id="host" type="text" placeholder="game.example.com"
            value={host} onChange={(e) => setHost(e.target.value)} className="flex-1" />
          <Input id="port" type="number" placeholder="27015"
            value={port} onChange={(e) => setPort(e.target.value)} className="w-24" />
        </div>
      </div>
      <div className="flex gap-4">
        <div className="space-y-2 flex-1">
          <Label htmlFor="minPlayers">Min Players (optional)</Label>
          <Input id="minPlayers" type="number" min={0} placeholder="0"
            value={minPlayers} onChange={(e) => setMinPlayers(e.target.value)} />
          <p className="text-xs text-muted-foreground">Alert if fewer players</p>
        </div>
        <div className="space-y-2 flex-1">
          <Label htmlFor="maxPlayers">Max Players (optional)</Label>
          <Input id="maxPlayers" type="number" min={0} placeholder="0"
            value={maxPlayers} onChange={(e) => setMaxPlayers(e.target.value)} />
          <p className="text-xs text-muted-foreground">Alert if more players</p>
        </div>
      </div>
    </>
  );
```

---

## Key Files

| File | Action |
|------|--------|
| `server/internal/checkers/checkgameserver/config.go` | New |
| `server/internal/checkers/checkgameserver/checker.go` | New |
| `server/internal/checkers/checkgameserver/errors.go` | New |
| `server/internal/checkers/checkgameserver/samples.go` | New |
| `server/internal/checkers/checkgameserver/checker_test.go` | New |
| `server/internal/checkers/checkerdef/types.go` | Modify |
| `server/internal/checkers/registry/registry.go` | Modify |
| `server/go.mod` | Modify (add `github.com/rumblefrog/go-a2s`) |
| `web/dash0/src/components/shared/check-form.tsx` | Modify |
| `web/dash0/src/locales/en/checks.json` | Modify |
| `web/dash0/src/locales/fr/checks.json` | Modify |

## Verification

- [ ] `make lint` passes
- [ ] `make test` passes
- [ ] Create a game server check via the UI
- [ ] Verify server info is returned (name, map, players)
- [ ] Verify player count thresholds work
- [ ] Verify offline server shows `StatusDown`
- [ ] Verify metrics include query time and player count
- [ ] E2E tests pass

**Status**: Draft | **Created**: 2026-03-28
