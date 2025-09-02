# UDP Port Monitoring

## Overview

Add UDP port monitoring as a new check type. Verify that UDP services are reachable and optionally responding with expected data. Follow the same architecture as the TCP checker.

## Check Type

Type: `udp`

---

## Backend

### Package: `back/internal/checkers/checkudp/`

Follow the TCP checker structure with these files:

| File | Purpose |
|------|---------|
| `config.go` | `UDPConfig` struct with `FromMap()` / `GetConfig()` |
| `checker.go` | `UDPChecker` struct implementing `checkerdef.Checker` |
| `errors.go` | Sentinel errors (`ErrInvalidConfigType`) |
| `samples.go` | `GetSampleConfigs()` with DNS, NTP, SIP examples |
| `checker_test.go` | Unit tests |

### Configuration (`UDPConfig`)

```go
type UDPConfig struct {
    Host       string        `json:"host,omitempty"`
    Port       int           `json:"port,omitempty"`
    Timeout    time.Duration `json:"timeout,omitempty"`
    SendData   string        `json:"send_data,omitempty"`
    ExpectData string        `json:"expect_data,omitempty"`
}
```

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `host` | string | yes | — | Target hostname or IP |
| `port` | int | yes | — | Target UDP port (1-65535) |
| `timeout` | duration | no | 5s | Read/write timeout (max 60s) |
| `send_data` | string | no | — | Payload to send (ASCII) |
| `expect_data` | string | no | — | Expected substring in response |

### Validation Rules

- `host` is required and must be a non-empty string
- `port` is required and must be between 1 and 65535
- `timeout`, if set, must be > 0 and <= 60s
- If `expect_data` is set, `send_data` should also be set (warn if not — some protocols respond without a request)

### Execution Behavior

1. Resolve `host` to IP (prefer IPv4, fall back to IPv6)
2. Create a UDP connection via `net.DialContext` with `"udp"` network
3. If `send_data` is provided, write it to the connection
4. If `send_data` or `expect_data` is provided, read response (4KB buffer, same as TCP)
5. If `expect_data` is provided, check response contains the expected substring
6. If no `send_data`/`expect_data`, a successful dial is sufficient (best-effort — see limitations)

**Status mapping:**

| Condition | Status |
|-----------|--------|
| Successful exchange or dial | `StatusUp` |
| DNS resolution failure | `StatusError` |
| Write/read failure | `StatusDown` |
| Context timeout | `StatusTimeout` |
| `expect_data` not found in response | `StatusDown` |

### Metrics

| Key | Type | Description |
|-----|------|-------------|
| `total_time_ms` | float64 | Total check duration in ms |
| `bytes_sent` | int | Bytes sent (if `send_data` used) |
| `bytes_received` | int | Bytes received |

### Output

| Key | Type | Description |
|-----|------|-------------|
| `host` | string | Resolved IP address |
| `port` | int | Target port |
| `ip_version` | string | `"ipv4"` or `"ipv6"` |
| `received_data` | string | First 1KB of response (if any) |
| `error` | string | Error description (on failure) |

### Registration

1. Add to `back/internal/checkers/checkerdef/types.go`:
   ```go
   CheckTypeUDP CheckType = "udp"
   ```

2. Add to `back/internal/checkers/registry/registry.go`:
   - `GetChecker`: `case checkerdef.CheckTypeUDP: return &checkudp.UDPChecker{}, true`
   - `ParseConfig`: `case checkerdef.CheckTypeUDP: return &checkudp.UDPConfig{}, true`

### Sample Configs

```go
// DNS check on Google Public DNS
{Name: "Google DNS (53)", Slug: "udp-google-dns", Period: 5m,
 Config: {host: "8.8.8.8", port: 53, timeout: "5s"}}

// NTP check
{Name: "NTP Pool (123)", Slug: "udp-ntp-pool", Period: 5m,
 Config: {host: "pool.ntp.org", port: 123, timeout: "5s"}}

// SIP check
{Name: "SIP Server (5060)", Slug: "udp-sip", Period: 5m,
 Config: {host: "sip.example.com", port: 5060, timeout: "5s"}}
```

### Limitations

- **No guaranteed reachability detection**: UDP is connectionless. Without `send_data`/`expect_data`, the check can only verify that no ICMP "port unreachable" is returned. Firewalls may silently drop packets, making the port appear open.
- **Best practice**: Always use `send_data`/`expect_data` for reliable monitoring. For known protocols (DNS, NTP), use appropriate request payloads.

---

## Frontend

### Dashboard (`apps/dash0/src/components/shared/check-form.tsx`)

#### Type Registration

Add to the `CheckType` union type:
```typescript
type CheckType = "http" | "tcp" | "icmp" | "dns" | "ssl" | "heartbeat" | "domain" | "smtp" | "udp";
```

