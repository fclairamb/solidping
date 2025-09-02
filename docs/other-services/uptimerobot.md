# UptimeRobot - Complete Analysis

## Overview

UptimeRobot positions itself as "the world's leading uptime monitoring service" with over 2.5 million active users. The platform offers a generous free tier with 50 monitors and provides comprehensive monitoring capabilities through a modern REST API.

**Base API URL**: `https://api.uptimerobot.com/v3/` (v3 is current, legacy v2 available)

**API Specification**: RESTful JSON API with JWT authentication

## Key Features

### Monitoring Capabilities

UptimeRobot supports multiple monitoring types:

- **HTTP/HTTPS monitoring** - Website uptime with customizable check intervals (5 minutes free, 30 seconds paid)
- **Keyword monitoring** - Verify specific text presence or absence in responses
- **Ping (ICMP) monitoring** - Server connectivity checks
- **Port monitoring** - TCP port connectivity for services (SMTP, DNS, FTP, etc.)
- **Heartbeat/Cron monitoring** - Scheduled job verification via reverse monitoring
- **SSL certificate monitoring** - Track certificate expiration with alerts
- **Domain expiration monitoring** - Domain renewal tracking
- **DNS monitoring** - Track DNS record changes (A, AAAA, CNAME, MX)

### Advanced Diagnostic Features

- **Multi-location monitoring** - Geo-verified checks from multiple regions
- **Response time tracking** - Monitor performance degradation
- **Custom HTTP headers** - Advanced request customization
- **HTTP method selection** - HEAD, GET, POST, PUT, PATCH, DELETE, OPTIONS
- **HTTP authentication** - Basic and Digest authentication support
- **Custom status codes** - Define success/failure HTTP codes
- **SSL error handling** - Option to ignore SSL errors

### Notification System

UptimeRobot integrates with 12+ alert destinations:
- **Email**
- **SMS**
- **Voice calls**
- **Slack**
- **Microsoft Teams**
- **Discord**
- **Telegram**
- **PagerDuty**
- **Pushover**
- **Pushbullet**
- **Zapier**
- **Webhooks** - Custom HTTP POST/GET callbacks
- **Push notifications** - Mobile app alerts

### Status Pages

- **Public status pages** - Free with all plans
- **White-labeled pages** - Custom branding
- **Custom domains** - Host on your own domain
- **Password protection** - Private status pages
- **Multiple status pages** - Organize by service/customer
- **Subscriber notifications** - Email subscribers about incidents

### Platform Features

- **Mobile apps** - iOS and Android native applications
- **No credit card required** - For free tier
- **Instant setup** - "Start monitoring in 30 seconds"
- **2.5+ million users** - Large, established user base

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

## Heartbeat Monitoring In-Depth

Heartbeat monitoring operates inversely to traditional monitoring: instead of UptimeRobot polling your service, your service pings UptimeRobot.

### How It Works

1. **Create heartbeat monitor** in UptimeRobot (via UI or API)
2. **Receive unique URL**: `https://heartbeat.uptimerobot.com/m[unique-identifier]`
   - Example: `https://heartbeat.uptimerobot.com/m794yyyyyyyy-xxxxxxxxxxxxxxx`
3. **Configure expected interval** (e.g., every 5 minutes)
4. **Add heartbeat call to your script**
5. **Monitor marked down** if heartbeat not received within interval + grace period

### Setup Process

**Unix/Linux (Crontab)**:
```bash
# Edit crontab
crontab -e

# Add heartbeat call (every 5 minutes)
*/5 * * * * curl https://heartbeat.uptimerobot.com/m794xxx-xxxxxxxx
```

**Using wget**:
```bash
*/5 * * * * wget -q -O /dev/null https://heartbeat.uptimerobot.com/m794xxx-xxxxxxxx
```

**Windows (Task Scheduler)**:
1. Create new task in Task Scheduler
2. Set trigger to match monitor interval (e.g., every 5 minutes)
3. Create action using PowerShell:
```powershell
Invoke-WebRequest -Uri "https://heartbeat.uptimerobot.com/m794xxx-xxxxxxxx"
```
4. Configure to run when no user is logged in

**Python Script**:
```python
import requests

# Your scheduled task
def daily_backup():
    # Perform backup
    backup_data()

    # Report success to UptimeRobot
    requests.get('https://heartbeat.uptimerobot.com/m794xxx-xxxxxxxx')

# In your cron/scheduler
daily_backup()
```

