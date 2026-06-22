# UptimeRobot — API

## API Architecture

### API Versions

UptimeRobot offers two API versions:

**v3 (Current)** - Modern RESTful API introduced September 2025:
- Standard HTTP verbs (GET, POST, PATCH, DELETE)
- Resource-oriented paths (e.g., `/monitors`, `/monitors/{id}`)
- JWT bearer token authentication
- JSON-only responses
- Cursor-based pagination
- CORS support for browser clients

**v2 (Legacy)** - Available at `/v2/` endpoint:
- POST-only requests
- Verb-style endpoint names (getMonitors, newMonitor, etc.)
- API key in request body or URL parameter
- Multiple response formats (JSON, JSON-P, XML)
- Offset-based pagination

**Recommendation**: Use v3 for new integrations. V2 remains available for backward compatibility.

### Authentication

UptimeRobot uses **HTTP Basic Access Authentication** with three API key types:

1. **Account-specific API key**
   - Full access to all API methods
   - Manage all monitors in the account
   - Create/edit/delete monitors, alert contacts, maintenance windows, status pages

2. **Monitor-specific API keys**
   - Limited to `GET /monitors` endpoint
   - Read-only access to specific monitors
   - Useful for sharing monitor data without exposing account control

3. **Read-only API key**
   - Access restricted to GET endpoints only
   - Cannot modify any resources
   - Safe for embedding in client applications

**Obtaining API Keys**:
1. Log in to UptimeRobot dashboard
2. Navigate to Integrations & API in sidebar
3. Choose API section
4. Create main API key or monitor-specific keys

**v3 Authentication Header**:
```
Authorization: Bearer YOUR_API_KEY
```

**v2 Authentication** (legacy):
```json
{
  "api_key": "YOUR_API_KEY"
}
```

### Rate Limiting

Rate limits vary by subscription tier:

**Free Plan**:
- 10 requests/minute

**Pro Plans**:
- Formula: `monitor_limit × 2 req/min`
- Maximum: 5,000 requests/minute
- Example: 100 monitors = 200 req/min

**Rate Limit Headers** (returned in all responses):
- `X-RateLimit-Limit` - Current rate limit
- `X-RateLimit-Remaining` - Remaining calls in current window
- `X-RateLimit-Reset` - Reset time (Unix epoch seconds)
- `Retry-After` - Recommended retry delay (when limit exceeded)

**HTTP Status**: 429 Too Many Requests when limit exceeded

### API Standards

- RESTful architecture (v3)
- JSON request/response format
- Standard HTTP status codes
- Cursor-based pagination (v3)
- CORS-enabled for browser clients (v3)

## Core API Endpoints (v3)

### Monitors

#### Monitor Types

UptimeRobot supports the following monitor types (v3 uses descriptive strings):

- **HTTP** - HTTP/HTTPS website monitoring
- **KEYWORD** - Keyword presence/absence checking
- **PING** - ICMP ping monitoring
- **PORT** - TCP port connectivity
- **HEARTBEAT** - Cron job/scheduled task monitoring (reverse monitoring)
- **SSL** - SSL certificate expiration monitoring
- **DOMAIN** - Domain expiration monitoring
- **DNS** - DNS record monitoring

#### List Monitors

**Endpoint**: `GET /monitors`

**Query Parameters**:
- `limit` (integer) - Results per page
- `cursor` (string) - Pagination cursor
- `search` (string) - Filter by monitor name/URL
- `type` (string) - Filter by monitor type
- `status` (string) - Filter by status (up, down, paused, etc.)

**Example**:
```bash
curl --request GET \
  --url 'https://api.uptimerobot.com/v3/monitors' \
  --header 'Authorization: Bearer YOUR_API_KEY'
```

#### Get Single Monitor

**Endpoint**: `GET /monitors/{id}`

Returns complete monitor configuration and current status.

**Example**:
```bash
curl --request GET \
  --url 'https://api.uptimerobot.com/v3/monitors/123456789' \
  --header 'Authorization: Bearer YOUR_API_KEY'
```

#### Create Monitor

**Endpoint**: `POST /monitors`

**Required Parameters**:
- `type` (string) - Monitor type (HTTP, KEYWORD, PING, PORT, HEARTBEAT, SSL, DOMAIN, DNS)
- `friendly_name` (string) - Monitor display name
- `url` (string) - URL or IP address to monitor

**Optional Parameters**:

*HTTP/HTTPS Configuration*:
- `http_method` (string) - HEAD, GET, POST, PUT, PATCH, DELETE, OPTIONS
- `http_username` (string) - HTTP authentication username
- `http_password` (string) - HTTP authentication password
- `http_auth_type` (string) - BASIC or DIGEST
- `custom_http_headers` (array) - Custom headers [{name, value}]
- `custom_http_statuses` (array) - Valid status codes [200, 201, 204, etc.]
- `ignore_ssl_errors` (boolean) - Ignore SSL certificate errors
- `disable_ssl_expiry_reminders` (boolean) - Disable SSL expiry alerts

*Keyword Monitoring*:
- `keyword_type` (string) - EXISTS or NOT_EXISTS
- `keyword_value` (string) - Keyword to search for

*Port Monitoring*:
- `port` (integer) - Port number to check (1-65535)

*Heartbeat Monitoring*:
- `heartbeat_interval` (integer) - Expected interval in seconds
- `heartbeat_grace_period` (integer) - Grace period before alerting

*DNS Monitoring*:
- `dns_record_type` (string) - A, AAAA, CNAME, MX
- `dns_expected_value` (string) - Expected DNS value