Add to the `checkTypes` array:
```typescript
{ value: "udp", label: "UDP", description: "Check UDP port reachability" },
```

#### Form Fields

The UDP form uses the **same layout as TCP**: Host + Port side by side.

```tsx
case "udp":
  return (
    <div className="space-y-2">
      <Label>Host</Label>
      <div className="flex gap-2">
        <Input
          id="host" type="text" placeholder="8.8.8.8"
          value={host} onChange={(e) => setHost(e.target.value)}
          className="flex-1" data-testid="check-host-input"
        />
        <Input
          id="port" type="number" placeholder="53"
          value={port} onChange={(e) => setPort(e.target.value)}
          className="w-24" data-testid="check-port-input"
        />
      </div>
    </div>
  );
```

#### Config Submission

Add to the `handleSubmit` switch:
```typescript
case "udp":
  if (!host || !port) {
    setError("Host and port are required");
    return;
  }
  config.host = host;
  config.port = parseInt(port, 10);
  break;
```

#### Default Period

In `defaultPeriod()`, UDP uses `"00:01:00"` (1 minute) — no change needed, it falls into the default case.

---

## E2E Tests (`apps/dash0/e2e/checks.spec.ts`)

### Test: Create a UDP check

```typescript
test("should create a new UDP check", async ({ authenticatedPage }) => {
  const page = authenticatedPage;

  // Navigate to new check form
  await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
  await page.waitForURL(/\/checks/);
  await page.waitForLoadState("networkidle");
  await page.getByTestId("new-check-button").click();
  await page.waitForURL(/\/checks\/new/);
  await page.waitForLoadState("networkidle");
  await expect(page.getByTestId("check-name-input")).toBeVisible();

  // Select UDP type
  await page.getByTestId("check-type-select").click();
  await page.getByRole("option", { name: /UDP/i }).click();

  // Fill in the form
  const checkName = `E2E UDP ${Date.now()}`;
  await page.getByTestId("check-name-input").fill(checkName);
  await page.getByTestId("check-host-input").fill("8.8.8.8");
  await page.getByTestId("check-port-input").fill("53");

  // Submit the form
  await page.getByTestId("check-submit-button").click();

  // Wait for navigation to check detail page
  await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
  await page.waitForLoadState("networkidle");

  // Verify check name is displayed
  await expect(page.getByRole("heading", { name: checkName })).toBeVisible();

  // Take screenshot
  await page.screenshot({
    path: "test-results/screenshots/checks-udp-created.png",
    fullPage: true,
  });
});
```

### Test: Validation error for empty host/port

```typescript
test("should show validation error for UDP when host or port is empty", async ({ authenticatedPage }) => {
  const page = authenticatedPage;

  await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
  await page.waitForURL(/\/checks/);
  await page.waitForLoadState("networkidle");
  await page.getByTestId("new-check-button").click();
  await page.waitForURL(/\/checks\/new/);
  await page.waitForLoadState("networkidle");
  await expect(page.getByTestId("check-name-input")).toBeVisible();

  // Select UDP type
  await page.getByTestId("check-type-select").click();
  await page.getByRole("option", { name: /UDP/i }).click();

  // Fill name only, leave host/port empty
  await page.getByTestId("check-name-input").fill("UDP Missing Config");
  await page.getByTestId("check-submit-button").click();

  await page.waitForTimeout(500);
  expect(page.url()).toContain("/checks/new");
  await expect(page.getByText(/Host and port are required/i)).toBeVisible();
});
```

---

## Key Files

| File | Action |
|------|--------|
| `back/internal/checkers/checkudp/config.go` | New |
| `back/internal/checkers/checkudp/checker.go` | New |
| `back/internal/checkers/checkudp/errors.go` | New |
| `back/internal/checkers/checkudp/samples.go` | New |
| `back/internal/checkers/checkudp/checker_test.go` | New |
| `back/internal/checkers/checkerdef/types.go` | Add `CheckTypeUDP` constant |
| `back/internal/checkers/registry/registry.go` | Register UDP in both switches |
| `apps/dash0/src/components/shared/check-form.tsx` | Add UDP type, form fields, config submission |
| `apps/dash0/e2e/checks.spec.ts` | Add 2 UDP E2E tests |

## Verification

1. **Backend tests**: `make test` — all pass including new `checkudp` tests
2. **Linting**: `make lint` — no errors
3. **Manual test**: `make dev-test`, create UDP check targeting `8.8.8.8:53` via the UI
4. **E2E tests**: `cd apps/dash0 && bun run test:e2e`
5. **API test**:
   ```bash
   TOKEN=$(cat /tmp/token.txt)
   curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
     -d '{"name":"DNS UDP","slug":"dns-udp","type":"udp","config":{"host":"8.8.8.8","port":53}}' \
     'http://localhost:4000/api/v1/orgs/default/checks' | jq '.'
   ```

**Status**: Draft | **Created**: 2026-03-22