### Use Cases

- **Cron jobs** - Database backups, data exports, cleanup tasks
- **Scheduled tasks** - Windows Task Scheduler jobs
- **Background workers** - Queue processors, batch jobs
- **Serverless functions** - Scheduled Lambda/Cloud Functions
- **Intranet servers** - Internal servers with internet connectivity
- **Performance indicators** - Application health metrics
- **ETL processes** - Data pipeline monitoring

### Important Notes

- Heartbeat interval in cron **must match** UptimeRobot monitor interval
- GET or POST requests both work
- No authentication required on heartbeat URL (URL itself is secret)
- Heartbeat URL should be kept private
- Grace period provides buffer for timing variations
- Pro plan required for heartbeat monitoring

## Pricing Model

UptimeRobot offers four pricing tiers with different monitor limits and check intervals:

### Free Plan
- **Cost**: $0/month
- **Monitors**: 50
- **Check interval**: 5 minutes
- **Status pages**: 1 basic page
- **Alert contacts**: Unlimited
- **Monitor types**: All types supported
- **Features**:
  - Email alerts
  - SMS alerts (limited)
  - Public status pages
  - 50-second timeout
  - Mobile apps
  - No credit card required

### Solo Plan
- **Cost**: $7-8/month (billed annually)
- **Monitors**: 10
- **Check interval**: 1 minute
- **Status pages**: 1 customizable
- **Features**:
  - All Free features
  - 1-minute intervals
  - Keyword monitoring
  - Port monitoring
  - Advanced HTTP

**Note**: Free plan offers more monitors (50 vs 10) but slower intervals (5min vs 1min)

### Team Plan
- **Cost**: $29-34/month (billed annually)
- **Monitors**: 50-100 (varies)
- **Check interval**: 1 minute
- **Status pages**: Multiple
- **Features**:
  - All Solo features
  - Team collaboration
  - Multiple status pages
  - Advanced notifications

### Enterprise Plan
- **Cost**: $54-64/month+ (custom pricing)
- **Monitors**: 200+
- **Check interval**: 30 seconds
- **Status pages**: Unlimited
- **Features**:
  - All Team features
  - 30-second intervals
  - White-label status pages
  - Priority support
  - Custom domain status pages
  - Dedicated account manager

### Pricing Insights

**Free tier advantage**: UptimeRobot's 50 free monitors significantly exceeds competitors:
- Pingdom: 1 monitor free
- StatusCake: 10 monitors free
- BetterStack: 10 monitors free

**Best value**: Free plan for hobbyists/small projects, Enterprise for production needs requiring fast intervals

**Upgrade drivers**: Need for 1-minute or 30-second intervals, heartbeat monitoring, advanced features

## Comparison with SolidPing

### Similarities

Both platforms offer:
- HTTP/HTTPS uptime monitoring
- Keyword monitoring
- Ping monitoring
- Port/TCP monitoring
- Heartbeat/cron monitoring
- REST APIs for programmatic access
- Status tracking and reporting
- Multiple notification channels
- Alert management

### UptimeRobot Advantages

1. **Generous free tier** - 50 monitors vs typical 5-10
2. **Established platform** - 2.5+ million users, proven reliability
3. **Native mobile apps** - iOS and Android applications
4. **Multiple integrations** - 12+ notification channels pre-built
5. **Multi-location monitoring** - Geographic redundancy
6. **DNS monitoring** - Track DNS record changes
7. **Domain expiration tracking** - Domain renewal alerts
8. **Public status pages** - Free with all plans
9. **Mature ecosystem** - Terraform provider, extensive third-party integrations
10. **No setup complexity** - "30 seconds to start monitoring"

### SolidPing Advantages

1. **Self-hosted option** - Full data ownership and control
2. **Open source potential** - Customizable and extensible
3. **No vendor lock-in** - Own your monitoring infrastructure
4. **Direct database access** - PostgreSQL for custom queries
5. **Privacy-first** - No third-party data sharing
6. **No account limits** - Unlimited monitors on self-hosted
7. **Cost control** - No recurring fees for self-hosted deployment
8. **Simpler architecture** - Easier to understand and modify
9. **API-first design** - Built for developers from ground up
10. **PostgreSQL-native** - Familiar tooling and ecosystem