*General Settings*:
- `interval` (integer) - Check interval in seconds (60, 300, 600, 1800, 3600)
- `timeout` (integer) - Request timeout in seconds
- `alert_contacts` (array) - Array of alert contact IDs
- `mwindows` (array) - Array of maintenance window IDs
- `custom_uptime_ranges` (string) - Custom uptime calculation periods

**Example**:
```bash
curl --request POST \
  --url 'https://api.uptimerobot.com/v3/monitors' \
  --header 'Authorization: Bearer YOUR_API_KEY' \
  --header 'Content-Type: application/json' \
  --data '{
    "type": "HTTP",
    "friendly_name": "Production API",
    "url": "https://api.example.com/health",
    "interval": 60,
    "http_method": "GET",
    "custom_http_statuses": [200, 201, 204],
    "alert_contacts": ["123", "456"]
  }'
```

#### Update Monitor

**Endpoint**: `PATCH /monitors/{id}`

Send only the parameters you wish to change. Same parameters as create.

**Example**:
```bash
curl --request PATCH \
  --url 'https://api.uptimerobot.com/v3/monitors/123456789' \
  --header 'Authorization: Bearer YOUR_API_KEY' \
  --header 'Content-Type: application/json' \
  --data '{
    "interval": 60,
    "friendly_name": "Updated Monitor Name"
  }'
```

#### Delete Monitor

**Endpoint**: `DELETE /monitors/{id}`

Permanently deletes a monitor.

**Example**:
```bash
curl --request DELETE \
  --url 'https://api.uptimerobot.com/v3/monitors/123456789' \
  --header 'Authorization: Bearer YOUR_API_KEY'
```

#### Monitor Status Values

- `up` (0) - Monitor is operational
- `seems_down` (1) - Monitor appears to be down (first failure)
- `down` (2) - Monitor is confirmed down
- `paused` (8) - Monitoring is paused
- `started` (9) - Monitor just created, first check pending

### Alert Contacts

Alert contacts define how and where notifications are sent.

#### List Alert Contacts

**Endpoint**: `GET /alert-contacts`

**Example**:
```bash
curl --request GET \
  --url 'https://api.uptimerobot.com/v3/alert-contacts' \
  --header 'Authorization: Bearer YOUR_API_KEY'
```

#### Create Alert Contact

**Endpoint**: `POST /alert-contacts`

**Parameters**:
- `type` (string) - EMAIL, SMS, VOICE_CALL, WEBHOOK, SLACK, etc.
- `friendly_name` (string) - Display name
- `value` (string) - Contact value (email, phone, webhook URL, etc.)

**Example** (Email):
```bash
curl --request POST \
  --url 'https://api.uptimerobot.com/v3/alert-contacts' \
  --header 'Authorization: Bearer YOUR_API_KEY' \
  --header 'Content-Type: application/json' \
  --data '{
    "type": "EMAIL",
    "friendly_name": "DevOps Team",
    "value": "devops@example.com"
  }'
```

**Example** (Webhook):
```bash
curl --request POST \
  --url 'https://api.uptimerobot.com/v3/alert-contacts' \
  --header 'Authorization: Bearer YOUR_API_KEY' \
  --header 'Content-Type: application/json' \
  --data '{
    "type": "WEBHOOK",
    "friendly_name": "Custom Webhook",
    "value": "https://your-app.com/webhook",
    "webhook_method": "POST"
  }'
```

### Integrations

v3 API allows programmatic integration management for third-party services.

**Supported Integrations**:
- Slack
- Microsoft Teams
- Google Chat
- Discord
- Telegram
- Pushover
- Pushbullet
- PagerDuty
- Splunk
- Mattermost
- Zapier
- Webhooks

**Endpoint**: `POST /integrations`

Alert contacts are now created automatically when setting up integrations via API.

### Maintenance Windows

Maintenance windows prevent alerts during scheduled maintenance.

#### List Maintenance Windows

**Endpoint**: `GET /maintenance-windows`

**Note**: Requires Pro account with maintenance windows configured.

#### Create Maintenance Window

**Endpoint**: `POST /maintenance-windows`

**Parameters**:
- `friendly_name` (string) - Window name
- `type` (string) - ONCE, DAILY, WEEKLY, MONTHLY
- `start_time` (string) - Start time
- `duration` (integer) - Duration in minutes

### Public Status Pages

#### List Status Pages

**Endpoint**: `GET /psps`

Returns all public status pages (PSP = Public Status Page).

#### Get Status Page Details

**Endpoint**: `GET /psps/{id}`

**Example**:
```bash
curl --request GET \
  --url 'https://api.uptimerobot.com/v3/psps/12345' \
  --header 'Authorization: Bearer YOUR_API_KEY'
```

#### Create Status Page

**Endpoint**: `POST /psps`

**Parameters**:
- `friendly_name` (string) - Status page name
- `monitors` (array) - Monitor IDs to include
- `custom_domain` (string) - Custom domain (optional)
- `password` (string) - Password protection (optional)
- `sort` (string) - Monitor sorting (FRIENDLY_NAME_A_Z, etc.)

### User Information

#### Get User Profile

**Endpoint**: `GET /user/me`

Returns logged-in user's profile and subscription details.

**Example**:
```bash
curl --request GET \
  --url 'https://api.uptimerobot.com/v3/user/me' \
  --header 'Authorization: Bearer YOUR_API_KEY'
```

**Response includes**:
- Account email
- Subscription tier
- Monitor limits
- Current monitor count
- API rate limits

See [api-reference.md](api-reference.md) for the complete endpoint summary, monitor types, status values, alert contact types, HTTP methods, query parameters, status codes, and rate-limit headers.
