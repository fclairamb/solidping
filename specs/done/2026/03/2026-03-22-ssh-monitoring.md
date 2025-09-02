# SSH Monitoring

## Overview

Add SSH server monitoring as a new check type. Verifies SSH services are reachable and responding with a valid protocol banner. Optionally authenticates and executes a command to validate the server is functioning correctly (e.g., `uptime`, `df -h`, a custom health script). Supported by StatusCake and Gatus.

## Goals

1. Verify SSH servers are reachable and responding with a valid banner
2. Optionally verify host key fingerprint for security monitoring
3. Optionally authenticate and execute a command, checking exit code and output
4. Measure connection and command execution time

---

## Check Type

Type: `ssh`

### Settings

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `host` | string | yes | — | Target hostname or IP |
| `port` | int | no | 22 | Target SSH port |
| `timeout` | duration | no | 10s | Connection timeout |
| `expected_fingerprint` | string | no | — | Expected host key fingerprint (SHA256 format, e.g., `SHA256:uNiV...`) |
| `username` | string | no | — | SSH username (required for command execution) |
| `password` | string | no | — | SSH password (mutually exclusive with `private_key`) |
| `private_key` | string | no | — | PEM-encoded private key (mutually exclusive with `password`) |
| `command` | string | no | — | Command to execute after authentication |
| `expected_exit_code` | int | no | 0 | Expected command exit code |
| `expected_output` | string | no | — | Expected substring in stdout |
| `expected_output_pattern` | string | no | — | Regex pattern to match against stdout |

No database migration needed — `config` is stored as JSONB in the `checks` table.

### Execution Modes

**Mode 1 — Banner-only** (no `username` set):
1. Open TCP connection to `host:port`
2. Read the SSH protocol banner (e.g., `SSH-2.0-OpenSSH_9.6`)
3. Verify it starts with `SSH-`
4. If `expected_fingerprint` is set, perform key exchange (accept any key) and compare fingerprint
5. Close connection

**Mode 2 — Command execution** (`username` + `command` set):
1. Establish SSH connection with authentication (`password` or `private_key`)
2. Read and store the banner
3. If `expected_fingerprint` is set, verify host key fingerprint during handshake
4. Open a session and execute `command`
5. Capture stdout, stderr, and exit code
6. Check exit code matches `expected_exit_code`
7. If `expected_output` is set, verify stdout contains the substring
8. If `expected_output_pattern` is set, verify stdout matches the regex
9. Close session and connection

### Success Criteria (StatusUp)
- Banner received and starts with `SSH-`
- Fingerprint matches (if `expected_fingerprint` is set)
- Command exits with `expected_exit_code` (if `command` is set)
- Output contains `expected_output` (if set)
- Output matches `expected_output_pattern` (if set)

### Failure Criteria (StatusDown)
- Connection refused or reset
- Banner does not start with `SSH-`
- Fingerprint mismatch
- Authentication failed
- Command exited with unexpected exit code
- Output does not contain expected substring or match pattern

### Timeout (StatusTimeout)
- TCP connection or SSH handshake exceeds timeout
- Command execution exceeds timeout

### Error (StatusError)
- DNS resolution failure
- Invalid configuration (e.g., malformed private key)
- Network unreachable

---

## Go Types

### Config (`back/internal/checkers/checkssh/config.go`)

```go
type SSHConfig struct {
    Host                  string        `json:"host"`
    Port                  int           `json:"port,omitempty"`
    Timeout               time.Duration `json:"timeout,omitempty"`
    ExpectedFingerprint   string        `json:"expected_fingerprint,omitempty"`
    Username              string        `json:"username,omitempty"`
    Password              string        `json:"password,omitempty"`
    PrivateKey            string        `json:"private_key,omitempty"`
    Command               string        `json:"command,omitempty"`
    ExpectedExitCode      int           `json:"expected_exit_code,omitempty"`
    ExpectedOutput        string        `json:"expected_output,omitempty"`
    ExpectedOutputPattern string        `json:"expected_output_pattern,omitempty"`
}
```

