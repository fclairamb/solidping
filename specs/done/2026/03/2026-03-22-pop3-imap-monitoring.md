# POP3/IMAP Monitoring

## Overview

Add POP3 and IMAP email protocol monitoring as new check types. Verifies mail retrieval servers are reachable and responding correctly. Supported by Pingdom, BetterStack, and Gatus (via STARTTLS).

## Goals

1. Verify POP3/POP3S servers respond with valid greeting
2. Verify IMAP/IMAPS servers respond with valid greeting
3. Support both implicit TLS and STARTTLS
4. Measure connection and response time

---

## Check Types

### POP3

Type: `pop3`

#### Settings

| Field | Type | JSON key | Description |
|-------|------|----------|-------------|
| `Host` | string | `host` | Target hostname or IP (required) |
| `Port` | int | `port` | Target port (default: 110, or 995 when `tls` is true) |
| `TLS` | bool | `tls` | Use implicit TLS / POP3S (default: false) |
| `StartTLS` | bool | `starttls` | Upgrade to TLS via STLS command (default: false) |
| `TLSVerify` | bool | `tls_verify` | Verify TLS certificate (default: false) |
| `TLSServerName` | string | `tls_server_name` | Override SNI hostname for TLS |
| `Timeout` | duration | `timeout` | Connection timeout (default: 10s, max: 60s) |
| `ExpectGreeting` | string | `expect_greeting` | Substring to match in greeting banner |

#### Behavior

1. Resolve `host` to IP addresses
2. Connect to `host:port` via TCP (with TLS handshake if `tls` is true)
3. Read greeting line — expect `+OK` prefix
4. If `expect_greeting` is set, verify greeting contains the substring
5. If `starttls` is true, send `STLS\r\n`, expect `+OK`, then upgrade to TLS
6. Send `QUIT\r\n` to cleanly disconnect
7. Record greeting, connection time, and TLS info in output

#### Default Ports

| Mode | Port |
|------|------|
| Plain | 110 |
| Implicit TLS (`tls: true`) | 995 |
| STARTTLS (`starttls: true`) | 110 |

### IMAP

Type: `imap`

#### Settings

| Field | Type | JSON key | Description |
|-------|------|----------|-------------|
| `Host` | string | `host` | Target hostname or IP (required) |
| `Port` | int | `port` | Target port (default: 143, or 993 when `tls` is true) |
| `TLS` | bool | `tls` | Use implicit TLS / IMAPS (default: false) |
| `StartTLS` | bool | `starttls` | Upgrade to TLS via STARTTLS command (default: false) |
| `TLSVerify` | bool | `tls_verify` | Verify TLS certificate (default: false) |
| `TLSServerName` | string | `tls_server_name` | Override SNI hostname for TLS |
| `Timeout` | duration | `timeout` | Connection timeout (default: 10s, max: 60s) |
| `ExpectGreeting` | string | `expect_greeting` | Substring to match in greeting banner |

#### Behavior

1. Resolve `host` to IP addresses
2. Connect to `host:port` via TCP (with TLS handshake if `tls` is true)
3. Read greeting line — expect `* OK` prefix
4. If `expect_greeting` is set, verify greeting contains the substring
5. If `starttls` is true, send `a001 STARTTLS\r\n`, expect `a001 OK`, then upgrade to TLS
6. Send `a002 LOGOUT\r\n` to cleanly disconnect
7. Record greeting, connection time, and TLS info in output

#### Default Ports

| Mode | Port |
|------|------|
| Plain | 143 |
| Implicit TLS (`tls: true`) | 993 |
| STARTTLS (`starttls: true`) | 143 |

---

## Backend Implementation

### Files to create

| File | Purpose |
|------|---------|
| `back/internal/checkers/checkpop3/config.go` | `POP3Config` struct with `FromMap`, `GetConfig`, `Validate` |
| `back/internal/checkers/checkpop3/checker.go` | `POP3Checker` struct with `Type`, `Validate`, `Execute` |
| `back/internal/checkers/checkpop3/checker_test.go` | Table-driven tests with mock POP3 server |
| `back/internal/checkers/checkimap/config.go` | `IMAPConfig` struct with `FromMap`, `GetConfig`, `Validate` |
| `back/internal/checkers/checkimap/checker.go` | `IMAPChecker` struct with `Type`, `Validate`, `Execute` |
| `back/internal/checkers/checkimap/checker_test.go` | Table-driven tests with mock IMAP server |

