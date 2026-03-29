# SNMP Monitoring

## Overview

Add an SNMP health check that queries network devices and infrastructure components via Simple Network Management Protocol. SNMP is the standard for monitoring network switches, routers, printers, UPS systems, and other hardware where installing agents isn't possible. The check performs SNMP GET requests and validates the returned OID values.

**Use cases:**
- Monitor network switch/router availability and interface status
- Check UPS battery level and power status
- Monitor printer toner levels and page counts
- Verify server hardware health via IPMI/iLO SNMP
- Track device uptime (`sysUpTime`)
- Validate system identity (`sysDescr`, `sysName`)
- Alert on specific OID value thresholds (e.g., CPU > 90%, disk > 95%)

## Check Type
Type: `snmp`

---

## Backend

### Package: `server/internal/checkers/checksnmp/`

| File | Description |
|------|-------------|
| `config.go` | `SNMPConfig` struct with `FromMap()` / `GetConfig()` |
| `checker.go` | `SNMPChecker` implementing `Checker` interface |
| `errors.go` | Package-level error sentinel values |
| `samples.go` | `GetSampleConfigs()` with example configurations |
| `checker_test.go` | Table-driven tests with SNMP simulator |

### Configuration (`SNMPConfig`)

```go
type SNMPConfig struct {
    Host          string        `json:"host"`
    Port          int           `json:"port,omitempty"`
    Version       string        `json:"version,omitempty"`
    Community     string        `json:"community,omitempty"`
    OID           string        `json:"oid"`
    ExpectedValue string        `json:"expected_value,omitempty"`
    Operator      string        `json:"operator,omitempty"`
    // SNMPv3 fields
    Username      string        `json:"username,omitempty"`
    AuthProtocol  string        `json:"auth_protocol,omitempty"`
    AuthPassword  string        `json:"auth_password,omitempty"`
    PrivProtocol  string        `json:"priv_protocol,omitempty"`
    PrivPassword  string        `json:"priv_password,omitempty"`
    Timeout       time.Duration `json:"timeout,omitempty"`
}
```

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `host` | string | **yes** | — | Target device hostname or IP |
| `port` | int | no | `161` | SNMP port |
| `version` | string | no | `2c` | SNMP version: `1`, `2c`, `3` |
| `community` | string | no | `public` | Community string (v1/v2c only) |
| `oid` | string | **yes** | — | OID to query (e.g., `.1.3.6.1.2.1.1.1.0` for sysDescr) |
| `expected_value` | string | no | — | Expected value for comparison |
| `operator` | string | no | `equals` | Comparison: `equals`, `contains`, `greater_than`, `less_than`, `not_equals` |
| `username` | string | no | — | SNMPv3 username (USM) |
| `auth_protocol` | string | no | — | SNMPv3 auth: `MD5`, `SHA`, `SHA-256`, `SHA-512` |
| `auth_password` | string | no | — | SNMPv3 auth password |
| `priv_protocol` | string | no | — | SNMPv3 privacy: `DES`, `AES`, `AES-192`, `AES-256` |
| `priv_password` | string | no | — | SNMPv3 privacy password |
| `timeout` | duration | no | `10s` | SNMP request timeout |

### Validation Rules

- `host` is required
- `oid` is required and must start with `.` or be a valid OID string
- `version` must be `1`, `2c`, or `3`
- For v1/v2c: `community` must not be empty (defaults to `public`)
- For v3: `username` is required
- `auth_protocol` if set must be one of: `MD5`, `SHA`, `SHA-256`, `SHA-512`
- `priv_protocol` if set must be one of: `DES`, `AES`, `AES-192`, `AES-256`
- `priv_protocol` requires `auth_protocol` to also be set
- `operator` must be one of: `equals`, `contains`, `greater_than`, `less_than`, `not_equals`
- `timeout` must be > 0 and ≤ 60s
- Auto-generate `spec.Name` as `host (OID)` if empty
- Auto-generate `spec.Slug` as `snmp-{host}` if empty

### Execution Behavior