`FromMap` must handle type coercion for `port` (int vs float64 from JSON) and `timeout` (duration string). `GetConfig` omits zero-value/default fields.

### Validation

Add to `SSHConfig.Validate()`:
- `host` must not be empty
- `port` must be 1–65535 (if set)
- `timeout` must be > 0 and <= 60s (if set)
- `command` requires `username` to be set
- `expected_exit_code` requires `command` to be set
- `expected_output` requires `command` to be set
- `expected_output_pattern` requires `command` to be set and must compile as valid regex
- `password` and `private_key` are mutually exclusive
- If `username` is set, exactly one of `password` or `private_key` must be provided
- If `private_key` is set, it must parse as a valid PEM key

---

## Backend Implementation

### Files to create

| File | Purpose |
|------|---------|
| `back/internal/checkers/checkssh/config.go` | `SSHConfig` struct with `FromMap`, `GetConfig`, `Validate` |
| `back/internal/checkers/checkssh/checker.go` | `SSHChecker` with `Type`, `Validate`, `Execute` |
| `back/internal/checkers/checkssh/checker_test.go` | Table-driven tests |
| `back/internal/checkers/checkssh/errors.go` | Custom error types |
| `back/internal/checkers/checkssh/samples.go` | Sample configurations |

### Files to modify

| File | Change |
|------|--------|
| `back/internal/checkers/checkerdef/types.go` | Add `CheckTypeSSH CheckType = "ssh"` |
| `back/internal/checkers/registry/registry.go` | Add `checkssh` cases to `GetChecker()` and `ParseConfig()` |

### SSH library

Use `golang.org/x/crypto/ssh` for:
- SSH client dial with password or public key auth
- Host key callback for fingerprint verification
- Session creation and command execution

For banner-only mode, use raw TCP connect + read (no SSH library needed).

### Execution flow (`checker.go`)

```go
func (c *SSHChecker) Execute(ctx context.Context, config checkerdef.Config) (*checkerdef.Result, error) {
    cfg := config.(*SSHConfig)
    params := newExecParams(cfg) // apply defaults
    ctx, cancel := context.WithTimeout(ctx, params.timeout)
    defer cancel()

    start := time.Now()
    metrics := map[string]any{}
    output := map[string]any{}

    // Resolve host
    targetIP, err := resolveHost(ctx, params.host)
    // ... error handling ...

    output["host"] = targetIP.String()
    output["port"] = params.port

    if cfg.Username == "" {
        // Banner-only mode
        return c.executeBannerOnly(ctx, targetIP, params, cfg, start, metrics, output)
    }

    // Command execution mode
    return c.executeWithAuth(ctx, targetIP, params, cfg, start, metrics, output)
}
```

**Banner-only mode**:
1. `net.DialContext` to `host:port`
2. Read until `\n` for banner
3. Validate banner starts with `SSH-`
4. If `expected_fingerprint` set, do SSH handshake with `ssh.Dial` using `ssh.InsecureIgnoreHostKey` to capture key, then compare
5. Return result

**Command execution mode**:
1. Build `ssh.ClientConfig` with `username` + auth method (`ssh.Password` or `ssh.PublicKeys`)
2. Set `HostKeyCallback`:
   - If `expected_fingerprint` set: verify and reject on mismatch
   - Otherwise: `ssh.InsecureIgnoreHostKey()`
3. `ssh.Dial("tcp", target, clientConfig)`
4. Record banner from client connection
5. `client.NewSession()`
6. `session.CombinedOutput(command)` or separate stdout/stderr capture
7. Check exit code, stdout content, patterns
8. Return result

### Metrics

Returned in `Result.Metrics`:
- `connection_time_ms` (float64) — TCP connection duration
- `auth_time_ms` (float64) — SSH authentication duration (command mode only)
- `command_time_ms` (float64) — Command execution duration (command mode only)
- `total_time_ms` (float64) — Total check duration