### Files to modify

| File | Change |
|------|--------|
| `back/internal/checkers/checkerdef/types.go` | Add `CheckTypePOP3 = "pop3"` and `CheckTypeIMAP = "imap"` constants, add to `ListCheckTypes()` |
| `back/internal/checkers/registry/registry.go` | Add `checkpop3` and `checkimap` imports, add cases to `GetChecker()` and `ParseConfig()` |

### Go Types

#### POP3 Config (`checkpop3/config.go`)

```go
const (
    defaultPort        = 110
    defaultTLSPort     = 995
    defaultTimeout     = 10 * time.Second
    maxTimeout         = 60 * time.Second
)

type POP3Config struct {
    Host           string        `json:"host"`
    Port           int           `json:"port,omitempty"`
    TLS            bool          `json:"tls,omitempty"`
    StartTLS       bool          `json:"starttls,omitempty"`
    TLSVerify      bool          `json:"tls_verify,omitempty"`      //nolint:tagliatelle
    TLSServerName  string        `json:"tls_server_name,omitempty"` //nolint:tagliatelle
    Timeout        time.Duration `json:"timeout,omitempty"`
    ExpectGreeting string        `json:"expect_greeting,omitempty"` //nolint:tagliatelle
}
```

Follow the same `FromMap`/`GetConfig`/`Validate` pattern as `checksmtp/config.go`.

**Validation rules:**
- `host` is required
- `port` must be 0–65535
- `timeout` must be > 0 and <= 60s (if set)
- `tls` and `starttls` cannot both be true
- `starttls` cannot be used with port 995 (implicit TLS port)

#### IMAP Config (`checkimap/config.go`)

```go
const (
    defaultPort        = 143
    defaultTLSPort     = 993
    defaultTimeout     = 10 * time.Second
    maxTimeout         = 60 * time.Second
)

type IMAPConfig struct {
    Host           string        `json:"host"`
    Port           int           `json:"port,omitempty"`
    TLS            bool          `json:"tls,omitempty"`
    StartTLS       bool          `json:"starttls,omitempty"`
    TLSVerify      bool          `json:"tls_verify,omitempty"`      //nolint:tagliatelle
    TLSServerName  string        `json:"tls_server_name,omitempty"` //nolint:tagliatelle
    Timeout        time.Duration `json:"timeout,omitempty"`
    ExpectGreeting string        `json:"expect_greeting,omitempty"` //nolint:tagliatelle
}
```

Same `FromMap`/`GetConfig`/`Validate` pattern. Same validation rules, but implicit TLS port is 993 instead of 995.

### Checker Behavior

Both checkers use `net.Dialer` with context deadline and `net/textproto.Conn` for line-based protocol exchange (same as SMTP checker).

#### Auto-generated Name/Slug

In `Validate()`:
- POP3: `Name = "POP3: " + host`, `Slug = "pop3-" + host`
- IMAP: `Name = "IMAP: " + host`, `Slug = "imap-" + host`

#### Default Port Resolution

```go
func resolvePort(cfg *POP3Config) int {
    if cfg.Port != 0 { return cfg.Port }
    if cfg.TLS { return defaultTLSPort }
    return defaultPort
}
```

Same pattern for IMAP with its own port constants.

### Metrics

| Key | Description |
|-----|-------------|
| `connection_time_ms` | TCP dial duration |
| `greeting_time_ms` | Time to receive greeting line |
| `starttls_time_ms` | STARTTLS + TLS handshake (when applicable) |
| `total_time_ms` | Full check duration |

### Output

| Key | Description |
|-----|-------------|
| `host` | Resolved IP address |
| `port` | Connected port |
| `greeting` | Full greeting banner text |
| `tls_version` | TLS version string (when TLS used) |
| `tls_cipher` | Cipher suite name (when TLS used) |
| `error` | Error message (on failure) |