### Feature Gaps in SolidPing

Areas where SolidPing could consider expansion to match UptimeRobot:

1. **DNS monitoring** - Track DNS record changes and expiry
2. **Domain expiration** - Domain renewal tracking
3. **Multi-location checks** - Geographic redundancy
4. **Native integrations** - Pre-built Slack, Teams, Discord, PagerDuty
5. **Mobile applications** - iOS and Android apps
6. **Status page hosting** - Public status communication
7. **Maintenance windows** - Scheduled downtime handling
8. **Monitor grouping** - Organize monitors by service/customer
9. **Custom HTTP methods** - PUT, PATCH, DELETE support
10. **HTTP authentication** - Basic/Digest auth for monitors
11. **Custom status codes** - Define success/failure codes
12. **SSL error handling** - Option to ignore SSL errors
13. **Keyword absence** - Alert when keyword disappears
14. **Monitor-specific API keys** - Granular access control

## Technical Considerations

### Rate Limits

**Free Plan**: 10 requests/minute
- Suitable for small scripts and periodic checks
- Not suitable for real-time dashboards

**Pro Plans**: 2× monitor count, max 5,000/min
- 100 monitors = 200 req/min
- 200 monitors = 400 req/min
- Scales with usage

**Headers**: Rate limit info in every response
- Plan monitoring usage accordingly
- Implement exponential backoff

### API Versioning

**v3 (Current)**:
- Modern REST architecture
- Active development
- New features added regularly
- Recommended for all new integrations

**v2 (Legacy)**:
- Still supported
- No new features
- Maintained for backward compatibility
- Will eventually be deprecated

**Migration**: v2 to v3 migration guide available

### Data Retention

- Free plan: 6 months of logs
- Pro plans: 12+ months of logs
- Response time history available
- Uptime statistics calculated from historical data

### Response Times

- Monitor checks: 5min (free) to 30sec (enterprise)
- API response time: Typically <200ms
- Webhook delivery: Near real-time (<5 seconds)
- Status page updates: Immediate

### Security

- HTTPS required for all API calls
- Bearer token authentication (v3)
- API keys should be kept secret
- Monitor-specific keys for limited access
- Read-only keys for safe embedding
- Heartbeat URLs are unguessable but unprotected
- CORS support for browser clients (v3)

### Monitoring from Multiple Locations

UptimeRobot performs geo-verified checks:
- Monitors from multiple geographic locations
- Down status only after multiple locations confirm
- Reduces false positives from regional issues
- Exact locations not publicly documented

## Limitations and Gaps

1. **Free tier intervals** - 5-minute minimum (vs 1-minute competitors)
2. **Limited protocol support** - No SMTP, IMAP, POP3, DNS query monitoring
3. **No browser testing** - No Playwright/Puppeteer support
4. **Basic incident management** - No on-call scheduling, escalation rules
5. **Limited customization** - Less flexible than self-hosted solutions
6. **Heartbeat Pro-only** - Free plan lacks heartbeat monitoring
7. **Rate limits on free** - 10 req/min may be restrictive
8. **No official webhook push** - Must poll API for changes
9. **Limited filtering** - API list endpoints have basic filtering
10. **Closed source** - No code inspection or self-hosting
11. **US-centric** - Primary servers in US (may affect latency)
12. **No SLA guarantees** - Even on paid plans
13. **Limited status page customization** - On free/solo tiers

## API Design Patterns

### Good Patterns to Adopt

1. **Generous free tier** - 50 monitors attracts users
2. **Simple authentication** - Bearer tokens easy to implement
3. **Monitor-specific keys** - Granular access control
4. **Rate limit headers** - Transparent quota information
5. **Multiple auth types** - Read-only, full access, monitor-specific
6. **Cursor pagination** - Scales better than offset pagination
7. **Resource-oriented design** - RESTful conventions (v3)
8. **Structured parameters** - Named fields vs numeric codes
9. **CORS support** - Browser-friendly API
10. **Version migration** - Support legacy v2 during transition

### Patterns to Consider

1. **Friendly names** - pronounceable_name for voice alerts
2. **Heartbeat monitoring** - Reverse monitoring for cron jobs
3. **Multi-location checks** - Reduce false positives
4. **Custom uptime ranges** - Flexible SLA calculations
5. **Maintenance windows** - Scheduled downtime support
6. **Status pages API** - Programmatic status page management
7. **Integration endpoints** - Unified integration management

