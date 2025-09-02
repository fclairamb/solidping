# FTP / SFTP Monitoring

## Overview

Add FTP and SFTP health checks as two new check types. Verifies that FTP/SFTP servers are reachable, authenticate correctly, and can perform basic file operations. Beyond simple TCP port checks, this validates the server is actually functional — accepting logins, listing directories, and optionally verifying that a specific file exists or can be downloaded.

**Use cases:**
- Verify FTP/SFTP servers are accepting connections and authenticating
- Monitor anonymous FTP access availability
- Verify a specific file or directory exists (e.g., a daily export, a drop zone)
- Measure connection, authentication, and transfer latency over time
- Validate TLS/SSL connectivity (FTPS explicit via STARTTLS, FTPS implicit, SFTP over SSH)

## Check Types

Two separate check types:
- **`ftp`** — Plain FTP (RFC 959) with optional TLS (FTPS explicit via STARTTLS or FTPS implicit)
- **`sftp`** — SFTP over SSH (SSH File Transfer Protocol)

These are distinct protocols with different libraries, ports, and security models, so they are separate check types rather than modes of a single checker.

---

## FTP Checker

### Type: `ftp`

### Settings

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `host` | string | **yes** | — | FTP server hostname or IP |
| `port` | int | no | `21` | FTP server port (21 for plain/explicit TLS, 990 for implicit TLS) |
| `timeout` | duration | no | `10s` | Connection + operation timeout |
| `username` | string | no | `anonymous` | FTP username |
| `password` | string | no | — | FTP password (empty string for anonymous) |
| `tls_mode` | string | no | `none` | TLS mode: `none`, `explicit` (STARTTLS), `implicit` (port 990) |
| `tls_verify` | bool | no | `false` | Verify TLS certificate validity |
| `passive_mode` | bool | no | `true` | Use passive mode for data connections |
| `path` | string | no | — | File or directory path to verify exists |
| `expected_content` | string | no | — | Expected substring in downloaded file content (requires `path` pointing to a file, max 64KB read) |

No database migration needed — `config` is stored as JSONB in the `checks` table.

### Execution Flow

1. Parse and apply defaults (port 21, timeout 10s, username `anonymous`, passive mode on)
2. If `tls_mode` is `implicit`: dial TLS directly to `host:port`
3. Otherwise: dial plain TCP to `host:port`
4. Read server greeting (banner)
5. Record `t1` — compute `connection_time_ms`
6. If `tls_mode` is `explicit`: send `AUTH TLS` command, upgrade to TLS
7. Send `USER` + `PASS` commands to authenticate
8. Record `t2` — compute `auth_time_ms`
9. If `path` is set:
   a. Attempt `STAT` or `LIST` on the path to verify existence
   b. If `expected_content` is set, `RETR` the file and read first 64KB, check for substring
   c. Record `t3` — compute `operation_time_ms`
10. Send `QUIT`
11. Return result with metrics and output

### Validation Rules

- `host` is required and must be non-empty
- `port` must be between 1 and 65535
- `timeout` must be > 0 and <= 60s
- `tls_mode` must be one of: `none`, `explicit`, `implicit`, or empty (defaults to `none`)
- When `tls_mode` is `implicit` and port is not set, default port to `990`
- `expected_content` requires `path` to be set
- Auto-generate `spec.Name` as `ftp://host:port` if empty
- Auto-generate `spec.Slug` from host if empty

### Success Criteria (StatusUp)
- Server greeting received
- Authentication successful
- Path exists (if `path` is set)
- File content contains expected substring (if `expected_content` is set)

### Failure Criteria (StatusDown)
- Connection refused or reset
- Invalid or missing greeting
- Authentication failed (530 response)
- Path does not exist (550 response)
- File content does not contain expected substring
- TLS handshake failure

### Timeout (StatusTimeout)
- TCP connection exceeds timeout
- FTP command response exceeds timeout

### Error (StatusError)
- DNS resolution failure
- Invalid configuration
- Network unreachable

### Metrics

