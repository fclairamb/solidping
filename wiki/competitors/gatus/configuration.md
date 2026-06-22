# Gatus — Configuration & Technology Stack

## Configuration

### YAML Configuration

**Main config file**: `config/config.yaml` (or custom path)

**Structure**:
```yaml
# Storage configuration
storage:
  type: sqlite
  path: data/gatus.db

# Security
security:
  basic:
    username: admin
    password-sha512: "hashed_password"

# Alerting providers
alerting:
  slack:
    webhook-url: "https://hooks.slack.com/..."
    default-alert:
      description: "Health check failed"
      send-on-resolved: true

# Web UI configuration
web:
  address: 0.0.0.0
  port: 8080

# UI customization
ui:
  title: "Status Page"
  header: "Service Status"
  logo: "https://example.com/logo.png"

# Endpoints to monitor
endpoints:
  - name: website
    group: production
    url: "https://example.com"
    interval: 5m
    conditions:
      - "[STATUS] == 200"
      - "[RESPONSE_TIME] < 500"
    alerts:
      - type: slack
        enabled: true

  - name: api
    url: "https://api.example.com/health"
    interval: 1m
    conditions:
      - "[STATUS] == 200"
      - "[BODY].status == healthy"
      - "len([BODY].errors) == 0"
    alerts:
      - type: pagerduty
        send-on-resolved: true
```

### Endpoint Configuration

**Required fields**:
- `name` - Endpoint name
- `url` - Target URL
- `conditions` - Health check conditions

**Optional fields**:
- `group` - Group organization
- `interval` - Check frequency (default: 1m)
- `client` - HTTP client config
- `alerts` - Alert configuration
- `ui` - UI display options

**HTTP Client Options**:
```yaml
client:
  timeout: 10s
  insecure: false  # Skip TLS verification
  oauth2:
    token-url: "..."
    client-id: "..."
    client-secret: "..."
```

### Condition Placeholders

**Available placeholders**:

| Placeholder | Description | Example |
|-------------|-------------|---------|
| `[STATUS]` | HTTP status code | `[STATUS] == 200` |
| `[BODY]` | Response body (JSONPath) | `[BODY].status == UP` |
| `[RESPONSE_TIME]` | Response time (ms) | `[RESPONSE_TIME] < 1000` |
| `[IP]` | Resolved IP address | `[IP] == 1.2.3.4` |
| `[CERTIFICATE_EXPIRATION]` | Certificate validity | `[CERTIFICATE_EXPIRATION] > 168h` |
| `[DOMAIN_EXPIRATION]` | Domain expiry | `[DOMAIN_EXPIRATION] > 720h` |
| `[DNS_RCODE]` | DNS response code | `[DNS_RCODE] == NOERROR` |
| `[CONNECTED]` | Connection status | `[CONNECTED] == true` |

**Functions**:
- `len([BODY].array)` - Array/string length
- `has([BODY].field)` - Field existence
- `pat(pattern, value)` - Pattern matching

## Technology Stack

### Backend
- **Language**: Go (100%)
- **Performance**: Compiled binary, fast execution
- **Concurrency**: Go routines for parallel checks
- **Memory**: Low memory footprint
- **Binary size**: Small (< 20 MB)

### Frontend
- **Stack**: Simple HTML, CSS, JavaScript
- **No framework**: Vanilla JS for performance
- **Real-time**: Auto-refresh, no WebSocket needed
- **Responsive**: Mobile-friendly design

### Storage
- **Options**: In-memory, SQLite, PostgreSQL
- **Default**: SQLite (if persistence enabled)
- **Flexibility**: Choose based on needs
- **Migration**: Easy database switching