### Patterns to Avoid

1. **Numeric type codes** - v2's type=1 vs v3's type=HTTP
2. **POST-only API** - v2 limitation, v3 fixed with proper REST
3. **Mixed pagination** - v2 offset vs v3 cursor inconsistency
4. **Required plan features** - Heartbeat Pro-only limits free tier value
5. **Undocumented locations** - Multi-location checking details unclear
6. **Limited free intervals** - 5-minute minimum less competitive

## Integration Examples

### Monitoring a Web Service

```bash
# Create HTTP monitor with custom headers
curl --request POST \
  --url 'https://api.uptimerobot.com/v3/monitors' \
  --header 'Authorization: Bearer YOUR_API_KEY' \
  --header 'Content-Type: application/json' \
  --data '{
    "type": "HTTP",
    "friendly_name": "API Health Check",
    "url": "https://api.example.com/health",
    "interval": 60,
    "http_method": "GET",
    "custom_http_headers": [
      {"name": "X-API-Key", "value": "secret123"},
      {"name": "Accept", "value": "application/json"}
    ],
    "custom_http_statuses": [200, 201, 204],
    "timeout": 30
  }'
```

### Keyword Monitoring

```bash
# Alert when "error" appears on page
curl --request POST \
  --url 'https://api.uptimerobot.com/v3/monitors' \
  --header 'Authorization: Bearer YOUR_API_KEY' \
  --header 'Content-Type: application/json' \
  --data '{
    "type": "KEYWORD",
    "friendly_name": "Error Detection",
    "url": "https://example.com/status",
    "keyword_type": "NOT_EXISTS",
    "keyword_value": "error",
    "interval": 300
  }'
```

### Port Monitoring

```bash
# Monitor database port
curl --request POST \
  --url 'https://api.uptimerobot.com/v3/monitors' \
  --header 'Authorization: Bearer YOUR_API_KEY' \
  --header 'Content-Type: application/json' \
  --data '{
    "type": "PORT",
    "friendly_name": "PostgreSQL",
    "url": "db.example.com",
    "port": 5432,
    "interval": 300
  }'
```

### Heartbeat Monitoring

```bash
# 1. Create heartbeat monitor
curl --request POST \
  --url 'https://api.uptimerobot.com/v3/monitors' \
  --header 'Authorization: Bearer YOUR_API_KEY' \
  --header 'Content-Type: application/json' \
  --data '{
    "type": "HEARTBEAT",
    "friendly_name": "Daily Backup Job",
    "heartbeat_interval": 86400,
    "heartbeat_grace_period": 3600
  }'

# 2. Response includes heartbeat URL
# 3. Add to your cron job:
0 2 * * * /usr/local/bin/backup.sh && curl https://heartbeat.uptimerobot.com/m794xxx-xxxxxxxx
```

### Creating Slack Integration

```bash
# Create Slack alert contact
curl --request POST \
  --url 'https://api.uptimerobot.com/v3/integrations' \
  --header 'Authorization: Bearer YOUR_API_KEY' \
  --header 'Content-Type: application/json' \
  --data '{
    "type": "SLACK",
    "webhook_url": "https://hooks.slack.com/services/T00000000/B00000000/XXXXXXXXXXXXXXXXXXXX",
    "friendly_name": "DevOps Channel"
  }'
```

### Checking Monitor Status

```bash
# List all monitors with their status
curl --request GET \
  --url 'https://api.uptimerobot.com/v3/monitors' \
  --header 'Authorization: Bearer YOUR_API_KEY' \
  | jq '.monitors[] | {name: .friendly_name, status: .status, uptime: .uptime}'
```

### Creating Public Status Page

```bash
# Create status page with selected monitors
curl --request POST \
  --url 'https://api.uptimerobot.com/v3/psps' \
  --header 'Authorization: Bearer YOUR_API_KEY' \
  --header 'Content-Type: application/json' \
  --data '{
    "friendly_name": "Service Status",
    "monitors": ["123456", "789012", "345678"],
    "sort": "FRIENDLY_NAME_A_Z",
    "hide_url_links": false
  }'
```

## Complete API Reference

### Endpoint Summary

Quick reference of all v3 API endpoints:

| Endpoint | Method | Description |
|----------|--------|-------------|
| **Monitors** |
| `/monitors` | GET | List all monitors |
| `/monitors` | POST | Create a new monitor |
| `/monitors/{id}` | GET | Get single monitor details |
| `/monitors/{id}` | PATCH | Update existing monitor |
| `/monitors/{id}` | DELETE | Delete monitor |
| `/monitors/{id}/reset` | POST | Reset monitor statistics |
| **Alert Contacts** |
| `/alert-contacts` | GET | List all alert contacts |
| `/alert-contacts` | POST | Create new alert contact |
| `/alert-contacts/{id}` | GET | Get single alert contact |
| `/alert-contacts/{id}` | PATCH | Update alert contact |
| `/alert-contacts/{id}` | DELETE | Delete alert contact |
| **Integrations** |
| `/integrations` | GET | List all integrations |
| `/integrations` | POST | Create new integration |
| `/integrations/{id}` | DELETE | Delete integration |
| **Maintenance Windows** |
| `/maintenance-windows` | GET | List maintenance windows |
| `/maintenance-windows` | POST | Create maintenance window |
| `/maintenance-windows/{id}` | PATCH | Update maintenance window |
| `/maintenance-windows/{id}` | DELETE | Delete maintenance window |
| **Public Status Pages** |
| `/psps` | GET | List all status pages |
| `/psps` | POST | Create status page |
| `/psps/{id}` | GET | Get status page details |
| `/psps/{id}` | PATCH | Update status page |
| `/psps/{id}` | DELETE | Delete status page |
| **User** |
| `/user/me` | GET | Get user profile and limits |
| `/user/alert-contacts` | GET | Get user's alert contacts |

### Monitor Types Reference

| Type | Code (v2) | String (v3) | Description | Use Case |
|------|-----------|-------------|-------------|----------|
| HTTP(S) | 1 | HTTP | Website monitoring via HTTP/HTTPS | Websites, APIs, web apps |
| Keyword | 2 | KEYWORD | Content presence/absence | Error detection, content verification |
| Ping | 3 | PING | ICMP ping monitoring | Server connectivity |
| Port | 4 | PORT | TCP port connectivity | Database, SMTP, FTP, custom services |
| Heartbeat | 5 | HEARTBEAT | Reverse monitoring (cron jobs) | Scheduled tasks, background jobs |
| SSL | 6 | SSL | SSL certificate expiration | Certificate renewal tracking |
| Domain | 7 | DOMAIN | Domain expiration | Domain renewal alerts |
| DNS | 8 | DNS | DNS record monitoring | DNS record change detection |

### Monitor Status Values

| Status | Code (v2) | Description |
|--------|-----------|-------------|
| Paused | 0 | Monitor is paused |
| Not checked yet | 1 | New monitor, first check pending |
| Up | 2 | Monitor is operational |
| Seems down | 8 | First failure detected |
| Down | 9 | Confirmed down (multiple checks) |

### Alert Contact Types

| Type | Description | Configuration |
|------|-------------|---------------|
| EMAIL | Email notifications | Email address |
| SMS | SMS text messages | Phone number with country code |
| VOICE_CALL | Voice call alerts | Phone number |
| WEBHOOK | Custom HTTP callbacks | Webhook URL, optional method |
| SLACK | Slack channel notifications | Webhook URL |
| TELEGRAM | Telegram messages | Bot token + chat ID |
| DISCORD | Discord channel messages | Webhook URL |
| PUSHOVER | Pushover notifications | User key + API token |
| PUSHBULLET | Pushbullet push notifications | Access token |
| ZAPIER | Zapier workflow trigger | Webhook URL |

### HTTP Methods Supported

- GET - Retrieve resource
- HEAD - Check resource existence
- POST - Submit data
- PUT - Replace resource
- PATCH - Partial update
- DELETE - Remove resource
- OPTIONS - Check allowed methods

### Common Query Parameters

| Parameter | Endpoints | Type | Description |
|-----------|-----------|------|-------------|
| `limit` | List endpoints | integer | Results per page (default: 50, max: 50) |
| `cursor` | List endpoints | string | Pagination cursor |
| `search` | GET /monitors | string | Filter by name/URL |
| `type` | GET /monitors | string | Filter by monitor type |
| `status` | GET /monitors | string | Filter by status |

### HTTP Status Codes