Returned in `Result.Metrics`:
- `connection_time_ms` (float64) — TCP connection + greeting duration
- `auth_time_ms` (float64) — Authentication duration
- `tls_time_ms` (float64) — TLS handshake duration (if TLS enabled)
- `operation_time_ms` (float64) — File/directory check duration (if `path` set)
- `total_time_ms` (float64) — Total check duration

### Output

Returned in `Result.Output`:
- `host` (string) — Resolved IP address
- `port` (int) — Port connected to
- `banner` (string) — Server greeting (e.g., `220 ProFTPD Server ready`)
- `tls_version` (string) — TLS version if TLS enabled (e.g., `TLS 1.3`)
- `tls_cipher` (string) — TLS cipher suite if TLS enabled
- `authenticated` (bool) — Whether authentication succeeded
- `path_exists` (bool) — Whether the path was found (if `path` set)
- `content_match` (bool) — Whether content matched (if `expected_content` set)
- `error` (string) — Error message if check failed

---

## SFTP Checker

### Type: `sftp`

### Settings

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `host` | string | **yes** | — | SFTP server hostname or IP |
| `port` | int | no | `22` | SSH port |
| `timeout` | duration | no | `10s` | Connection + operation timeout |
| `username` | string | **yes** | — | SSH username |
| `password` | string | no | — | SSH password (mutually exclusive with `private_key`) |
| `private_key` | string | no | — | PEM-encoded private key (mutually exclusive with `password`) |
| `expected_fingerprint` | string | no | — | Expected host key fingerprint (SHA256 format) |
| `path` | string | no | — | File or directory path to verify exists |
| `expected_content` | string | no | — | Expected substring in downloaded file content (requires `path` pointing to a file, max 64KB read) |

### Execution Flow

1. Parse and apply defaults (port 22, timeout 10s)
2. Build `ssh.ClientConfig` with username + auth method (`ssh.Password` or `ssh.PublicKeys`)
3. Set `HostKeyCallback`:
   - If `expected_fingerprint` set: verify and reject on mismatch
   - Otherwise: `ssh.InsecureIgnoreHostKey()`
4. `ssh.Dial("tcp", host:port, clientConfig)`
5. Record `t1` — compute `connection_time_ms`
6. Open SFTP subsystem via `sftp.NewClient(sshClient)`
7. Record `t2` — compute `auth_time_ms`
8. If `path` is set:
   a. `sftp.Stat(path)` to verify existence and get file info
   b. If `expected_content` is set and path is a file, `sftp.Open(path)` and read first 64KB, check for substring
   c. Record `t3` — compute `operation_time_ms`
9. Close SFTP client and SSH connection
10. Return result with metrics and output

### Validation Rules

- `host` is required and must be non-empty
- `port` must be between 1 and 65535
- `timeout` must be > 0 and <= 60s
- `username` is required and must be non-empty
- Exactly one of `password` or `private_key` must be provided
- `password` and `private_key` are mutually exclusive
- If `private_key` is set, it must parse as a valid PEM key
- `expected_content` requires `path` to be set
- Auto-generate `spec.Name` as `sftp://username@host:port` if empty
- Auto-generate `spec.Slug` from host if empty

### Success Criteria (StatusUp)
- SSH connection and authentication successful
- SFTP subsystem opened
- Fingerprint matches (if `expected_fingerprint` set)
- Path exists (if `path` set)
- File content contains expected substring (if `expected_content` set)

### Failure Criteria (StatusDown)
- Connection refused or reset
- SSH authentication failed
- Fingerprint mismatch
- SFTP subsystem unavailable
- Path does not exist
- File content does not match

### Timeout (StatusTimeout)
- TCP connection or SSH handshake exceeds timeout
- SFTP operation exceeds timeout

### Error (StatusError)
- DNS resolution failure
- Invalid configuration (e.g., malformed private key)
- Network unreachable

### Metrics

Returned in `Result.Metrics`:
- `connection_time_ms` (float64) — SSH connection + authentication duration
- `sftp_time_ms` (float64) — SFTP subsystem initialization duration
- `operation_time_ms` (float64) — File/directory check duration (if `path` set)
- `total_time_ms` (float64) — Total check duration

### Output