### Status Mapping

| Condition | Status |
|-----------|--------|
| Greeting received and valid | `StatusUp` |
| Greeting missing or invalid | `StatusDown` |
| Context deadline exceeded | `StatusTimeout` |
| DNS/network error | `StatusError` |

---

## Dashboard UI

### Files to modify

| File | Change |
|------|--------|
| `apps/dash0/src/components/shared/check-form.tsx` | Add `pop3` and `imap` to `CheckType` union, `checkTypes` array, `defaultPeriod`, form validation, and `renderConfigFields` |
| `apps/dash0/src/routes/orgs/$org/checks.index.tsx` | Add target extraction for POP3/IMAP (`config.host`) |

### Check Type Entries

Add to `checkTypes` array in `check-form.tsx`:

```typescript
{ value: "pop3", label: "POP3", description: "Check POP3 mail server availability" },
{ value: "imap", label: "IMAP", description: "Check IMAP mail server availability" },
```

Add to `CheckType` union:

```typescript
type CheckType = "http" | "tcp" | "icmp" | "dns" | "ssl" | "heartbeat" | "domain" | "smtp" | "pop3" | "imap";
```

### Default Period

POP3 and IMAP use `"01:00:00"` (1 hour), same as SMTP/DNS/SSL.

### Form Fields

Both POP3 and IMAP render the same form layout (similar to SMTP but simpler — no EHLO domain or AUTH):

```
┌─────────────────────────────────────────┐
│ Host                         Port       │
│ [mail.example.com          ] [110 ]     │
├─────────────────────────────────────────┤
│ ☐ Use implicit TLS (POP3S/IMAPS)       │
│ ☐ Use STARTTLS                          │
│ ☐ Verify TLS certificate               │
├─────────────────────────────────────────┤
│ Expected Greeting (optional)            │
│ [                                     ] │
└─────────────────────────────────────────┘
```

#### Form Validation

In the `handleSubmit` switch:

```typescript
case "pop3":
case "imap":
  if (!host) { setError("Host is required"); return; }
  config.host = host;
  if (port) config.port = parseInt(port, 10);
  if (tls) config.tls = true;
  if (startTLS) config.starttls = true;
  if (tlsVerify) config.tls_verify = true;
  if (expectGreeting) config.expect_greeting = expectGreeting;
  break;
```

#### renderConfigFields

```typescript
case "pop3":
case "imap":
  return (
    <>
      <div className="space-y-2">
        <Label>Host</Label>
        <div className="flex gap-2">
          <Input id="host" type="text" placeholder="mail.example.com"
            value={host} onChange={(e) => setHost(e.target.value)}
            className="flex-1" data-testid="check-host-input" />
          <Input id="port" type="number"
            placeholder={type === "pop3" ? "110" : "143"}
            value={port} onChange={(e) => setPort(e.target.value)}
            className="w-24" data-testid="check-port-input" />
        </div>
      </div>
      <div className="space-y-3">
        <label className="flex items-center gap-2">
          <Checkbox checked={tls}
            onCheckedChange={(v) => setTls(v === true)}
            data-testid="check-tls-checkbox" />
          <span className="text-sm">
            Use implicit TLS ({type === "pop3" ? "POP3S" : "IMAPS"})
          </span>
        </label>
        <label className="flex items-center gap-2">
          <Checkbox checked={startTLS}
            onCheckedChange={(v) => setStartTLS(v === true)}
            data-testid="check-starttls-checkbox" />
          <span className="text-sm">Use STARTTLS</span>
        </label>
        <label className="flex items-center gap-2">
          <Checkbox checked={tlsVerify}
            onCheckedChange={(v) => setTlsVerify(v === true)}
            data-testid="check-tls-verify-checkbox" />
          <span className="text-sm">Verify TLS certificate</span>
        </label>
      </div>
      <div className="space-y-2">
        <Label htmlFor="expectGreeting">Expected Greeting (optional)</Label>
        <Input id="expectGreeting" type="text"
          placeholder={type === "pop3" ? "+OK" : "* OK"}
          value={expectGreeting}
          onChange={(e) => setExpectGreeting(e.target.value)}
          data-testid="check-expect-greeting-input" />
      </div>
    </>
  );
```