| Code | Meaning | When Returned |
|------|---------|---------------|
| 200 | OK | Successful GET/PATCH requests |
| 201 | Created | Successful POST (create) requests |
| 204 | No Content | Successful DELETE requests |
| 400 | Bad Request | Invalid parameters or malformed request |
| 401 | Unauthorized | Missing or invalid API key |
| 403 | Forbidden | API key lacks required permissions |
| 404 | Not Found | Resource doesn't exist |
| 429 | Too Many Requests | Rate limit exceeded |
| 500 | Internal Server Error | UptimeRobot server error |

### Rate Limit Response Headers

| Header | Description | Example |
|--------|-------------|---------|
| X-RateLimit-Limit | Total requests allowed | 600 |
| X-RateLimit-Remaining | Requests remaining in window | 587 |
| X-RateLimit-Reset | Unix timestamp when limit resets | 1640995200 |
| Retry-After | Seconds to wait before retry | 60 |

## Conclusion

UptimeRobot is a mature, widely-used monitoring platform with a generous free tier and modern REST API. It excels at:

- **Accessibility** - 50 free monitors and easy setup
- **Simplicity** - Clean UI and straightforward API
- **Reliability** - 2.5+ million users, proven track record
- **Integration breadth** - 12+ notification channels
- **Mobile experience** - Native iOS and Android apps

However, it's a closed-source SaaS platform with:
- Limited free tier intervals (5 minutes)
- Heartbeat monitoring requires paid plan
- Less flexibility than self-hosted solutions
- No advanced incident management features

For SolidPing, UptimeRobot serves as an excellent reference for:
- API design evolution (v2 to v3 migration)
- Generous free tier strategy
- Heartbeat monitoring implementation
- Multi-location checking approach
- Status page API design
- Integration management patterns

While SolidPing may not need UptimeRobot's scale, understanding its approach to user acquisition (generous free tier), API modernization (v2 to v3), and feature prioritization helps inform development decisions.

## Sources

### General Documentation
- [UptimeRobot Homepage](https://uptimerobot.com)
- [UptimeRobot API Overview](https://uptimerobot.com/api/)
- [Plans & Pricing](https://uptimerobot.com/pricing/)

### API Documentation
- [API V3 Documentation](https://uptimerobot.com/api/v3/)
- [API V2 (Legacy) Documentation](https://uptimerobot.com/api/legacy/)
- [How to Use UptimeRobot's API](https://help.uptimerobot.com/en/articles/11620152-how-to-use-uptimerobot-s-api)

### Feature Documentation
- [Monitor Types Explained](https://help.uptimerobot.com/en/articles/11358441-uptimerobot-monitor-types-explained-http-ping-port-keyword-monitoring)
- [Ultimate Guide to Uptime Monitoring Types](https://uptimerobot.com/knowledge-hub/monitoring/ultimate-guide-to-uptime-monitoring-types/)
- [Heartbeat Monitoring Guide](https://uptimerobot.com/help/heartbeat-monitoring/)
- [Heartbeat Monitoring Feature Announcement](https://uptimerobot.com/blog/new-feature-heartbeat-monitoring/)

### Integrations
- [Integrations & Notifications](https://uptimerobot.com/integrations/)
- [Slack Integration](https://uptimerobot.com/integrations/slack/)
- [Web Hook Alert Contacts](https://uptimerobot.com/blog/web-hook-alert-contacts-new-feature/)
- [Slack Webhook Setup Guide](https://help.uptimerobot.com/en/articles/11650878-how-to-add-slack-webhook-alerts-in-uptimerobot-step-by-step)

### Status Pages
- [FREE Status Page](https://uptimerobot.com/status-page/)
- [Public Status Pages 2.0 Announcement](https://uptimerobot.com/blog/new-status-pages/)
- [Status Pages Guide](https://uptimerobot.com/knowledge-hub/devops/status-pages-guide/)

### API Updates
- [Introducing the UptimeRobot v3 API](https://uptimerobot.com/blog/introducing-the-uptimerobot-v3-api/)
- [Uptime Robot Gets An API](https://uptimerobot.com/blog/uptime-robot-gets-an-api/)

### Additional Resources
- [Frequently Asked Questions](https://uptimerobot.com/faq/)
- [Monitoring for Developers](https://uptimerobot.com/monitoring-for-developers/)