Returned in `Result.Output`:
- `host` (string) — Resolved IP address
- `port` (int) — Port connected to
- `banner` (string) — SSH version banner
- `fingerprint` (string) — Host key fingerprint in SHA256 format
- `authenticated` (bool) — Whether authentication succeeded
- `path_exists` (bool) — Whether the path was found (if `path` set)
- `file_size` (int64) — File size in bytes (if `path` set and is a file)
- `content_match` (bool) — Whether content matched (if `expected_content` set)
- `error` (string) — Error message if check failed

---

## Go Types

### FTP Config (`back/internal/checkers/checkftp/config.go`)

```go
type FTPConfig struct {
    Host            string        `json:"host"`
    Port            int           `json:"port,omitempty"`
    Timeout         time.Duration `json:"timeout,omitempty"`
    Username        string        `json:"username,omitempty"`
    Password        string        `json:"password,omitempty"`
    TLSMode         string        `json:"tls_mode,omitempty"`
    TLSVerify       bool          `json:"tls_verify,omitempty"`
    PassiveMode     bool          `json:"passive_mode,omitempty"`
    Path            string        `json:"path,omitempty"`
    ExpectedContent string        `json:"expected_content,omitempty"`
}
```

### SFTP Config (`back/internal/checkers/checksftp/config.go`)

```go
type SFTPConfig struct {
    Host                string        `json:"host"`
    Port                int           `json:"port,omitempty"`
    Timeout             time.Duration `json:"timeout,omitempty"`
    Username            string        `json:"username"`
    Password            string        `json:"password,omitempty"`
    PrivateKey          string        `json:"private_key,omitempty"`
    ExpectedFingerprint string        `json:"expected_fingerprint,omitempty"`
    Path                string        `json:"path,omitempty"`
    ExpectedContent     string        `json:"expected_content,omitempty"`
}
```

`FromMap` must handle type coercion for `port` (int vs float64 from JSON) and `timeout` (duration string). `GetConfig` omits zero-value/default fields.

---

## Backend Implementation

### Files to create

| File | Purpose |
|------|---------|
| `back/internal/checkers/checkftp/config.go` | `FTPConfig` struct with `FromMap`, `GetConfig`, `Validate` |
| `back/internal/checkers/checkftp/checker.go` | `FTPChecker` with `Type`, `Validate`, `Execute` |
| `back/internal/checkers/checkftp/checker_test.go` | Table-driven tests with testcontainers |
| `back/internal/checkers/checkftp/errors.go` | Custom error types |
| `back/internal/checkers/checkftp/samples.go` | Sample configurations |
| `back/internal/checkers/checksftp/config.go` | `SFTPConfig` struct with `FromMap`, `GetConfig`, `Validate` |
| `back/internal/checkers/checksftp/checker.go` | `SFTPChecker` with `Type`, `Validate`, `Execute` |
| `back/internal/checkers/checksftp/checker_test.go` | Table-driven tests with testcontainers |
| `back/internal/checkers/checksftp/errors.go` | Custom error types |
| `back/internal/checkers/checksftp/samples.go` | Sample configurations |

### Files to modify

| File | Change |
|------|--------|
| `back/internal/checkers/checkerdef/types.go` | Add `CheckTypeFTP CheckType = "ftp"` and `CheckTypeSFTP CheckType = "sftp"` |
| `back/internal/checkers/registry/registry.go` | Add `checkftp` and `checksftp` cases to `GetChecker()` and `ParseConfig()` |

### Go Libraries

**FTP**: Use `github.com/jlaffaye/ftp` — mature, well-maintained Go FTP client library supporting:
- Plain FTP and explicit TLS (STARTTLS)
- Implicit TLS
- Passive and active mode
- Directory listing and file retrieval

**SFTP**: Use `github.com/pkg/sftp` + `golang.org/x/crypto/ssh` — standard Go SFTP client:
- SSH authentication (password, public key)
- Host key verification
- File stat, read, and directory listing

---

## Dashboard UI

### Check Form (`check-form.tsx`)

#### Type Registration

Add to `CheckType` union:
```typescript
type CheckType = ... | "ftp" | "sftp";
```