#### New State Variables

Add `tls` state variable (the existing `startTLS`, `tlsVerify`, and `expectGreeting` variables from SMTP can be reused):

```typescript
const [tls, setTls] = useState(false);
```

Initialize from existing check config in edit mode:

```typescript
if (check?.config?.tls) setTls(check.config.tls as boolean);
```

### Check List Target Display

In `checks.index.tsx`, add POP3/IMAP to the target extraction logic alongside existing types:

```typescript
// Target column: show host for POP3/IMAP (same as SMTP)
case "pop3":
case "imap":
case "smtp":
  return config.host || slug;
```

### Check Detail Page

No new components needed. The existing config key-value display on the check detail page (`checks.$checkUid.index.tsx`) will automatically render all POP3/IMAP config fields. The existing output display will show greeting, TLS info, and error fields from the result.

---

## API Examples

### Create POP3 check (plain)

```bash
curl -s -X POST -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"type":"pop3","config":{"host":"mail.example.com"}}' \
  'http://localhost:4000/api/v1/orgs/default/checks' | jq '.'
```

### Create POP3S check (implicit TLS)

```bash
curl -s -X POST -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"type":"pop3","config":{"host":"mail.example.com","tls":true}}' \
  'http://localhost:4000/api/v1/orgs/default/checks' | jq '.'
```

### Create IMAP check with STARTTLS

```bash
curl -s -X POST -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"type":"imap","config":{"host":"imap.example.com","starttls":true,"tls_verify":true}}' \
  'http://localhost:4000/api/v1/orgs/default/checks' | jq '.'
```

---

## Tests

### Backend

Table-driven tests in `checker_test.go` for each protocol, using a mock TCP server:

| Test case | Description |
|-----------|-------------|
| Valid greeting | Mock server sends `+OK` / `* OK`, expect `StatusUp` |
| Invalid greeting | Mock server sends wrong prefix, expect `StatusDown` |
| Greeting match | `expect_greeting` substring check passes/fails |
| STARTTLS upgrade | Mock STLS/STARTTLS exchange, verify TLS upgrade |
| Implicit TLS | Connect with TLS from start |
| TLS + STARTTLS conflict | Config validation rejects both set |
| Connection refused | Expect `StatusError` |
| Timeout | Slow mock server, expect `StatusTimeout` |
| Config roundtrip | `FromMap` → `GetConfig` → `FromMap` preserves values |
| Port defaults | Verify correct default port for plain/TLS modes |

### Frontend

Playwright E2E tests:

| Test case | Description |
|-----------|-------------|
| Create POP3 check | Fill form, submit, verify check appears in list |
| Create IMAP check | Fill form with STARTTLS, submit, verify |
| Edit POP3 check | Load existing, modify host, save |
| Type-specific placeholder | Verify port placeholder shows 110 for POP3, 143 for IMAP |

---

## Implementation Steps

1. **Backend: POP3 checker**
   - Create `checkpop3/config.go` with `POP3Config`
   - Create `checkpop3/checker.go` with `POP3Checker`
   - Create `checkpop3/checker_test.go`
   - Add `CheckTypePOP3` to `checkerdef/types.go`
   - Register in `registry/registry.go`

2. **Backend: IMAP checker**
   - Create `checkimap/config.go` with `IMAPConfig`
   - Create `checkimap/checker.go` with `IMAPChecker`
   - Create `checkimap/checker_test.go`
   - Add `CheckTypeIMAP` to `checkerdef/types.go`
   - Register in `registry/registry.go`

3. **Dashboard: form support**
   - Add POP3/IMAP to `CheckType` union and `checkTypes` array
   - Add `tls` state variable
   - Add form validation and config fields rendering
   - Update `defaultPeriod` for new types
   - Update target display in check list

4. **Verify**: `make test && make lint && make dev-test`

---

## Competitor Reference

- **Pingdom**: POP3, POP3S, IMAP, IMAPS monitoring
- **BetterStack**: POP3/IMAP monitoring
- **Gatus**: STARTTLS support for IMAP
