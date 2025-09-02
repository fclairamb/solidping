# SMTP Monitoring Check Specification

## Overview

The SMTP checker validates mail server availability and health by connecting to an SMTP server, performing the greeting exchange, and optionally testing STARTTLS and authentication capabilities. This goes beyond a simple TCP port check by actually speaking the SMTP protocol and validating server responses.

## Implementation

- Checker type: `"smtp"`
- Package location: `/back/internal/checkers/checksmtp/`
- Based on Go's `net`, `net/textproto`, and `crypto/tls` packages
- Follows the same patterns as `checktcp` (TCP-based, connection + protocol exchange)

## Configuration

### Required Fields

- `host` (string) - SMTP server hostname or IP address

### Optional Fields

- `port` (int, default: 25) - SMTP port. Common values:
  - 25: Standard SMTP (relay/MTA-to-MTA)
  - 465: SMTPS (implicit TLS, legacy but still used)
  - 587: Submission (STARTTLS)
- `timeout` (duration string, default: "10s") - Maximum time for the full check. Must be > 0 and <= 60s
- `starttls` (bool, default: false) - Attempt STARTTLS upgrade after greeting
- `tls_verify` (bool, default: true) - Verify TLS certificate validity (when using STARTTLS or port 465)
- `tls_server_name` (string, optional) - Override SNI server name for TLS (defaults to `host`)
- `ehlo_domain` (string, default: "solidping.local") - Domain to send in EHLO command
- `expect_greeting` (string, optional) - Expected substring in the 220 greeting banner (e.g., "Postfix", "Microsoft ESMTP")
- `check_auth` (bool, default: false) - Verify that the server advertises AUTH in EHLO response

## Validation Rules

- `host` must not be empty
- `port` must be 1-65535
- `timeout` must be > 0 and <= 60s
- If `starttls` is true and `port` is 465, return a validation error (port 465 uses implicit TLS, not STARTTLS)

## SMTP Protocol Flow

### Basic Check (default)

```
1. TCP connect to host:port
2. Read 220 greeting banner
3. Send EHLO solidping.local
4. Read 250 response (parse capabilities)
5. Send QUIT
6. Evaluate results
```

### With STARTTLS

```
1. TCP connect to host:port
2. Read 220 greeting banner
3. Send EHLO solidping.local
4. Read 250 response (verify STARTTLS advertised)
5. Send STARTTLS
6. Read 220 response
7. Perform TLS handshake
8. Send EHLO solidping.local (again, per RFC)
9. Read 250 response
10. Send QUIT
11. Evaluate results
```

### With Implicit TLS (port 465)

```
1. TLS connect to host:port
2. Read 220 greeting banner
3. Send EHLO solidping.local
4. Read 250 response
5. Send QUIT
6. Evaluate results
```

## Execution Behavior

### Success Criteria (StatusUp)

- TCP connection established (or TLS for port 465)
- Server responds with 220 greeting
- EHLO accepted with 250 response
- If `starttls: true`: STARTTLS upgrade succeeds and TLS handshake completes
- If `expect_greeting` set: greeting banner contains the expected substring
- If `check_auth: true`: server advertises AUTH capability in EHLO response

### Failure Criteria (StatusDown)

- Server responds with non-220 greeting (e.g., 421 service unavailable, 554 rejected)
- EHLO rejected (non-250 response)
- If `starttls: true`: server doesn't advertise STARTTLS, or STARTTLS command fails
- If `starttls: true` and `tls_verify: true`: certificate is invalid/untrusted/hostname mismatch
- If `expect_greeting` set: greeting doesn't contain expected substring
- If `check_auth: true`: server doesn't advertise AUTH

### Timeout (StatusTimeout)

- Connection or any protocol exchange exceeds timeout
- Context cancelled

### Error (StatusError)

- DNS resolution failed
- Network unreachable
- Unexpected protocol errors

## Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `connection_time_ms` | float64 | TCP connection establishment time |
| `greeting_time_ms` | float64 | Time to receive 220 greeting after connect |
| `ehlo_time_ms` | float64 | Time for EHLO exchange |
| `starttls_time_ms` | float64 | STARTTLS + TLS handshake time (if applicable) |
| `total_time_ms` | float64 | Total check duration |

## Output