### Output

Returned in `Result.Output`:
- `host` (string) — Resolved IP address
- `port` (int) — Port connected to
- `banner` (string) — SSH version banner (e.g., `SSH-2.0-OpenSSH_9.6`)
- `fingerprint` (string) — Host key fingerprint in SHA256 format (if key exchange performed)
- `exit_code` (int) — Command exit code (command mode only)
- `stdout` (string) — First 4KB of stdout (command mode only)
- `stderr` (string) — First 4KB of stderr (command mode only)
- `error` (string) — Error message if check failed

---

## Dashboard UI

### Check Form (`check-form.tsx`)

Add `case "ssh"` to `renderConfigFields()`:

**Always shown:**
- **Host** — `Input` (required, text, placeholder: `server.example.com`)
- **Port** — `Input` (number, placeholder: `22`)
- **Expected Fingerprint** — `Input` (text, placeholder: `SHA256:...`, hint: "Leave empty to skip fingerprint verification")

**Authentication section** (collapsible `Card` labeled "Authentication & Command"):
- **Username** — `Input` (text, placeholder: `monitoring`)
- **Auth Method** — `Select` with options: `password`, `private_key`
  - If `password`: **Password** — `Input` (type=password)
  - If `private_key`: **Private Key** — `Textarea` (monospace, placeholder: `-----BEGIN OPENSSH PRIVATE KEY-----`)
- **Command** — `Input` (text, placeholder: `uptime`, hint: "Command to execute after authentication")
- **Expected Exit Code** — `Input` (number, default: `0`)
- **Expected Output** — `Input` (text, placeholder: "substring to match in stdout")
- **Expected Output Pattern** — `Input` (text, placeholder: `^\\d+:\\d+`, hint: "Regex pattern to match against stdout")

The authentication section fields should be hidden when `username` is empty (progressive disclosure — show host/port/fingerprint first, reveal auth fields only when username is entered).

### Check Detail Page (`checks.$checkUid.index.tsx`)

No changes needed — the existing generic key-value display for `lastResult.metrics` and `lastResult.output` will show:
- Banner, fingerprint, exit code, stdout, stderr in the output section
- Connection time, auth time, command time in the metrics section

For better readability, the `stdout` and `stderr` fields will naturally display as text in the key-value pairs. If the output is multiline, it will wrap in the existing layout.

### Check List Target Display

Update the target display logic in `CheckRow` to handle SSH:

```typescript
// Add to target display logic
check.type === "ssh"
  ? `${check.config?.host}:${check.config?.port || 22}`
  : // ... existing logic
```

### Translations

Add to `apps/dash0/src/locales/en/checks.json`:

```json
{
  "sshHost": "Host",
  "sshPort": "Port",
  "sshExpectedFingerprint": "Expected Fingerprint",
  "sshUsername": "Username",
  "sshPassword": "Password",
  "sshPrivateKey": "Private Key",
  "sshCommand": "Command",
  "sshExpectedExitCode": "Expected Exit Code",
  "sshExpectedOutput": "Expected Output",
  "sshExpectedOutputPattern": "Expected Output Pattern",
  "sshAuthSection": "Authentication & Command"
}
```

Add French translations to `fr/checks.json`.

---

## Tests

### Config Tests (`config_test.go`)

Table-driven tests:

| Test case | Description |
|-----------|-------------|
| Minimal valid config | `host` only → defaults applied |
| All fields populated | Every field set → all parsed correctly |
| Port type coercion | `port` as float64 (JSON) → parsed as int |
| Timeout parsing | `"5s"` string → `5 * time.Second` |
| Missing host | → `ConfigError{Parameter: "host"}` |
| Port out of range | 99999 → validation error |
| Command without username | → validation error |
| Both password and private_key | → validation error |
| Username without auth | → validation error |
| Invalid regex pattern | → validation error |
| Invalid private key PEM | → validation error |
| Expected output without command | → validation error |