Add to `checkTypes` array:
```typescript
{ value: "ftp", label: "FTP", description: "Check FTP server availability" },
{ value: "sftp", label: "SFTP", description: "Check SFTP server availability" },
```

Add to `defaultPeriod`: both use `"01:00:00"`.

#### New State Variables

```typescript
const [tlsMode, setTlsMode] = useState(
  getConfigField(initialData?.config, "tls_mode") || "none"
);
const [tlsVerify, setTlsVerify] = useState(
  getConfigField(initialData?.config, "tls_verify") === "true"
);
const [path, setPath] = useState(
  getConfigField(initialData?.config, "path")
);
const [expectedContent, setExpectedContent] = useState(
  getConfigField(initialData?.config, "expected_content")
);
const [expectedFingerprint, setExpectedFingerprint] = useState(
  getConfigField(initialData?.config, "expected_fingerprint")
);
```

Note: `host`, `port`, `username`, `password` state variables already exist in the form.

#### FTP Config Fields (`case "ftp"`)

```
┌─────────────────────────────────────────┐
│ Host                                    │
│ ┌──────────────────────────┐ ┌────────┐ │
│ │ ftp.example.com          │ │ 21     │ │
│ └──────────────────────────┘ └────────┘ │
│                                         │
│ Username (optional)                     │
│ ┌─────────────────────────────────────┐ │
│ │ anonymous                           │ │
│ └─────────────────────────────────────┘ │
│                                         │
│ Password (optional)                     │
│ ┌─────────────────────────────────────┐ │
│ │ ••••••••                            │ │
│ └─────────────────────────────────────┘ │
│                                         │
│ TLS Mode                                │
│ ┌─────────────────────────────────────┐ │
│ │ None                            [▼] │ │
│ └─────────────────────────────────────┘ │
│                                         │
│ ☐ Verify TLS certificate               │
│                                         │
│ Path (optional)                         │
│ ┌─────────────────────────────────────┐ │
│ │ /uploads/daily-export.csv           │ │
│ └─────────────────────────────────────┘ │
│ Verify this file or directory exists    │
│                                         │
│ Expected Content (optional)             │
│ ┌─────────────────────────────────────┐ │
│ │ SUCCESS                             │ │
│ └─────────────────────────────────────┘ │
│ Substring to match in file content      │
└─────────────────────────────────────────┘
```

#### SFTP Config Fields (`case "sftp"`)

```
┌─────────────────────────────────────────┐
│ Host                                    │
│ ┌──────────────────────────┐ ┌────────┐ │
│ │ sftp.example.com         │ │ 22     │ │
│ └──────────────────────────┘ └────────┘ │
│                                         │
│ Username                                │
│ ┌─────────────────────────────────────┐ │
│ │ deploy                              │ │
│ └─────────────────────────────────────┘ │
│                                         │
│ Password                                │
│ ┌─────────────────────────────────────┐ │
│ │ ••••••••                            │ │
│ └─────────────────────────────────────┘ │
│                                         │
│ Expected Fingerprint (optional)         │
│ ┌─────────────────────────────────────┐ │
│ │ SHA256:...                          │ │
│ └─────────────────────────────────────┘ │
│ Leave empty to skip fingerprint check   │
│                                         │
│ Path (optional)                         │
│ ┌─────────────────────────────────────┐ │
│ │ /data/export.csv                    │ │
│ └─────────────────────────────────────┘ │
│ Verify this file or directory exists    │
│                                         │
│ Expected Content (optional)             │
│ ┌─────────────────────────────────────┐ │
│ │ SUCCESS                             │ │
│ └─────────────────────────────────────┘ │
│ Substring to match in file content      │
└─────────────────────────────────────────┘
```

#### Submit Handler

Add to the `handleSubmit` switch:

```typescript
case "ftp":
  if (!host) { setError("Host is required"); return; }
  config.host = host;
  if (port) config.port = parseInt(port, 10);
  if (username && username !== "anonymous") config.username = username;
  if (password) config.password = password;
  if (tlsMode && tlsMode !== "none") config.tls_mode = tlsMode;
  if (tlsVerify) config.tls_verify = true;
  if (path) config.path = path;
  if (expectedContent) config.expected_content = expectedContent;
  break;
case "sftp":
  if (!host) { setError("Host is required"); return; }
  if (!username) { setError("Username is required"); return; }
  config.host = host;
  if (port) config.port = parseInt(port, 10);
  config.username = username;
  if (password) config.password = password;
  if (expectedFingerprint) config.expected_fingerprint = expectedFingerprint;
  if (path) config.path = path;
  if (expectedContent) config.expected_content = expectedContent;
  break;
```

### Check List Target Display

Update the target display logic in `CheckRow`:

```typescript
check.type === "ftp"
  ? `ftp://${check.config?.host}:${check.config?.port || 21}`
  : check.type === "sftp"
    ? `sftp://${check.config?.host}:${check.config?.port || 22}`
    : // ... existing logic
```

---

## Tests

### FTP Config Tests (`checkftp/config_test.go`)

| Test case | Description |
|-----------|-------------|
| Minimal valid config | `host` only → defaults applied (port 21, anonymous, passive) |
| All fields populated | Every field set → all parsed correctly |
| Port type coercion | `port` as float64 (JSON) → parsed as int |
| Timeout parsing | `"5s"` string → `5 * time.Second` |
| Missing host | → `ConfigError{Parameter: "host"}` |
| Port out of range | 99999 → validation error |
| Invalid TLS mode | `"invalid"` → validation error |
| Implicit TLS default port | `tls_mode: "implicit"` with no port → port defaults to 990 |
| Expected content without path | → validation error |

### FTP Checker Tests (`checkftp/checker_test.go`)

Integration tests with testcontainers (use `fauria/vsftpd` or `stilliard/pure-ftpd` image):

| Test case | Description |
|-----------|-------------|
| Anonymous login | Connect anonymously → StatusUp |
| Authenticated login | Username/password → StatusUp |
| Auth failure | Wrong password → StatusDown |
| Path exists | Verify existing file → StatusUp |
| Path not found | Non-existent path → StatusDown |
| Content match | File content contains substring → StatusUp |
| Content mismatch | File content missing substring → StatusDown |
| Connection refused | Invalid port → StatusDown |
| Timeout | Unreachable host → StatusTimeout |
| Explicit TLS | STARTTLS upgrade → StatusUp |

### SFTP Config Tests (`checksftp/config_test.go`)

| Test case | Description |
|-----------|-------------|
| Minimal valid config | `host` + `username` + `password` → defaults applied |
| All fields populated | Every field set → all parsed correctly |
| Missing host | → `ConfigError{Parameter: "host"}` |
| Missing username | → `ConfigError{Parameter: "username"}` |
| Both password and private_key | → validation error |
| Neither password nor private_key | → validation error |
| Invalid private key PEM | → validation error |
| Expected content without path | → validation error |

### SFTP Checker Tests (`checksftp/checker_test.go`)

Integration tests with testcontainers (use `atmoz/sftp` image):

| Test case | Description |
|-----------|-------------|
| Password auth | Username/password → StatusUp |
| Key auth | Private key → StatusUp |
| Auth failure | Wrong password → StatusDown |
| Fingerprint match | Correct fingerprint → StatusUp |
| Fingerprint mismatch | Wrong fingerprint → StatusDown |
| Path exists | Verify existing file → StatusUp |
| Path not found | Non-existent path → StatusDown |
| Content match | File content contains substring → StatusUp |
| Content mismatch | File content missing substring → StatusDown |
| Connection refused | Invalid port → StatusDown |
| Timeout | Unreachable host → StatusTimeout |

---

## Sample Configurations

### FTP Samples (`checkftp/samples.go`)

```go
func (c *FTPChecker) GetSampleConfigs(_ *ListSampleOptions) []CheckSpec {
    return []CheckSpec{
        {
            Name:   "FTP Server",
            Slug:   "ftp-server",
            Period: 5 * time.Minute,
            Config: (&FTPConfig{
                Host: "ftp.example.com",
            }).GetConfig(),
        },
        {
            Name:   "FTP with Auth",
            Slug:   "ftp-auth",
            Period: 5 * time.Minute,
            Config: (&FTPConfig{
                Host:     "ftp.example.com",
                Username: "uploader",
                Password: "changeme",
                Path:     "/uploads/",
            }).GetConfig(),
        },
        {
            Name:   "FTPS Explicit",
            Slug:   "ftps-explicit",
            Period: 5 * time.Minute,
            Config: (&FTPConfig{
                Host:      "ftp.example.com",
                TLSMode:   "explicit",
                TLSVerify: true,
                Username:  "secure",
                Password:  "changeme",
            }).GetConfig(),
        },
    }
}
```

### SFTP Samples (`checksftp/samples.go`)

```go
func (c *SFTPChecker) GetSampleConfigs(_ *ListSampleOptions) []CheckSpec {
    return []CheckSpec{
        {
            Name:   "SFTP Server",
            Slug:   "sftp-server",
            Period: 5 * time.Minute,
            Config: (&SFTPConfig{
                Host:     "sftp.example.com",
                Username: "deploy",
                Password: "changeme",
            }).GetConfig(),
        },
        {
            Name:   "SFTP File Check",
            Slug:   "sftp-file-check",
            Period: 15 * time.Minute,
            Config: (&SFTPConfig{
                Host:     "sftp.example.com",
                Username: "monitoring",
                Password: "changeme",
                Path:     "/data/daily-export.csv",
            }).GetConfig(),
        },
    }
}
```

---

## JSON Examples

### FTP — Anonymous login

```json
{
  "type": "ftp",
  "config": {
    "host": "ftp.example.com"
  }
}
```

### FTP — Authenticated with file check

```json
{
  "type": "ftp",
  "config": {
    "host": "ftp.example.com",
    "username": "uploader",
    "password": "s3cret",
    "path": "/uploads/daily-export.csv",
    "expected_content": "SUCCESS"
  }
}
```

### FTP — Explicit TLS (STARTTLS)

```json
{
  "type": "ftp",
  "config": {
    "host": "ftp.example.com",
    "tls_mode": "explicit",
    "tls_verify": true,
    "username": "secure",
    "password": "s3cret"
  }
}
```

### FTP — Implicit TLS (port 990)

```json
{
  "type": "ftp",
  "config": {
    "host": "ftp.example.com",
    "tls_mode": "implicit",
    "tls_verify": true,
    "username": "secure",
    "password": "s3cret"
  }
}
```

### SFTP — Password authentication

```json
{
  "type": "sftp",
  "config": {
    "host": "sftp.example.com",
    "username": "deploy",
    "password": "s3cret"
  }
}
```

### SFTP — Private key with fingerprint verification

```json
{
  "type": "sftp",
  "config": {
    "host": "sftp.example.com",
    "username": "deploy",
    "private_key": "-----BEGIN OPENSSH PRIVATE KEY-----\n...\n-----END OPENSSH PRIVATE KEY-----",
    "expected_fingerprint": "SHA256:uNiVztksCsDhcc0u9e8BujQXVUpKZIDTMczCvj3tD2s"
  }
}
```

### SFTP — File existence and content check

```json
{
  "type": "sftp",
  "config": {
    "host": "sftp.example.com",
    "username": "monitoring",
    "password": "s3cret",
    "path": "/data/daily-export.csv",
    "expected_content": "COMPLETE"
  }
}
```

---

## Security Considerations

- `password` and `private_key` are stored in the check's JSONB `config` column, already access-controlled per organization
- The API should redact `password` and `private_key` in GET responses (return `"***"`)
- File content reads are limited to 64KB to prevent excessive memory usage
- `expected_content` only reads — no write operations are ever performed
- Timeout applies to the entire check to prevent hanging connections
- FTP active mode is discouraged (passive is default) to avoid firewall issues
- SFTP inherits SSH security model (host key verification, encrypted channel)

---

## Competitor Reference

- **UptimeRobot**: FTP monitoring (login check)
- **StatusCake**: FTP monitoring with authentication
- **Uptime Kuma**: No native FTP/SFTP support
- **Gatus**: No native FTP/SFTP support
- **Better Uptime**: FTP monitoring (basic)
- SolidPing differentiators: file existence verification, content matching, SFTP with key auth, TLS mode selection