| Field | Type | Description |
|-------|------|-------------|
| `host` | string | Resolved IP address |
| `port` | int | Port connected to |
| `greeting` | string | Full 220 greeting banner |
| `ehlo_capabilities` | []string | Capabilities from EHLO response (e.g., STARTTLS, AUTH, PIPELINING, SIZE) |
| `tls_version` | string | Negotiated TLS version (if TLS used) |
| `tls_cipher` | string | Negotiated cipher suite (if TLS used) |
| `auth_mechanisms` | []string | Advertised AUTH mechanisms (e.g., PLAIN, LOGIN, CRAM-MD5) |
| `error` | string | Error message if check failed |

## Examples

### Basic SMTP Check (port 25)
```json
{
  "host": "mail.example.com"
}
```

### Submission Port with STARTTLS
```json
{
  "host": "smtp.gmail.com",
  "port": 587,
  "starttls": true
}
```

### Implicit TLS (port 465)
```json
{
  "host": "smtp.gmail.com",
  "port": 465
}
```

### Verify Specific Mail Server Software
```json
{
  "host": "mail.example.com",
  "expect_greeting": "Postfix"
}
```

### Full Check with Auth Verification
```json
{
  "host": "smtp.example.com",
  "port": 587,
  "starttls": true,
  "check_auth": true,
  "ehlo_domain": "monitoring.example.com"
}
```

## Implementation Notes

### Go Packages

- Use `net.Dialer` with context for TCP connection (consistent with `checktcp`)
- Use `net/textproto.Conn` for SMTP line-based protocol handling
- Use `crypto/tls` for STARTTLS upgrade and implicit TLS
- Do NOT use `net/smtp` package - it doesn't provide enough control over timing metrics and error handling

### Port 465 Detection

When `port` is 465 and `starttls` is not explicitly set, automatically use implicit TLS (wrap the TCP connection in TLS before reading the greeting). This mirrors how `checktcp` handles the `tls` flag.

### EHLO Capability Parsing

Parse the multi-line 250 EHLO response to extract capabilities:
```
250-mail.example.com
250-PIPELINING
250-SIZE 52428800
250-STARTTLS
250-AUTH LOGIN PLAIN CRAM-MD5
250-ENHANCEDSTATUSCODES
250 8BITMIME
```

Extract:
- `ehlo_capabilities`: ["PIPELINING", "SIZE", "STARTTLS", "AUTH", "ENHANCEDSTATUSCODES", "8BITMIME"]
- `auth_mechanisms`: ["LOGIN", "PLAIN", "CRAM-MD5"] (parsed from AUTH line)

### Error Messages

Provide clear, actionable error messages:
- "greeting rejected: 421 Service not available" (include the actual SMTP response)
- "STARTTLS not advertised by server" (server doesn't support it)
- "STARTTLS handshake failed: certificate signed by unknown authority"
- "AUTH not advertised by server" (when check_auth is true)

### File Structure

```
internal/checkers/checksmtp/
├── config.go       # SMTPConfig struct with FromMap(), GetConfig()
├── checker.go      # SMTPChecker struct with Type(), Validate(), Execute()
├── checker_test.go # Table-driven tests with t.Parallel(), testify/require
└── errors.go       # ErrInvalidConfigType and other package-level errors
```

### Registry Integration

1. Add `CheckTypeSMTP CheckType = "smtp"` to `checkerdef/types.go`
2. Add to `ListCheckTypes()` in `checkerdef/types.go`
3. Add case to `GetChecker()` in `registry/registry.go`
4. Add case to `ParseConfig()` in `registry/registry.go`

## Test Plan

### Unit Tests

- **Config parsing**: FromMap with all field combinations, JSON float64 handling for port
- **Validation**: Required host, port range, timeout range, starttls+465 conflict
- **GetConfig roundtrip**: FromMap -> GetConfig -> FromMap produces same result

### Integration Tests (with real SMTP server or testcontainer)

- Basic connection to port 25
- STARTTLS upgrade on port 587
- Implicit TLS on port 465
- Greeting banner validation (expect_greeting match/mismatch)
- AUTH capability detection
- Connection refused (StatusDown)
- Timeout handling (StatusTimeout)
- Invalid TLS certificate with tls_verify: true (StatusDown)

### Test Targets

Public SMTP servers suitable for integration tests:
- `smtp.gmail.com:587` (STARTTLS)
- `smtp.gmail.com:465` (implicit TLS)
- Or use a containerized mail server (e.g., `mailhog/mailhog`, `axllent/mailpit`) for deterministic testing