1. Create GoSNMP connection params based on version
2. For v1/v2c: set community string
3. For v3: configure USM security parameters (auth/priv)
4. Create context with timeout
5. Record `t0` — connect to target
6. Perform SNMP GET for the configured OID
7. Extract value from response varbind
8. If `expected_value` is set, compare using `operator`:
   - `equals`: string equality
   - `contains`: substring match
   - `greater_than`/`less_than`: numeric comparison (parse both sides as float64)
   - `not_equals`: string inequality
9. Record `t1` — compute `query_time_ms`
10. Close connection
11. Return result

**Status mapping:**

| Condition | Status |
|-----------|--------|
| OID query returns value (no expected_value set) | `StatusUp` |
| OID query returns value matching expected_value | `StatusUp` |
| OID query returns value NOT matching expected_value | `StatusDown` |
| OID not found (NoSuchInstance, NoSuchObject) | `StatusDown` |
| Device unreachable | `StatusDown` |
| Authentication failure (v3) | `StatusDown` |
| Context deadline exceeded / no response | `StatusTimeout` |
| Invalid OID / configuration | `StatusError` |

### Metrics

| Key | Type | Description |
|-----|------|-------------|
| `query_time_ms` | float64 | Time for SNMP GET request |
| `value` | float64 | Numeric value of OID (if parseable) |

### Output

| Key | Type | Description |
|-----|------|-------------|
| `host` | string | Target device |
| `oid` | string | Queried OID |
| `value` | string | Returned value (string representation) |
| `value_type` | string | SNMP type (Integer, OctetString, Counter32, etc.) |
| `expected_value` | string | Expected value (if configured) |
| `match` | bool | Whether value matched expectation |
| `error` | string | Error message if check failed |

### Go Driver

Use `github.com/gosnmp/gosnmp` (most popular Go SNMP library). Add to `server/go.mod`.

```go
import "github.com/gosnmp/gosnmp"

func newSNMPClient(cfg *SNMPConfig) *gosnmp.GoSNMP {
    client := &gosnmp.GoSNMP{
        Target:    cfg.Host,
        Port:      uint16(cfg.resolvePort()),
        Timeout:   cfg.resolveTimeout(),
        Retries:   1,
    }
    switch cfg.Version {
    case "1":
        client.Version = gosnmp.Version1
        client.Community = cfg.resolveCommunity()
    case "2c", "":
        client.Version = gosnmp.Version2c
        client.Community = cfg.resolveCommunity()
    case "3":
        client.Version = gosnmp.Version3
        client.SecurityModel = gosnmp.UserSecurityModel
        client.MsgFlags = computeMsgFlags(cfg)
        client.SecurityParameters = &gosnmp.UsmSecurityParameters{
            UserName:                 cfg.Username,
            AuthenticationProtocol:   mapAuthProtocol(cfg.AuthProtocol),
            AuthenticationPassphrase: cfg.AuthPassword,
            PrivacyProtocol:          mapPrivProtocol(cfg.PrivProtocol),
            PrivacyPassphrase:        cfg.PrivPassword,
        }
    }
    return client
}
```

### Common OIDs (for documentation / UI hints)

| OID | Name | Description |
|-----|------|-------------|
| `.1.3.6.1.2.1.1.1.0` | sysDescr | System description |
| `.1.3.6.1.2.1.1.3.0` | sysUpTime | Uptime in hundredths of a second |
| `.1.3.6.1.2.1.1.5.0` | sysName | System name |
| `.1.3.6.1.2.1.2.1.0` | ifNumber | Number of network interfaces |
| `.1.3.6.1.2.1.25.1.1.0` | hrSystemUptime | Host uptime |

### Testing

Use an SNMP simulator or testcontainers with `polinux/snmpd`:

**Test cases** (table-driven):
1. **Happy path** — query sysDescr, expect `StatusUp`
2. **Value comparison (equals)** — match expected string, expect `StatusUp`
3. **Value comparison (greater_than)** — numeric threshold, expect `StatusUp`
4. **Value mismatch** — wrong expected value, expect `StatusDown`
5. **OID not found** — non-existent OID, expect `StatusDown`
6. **Wrong community** — expect `StatusTimeout` (SNMP silently drops bad community)
7. **Device unreachable** — wrong host, expect `StatusTimeout`
8. **SNMPv3 auth** — with username/auth/priv, expect `StatusUp`
9. **Missing host** — validation error
10. **Missing OID** — validation error

### Limitations

- Only SNMP GET supported (no WALK, GETBULK, or SET)
- Single OID per check (monitor multiple OIDs with multiple checks)
- No SNMP trap receiver (active polling only)
- No MIB file parsing (use numeric OIDs)
- SNMPv3 INFORM not supported
- UDP only (no TCP transport)

---

## Frontend

### Form Fields

```tsx
case "snmp":
  return (
    <>
      <div className="space-y-2">
        <Label>Host</Label>
        <div className="flex gap-2">
          <Input id="host" type="text" placeholder="192.168.1.1"
            value={host} onChange={(e) => setHost(e.target.value)} className="flex-1" />
          <Input id="port" type="number" placeholder="161"
            value={port} onChange={(e) => setPort(e.target.value)} className="w-24" />
        </div>
      </div>
      <div className="space-y-2">
        <Label htmlFor="oid">OID</Label>
        <Input id="oid" type="text" placeholder=".1.3.6.1.2.1.1.1.0"
          value={oid} onChange={(e) => setOid(e.target.value)} />
        <p className="text-xs text-muted-foreground">
          e.g. .1.3.6.1.2.1.1.1.0 (sysDescr), .1.3.6.1.2.1.1.3.0 (sysUpTime)
        </p>
      </div>
      <div className="space-y-2">
        <Label htmlFor="community">Community String (v1/v2c)</Label>
        <Input id="community" type="text" placeholder="public"
          value={community} onChange={(e) => setCommunity(e.target.value)} />
      </div>
      <div className="flex gap-4">
        <div className="space-y-2 flex-1">
          <Label htmlFor="expectedValue">Expected Value (optional)</Label>
          <Input id="expectedValue" type="text"
            value={expectedValue} onChange={(e) => setExpectedValue(e.target.value)} />
        </div>
        <div className="space-y-2 w-40">
          <Label htmlFor="operator">Operator</Label>
          <Select value={operator || "equals"} onValueChange={setOperator}>
            <SelectTrigger><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="equals">Equals</SelectItem>
              <SelectItem value="contains">Contains</SelectItem>
              <SelectItem value="greater_than">Greater Than</SelectItem>
              <SelectItem value="less_than">Less Than</SelectItem>
              <SelectItem value="not_equals">Not Equals</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </div>
    </>
  );
```

---

## Key Files

| File | Action |
|------|--------|
| `server/internal/checkers/checksnmp/config.go` | New |
| `server/internal/checkers/checksnmp/checker.go` | New |
| `server/internal/checkers/checksnmp/errors.go` | New |
| `server/internal/checkers/checksnmp/samples.go` | New |
| `server/internal/checkers/checksnmp/checker_test.go` | New |
| `server/internal/checkers/checkerdef/types.go` | Modify |
| `server/internal/checkers/registry/registry.go` | Modify |
| `server/go.mod` | Modify (add `github.com/gosnmp/gosnmp`) |
| `web/dash0/src/components/shared/check-form.tsx` | Modify |
| `web/dash0/src/locales/en/checks.json` | Modify |
| `web/dash0/src/locales/fr/checks.json` | Modify |

## Verification

- [ ] `make lint` passes
- [ ] `make test` passes
- [ ] Create an SNMP check via the UI against a test device/simulator
- [ ] Verify OID query returns value and shows `StatusUp`
- [ ] Verify expected value comparison works (all operators)
- [ ] Verify non-existent OID shows `StatusDown`
- [ ] Verify SNMPv3 with auth/priv works
- [ ] E2E tests pass

**Status**: Draft | **Created**: 2026-03-28