### Checker Tests (`checker_test.go`)

Integration tests with testcontainers (use `linuxserver/openssh-server` image):

| Test case | Description |
|-----------|-------------|
| Banner check | Connect, read banner, verify starts with `SSH-` |
| Fingerprint match | Verify correct fingerprint → StatusUp |
| Fingerprint mismatch | Wrong fingerprint → StatusDown |
| Command success | Run `echo hello` → exit 0, stdout contains "hello" |
| Command failure | Run `exit 1` → exit 1 → StatusDown (expected 0) |
| Expected output match | stdout contains substring → StatusUp |
| Expected output mismatch | stdout missing substring → StatusDown |
| Output pattern match | stdout matches regex → StatusUp |
| Auth failure | Wrong password → StatusDown |
| Connection refused | Invalid port → StatusDown |
| Timeout | Unreachable host → StatusTimeout |

---

## Sample Configurations (`samples.go`)

```go
func (c *SSHChecker) GetSampleConfigs(_ *ListSampleOptions) []CheckSpec {
    return []CheckSpec{
        {
            Name:   "GitHub SSH",
            Slug:   "ssh-github",
            Period: 5 * time.Minute,
            Config: (&SSHConfig{
                Host: "github.com",
                Port: 22,
            }).GetConfig(),
        },
        {
            Name:   "Server Health Check",
            Slug:   "ssh-server-health",
            Period: time.Minute,
            Config: (&SSHConfig{
                Host:             "server.example.com",
                Port:             22,
                Username:         "monitoring",
                Password:         "changeme",
                Command:          "uptime",
                ExpectedExitCode: 0,
            }).GetConfig(),
        },
        {
            Name:   "Disk Space Check",
            Slug:   "ssh-disk-space",
            Period: 5 * time.Minute,
            Config: (&SSHConfig{
                Host:                  "server.example.com",
                Port:                  22,
                Username:              "monitoring",
                Password:              "changeme",
                Command:               "df -h / | tail -1",
                ExpectedExitCode:      0,
                ExpectedOutputPattern: `\d+%`,
            }).GetConfig(),
        },
    }
}
```

---

## JSON Examples

### Banner-only check

```json
{
  "type": "ssh",
  "config": {
    "host": "server.example.com",
    "port": 22
  }
}
```

### Banner + fingerprint verification

```json
{
  "type": "ssh",
  "config": {
    "host": "server.example.com",
    "expected_fingerprint": "SHA256:uNiVztksCsDhcc0u9e8BujQXVUpKZIDTMczCvj3tD2s"
  }
}
```

### Command execution with password

```json
{
  "type": "ssh",
  "config": {
    "host": "server.example.com",
    "username": "monitoring",
    "password": "s3cret",
    "command": "systemctl is-active nginx",
    "expected_exit_code": 0,
    "expected_output": "active"
  }
}
```

### Command execution with private key and output pattern

```json
{
  "type": "ssh",
  "config": {
    "host": "server.example.com",
    "username": "deploy",
    "private_key": "-----BEGIN OPENSSH PRIVATE KEY-----\n...\n-----END OPENSSH PRIVATE KEY-----",
    "command": "curl -s http://localhost:8080/health",
    "expected_exit_code": 0,
    "expected_output_pattern": "\"status\":\\s*\"ok\""
  }
}
```

---

## Security Considerations

- `password` and `private_key` are stored in the check's JSONB `config` column, which is already access-controlled per organization
- Private keys should be treated as secrets — the API should redact them in GET responses (return `"***"` instead of the actual value)
- Command execution is limited by the SSH user's permissions on the remote server
- Output is truncated to 4KB to prevent excessive storage from large command outputs
- Timeout applies to the entire check including command execution to prevent hanging

---

## Competitor Reference

- **StatusCake**: SSH protocol monitoring (banner check)
- **Gatus**: SSH endpoint type with STARTTLS support
- **Neither** offers SSH command execution — this is a differentiator for SolidPing
