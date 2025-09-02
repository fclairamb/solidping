# BetterStack Uptime - Complete Analysis

## Overview

BetterStack Uptime (formerly Better Uptime) is a comprehensive uptime monitoring and incident management platform that combines monitoring, alerting, on-call management, and status pages into a unified service. The platform positions itself as a cost-effective alternative to using multiple tools like PagerDuty + Pingdom + Statuspage.io.

**Base API URL**: `https://uptime.betterstack.com/api/v2/` (v3 for incidents)

**API Specification**: Follows JSON:API specification

## Key Features

### Monitoring Capabilities

BetterStack supports a wide range of monitoring types:

- **HTTP/HTTPS monitoring** - Website uptime with 30-second check intervals
- **API monitoring** - Includes screenshots and error logs
- **Playwright transaction monitoring** - Real Chrome browser-based testing for complex user flows
- **SSL certificate monitoring** - Track certificate expiration
- **Domain expiration monitoring**
- **Network protocols** - DNS, SMTP, IMAP, POP3, generic TCP/UDP
- **Ping monitoring**
- **Heartbeat/Cron monitoring** - For scheduled jobs and serverless workers

### Advanced Diagnostic Features

- **Traceroute & MTR** - Automatic network path analysis for timeouts
- **Second-by-second error timelines** - Visual documentation of failures
- **Response time tracking** - Monitor performance degradation
- **Screenshot capture** - Visual record of errors for web monitors

### Incident Management

- **Automatic incident merging** - Prevents alert spam from mass outages
- **Flexible escalation rules** - Based on time, team availability, and incident origin
- **Slack-based incident response** - Manage incidents directly from Slack
- **AI-native platform** - Intelligent incident handling

### Status Pages

- **Custom branded domains** - status.yourdomain.com
- **Public status communication** - Keep customers informed
- **Multiple status page resources** - Group related services
- **Status reports** - Historical incident reporting

### Notification System

Multiple notification channels with "all-you-can-alert" pricing:
- **Voice calls** (unlimited)
- **SMS**
- **Email**
- **Slack**
- **Microsoft Teams**
- **Push notifications**

### Integrations

BetterStack integrates with major monitoring and cloud platforms:
- Datadog, New Relic, Grafana, Prometheus, Zabbix
- Azure, AWS, Google Cloud
- Infrastructure-as-code via Terraform

## API Architecture

### Authentication

BetterStack uses **Bearer token authentication**. Two token types are available:

1. **Global API tokens** - Valid across all teams for comprehensive Better Stack management
2. **Uptime API tokens** - Team-scoped tokens for managing Uptime resources specifically

**Header format**:
```
Authorization: Bearer $TOKEN
```

### Token Management

**Creating Global API Token**:
1. Navigate to Better Stack → API tokens
2. Access the Global API tokens section
3. Copy existing or create new tokens

**Creating Uptime API Token**:
1. Go to Better Stack → API tokens → Team-based tokens
2. Select your team
3. Create or copy a token from the Uptime API tokens section

### API Standards

- Follows JSON:API specification
- RESTful conventions
- Pagination support on list endpoints
- Standard HTTP methods (GET, POST, PATCH, DELETE)

## Core API Endpoints

### Monitors

#### Monitor Types

BetterStack supports the following monitor types via the `monitor_type` parameter:

- **status** - Check for 2XX HTTP status code
- **expected_status_code** - Check for specific HTTP status codes
- **keyword** - Verify specific text presence in response
- **keyword_absence** - Verify specific text is NOT in response
- **ping** - ICMP ping monitoring
- **tcp** - TCP port connectivity
- **udp** - UDP port connectivity
- **smtp** - SMTP server monitoring
- **pop** - POP3 server monitoring
- **imap** - IMAP server monitoring
- **dns** - DNS query monitoring
- **playwright** - Browser-based scenario testing

#### List Monitors

**Endpoint**: `GET https://uptime.betterstack.com/api/v2/monitors`

**Query Parameters**:
- `url` - Filter monitors by their URL property
- `pronounceable_name` - Filter monitors by their pronounceable name

**Example**:
```bash
# List all monitors
curl --request GET \
  --url https://uptime.betterstack.com/api/v2/monitors \
  --header "Authorization: Bearer $TOKEN"

# Filter by URL
curl --request GET \
  --url 'https://uptime.betterstack.com/api/v2/monitors?url=https://google.com' \
  --header "Authorization: Bearer $TOKEN"
```

#### Get Single Monitor

**Endpoint**: `GET https://uptime.betterstack.com/api/v2/monitors/{monitor_id}`

Returns complete monitor configuration and current status.

#### Create Monitor

**Endpoint**: `POST https://uptime.betterstack.com/api/v2/monitors`

**Required Parameters**:
- `monitor_type` - Type of monitoring (see Monitor Types above)
- `url` - Website or host address to monitor
- `pronounceable_name` - Human-readable monitor identifier

**Key Optional Parameters**:

*Alert Configuration*:
- `email` (boolean) - Email notifications
- `sms` (boolean) - SMS notifications
- `call` (boolean) - Voice call notifications
- `critical_alert` (boolean) - Critical-level push notifications
- `policy_id` (integer) - Escalation policy ID
- `expiration_policy_id` (integer) - SSL/domain expiration escalation policy

*Monitoring Behavior*:
- `check_frequency` (integer) - Seconds between checks (default: 30)
- `request_timeout` (integer) - Timeout in seconds (2-60 for HTTP; 500-5000ms for server/port)
- `confirmation_period` (integer) - Seconds to wait before creating incident
- `recovery_period` (integer) - Seconds monitor must stay up to auto-resolve
- `verify_ssl` (boolean) - Enable SSL certificate validation
- `follow_redirects` (boolean) - Follow HTTP redirects

*HTTP Configuration*:
- `http_method` (string) - GET, HEAD, POST, PUT, PATCH
- `request_headers` (array) - Custom headers with `name` and `value` properties
- `request_body` (string) - Payload for POST/PUT/PATCH
- `expected_status_codes` (array) - Acceptable HTTP response codes

*Content Verification*:
- `required_keyword` (string) - Content to verify (keyword/absence monitors) or DNS query domain

*Protocol-Specific*:
- `port` (string) - Required for TCP/UDP/SMTP/POP/IMAP monitors
- `regions` (array) - Check locations: `us`, `eu`, `as`, `au`

*Expiration Monitoring*:
- `ssl_expiration` (integer) - Days advance notice for certificate expiration (null, 1-60)
- `domain_expiration` (integer) - Days advance notice for domain expiration (null, 1-60)

*Maintenance Windows*:
- `maintenance_days` (array) - Days: `mon`, `tue`, `wed`, `thu`, `fri`, `sat`, `sun`
- `maintenance_from` (string) - Window start time (HH:MM:SS)
- `maintenance_to` (string) - Window end time (HH:MM:SS)
- `maintenance_timezone` (string) - Timezone (defaults to UTC)

*Playwright-Specific*:
- `playwright_script` (string) - JavaScript scenario source code
- `environment_variables` (object) - Key-value pairs for scenario

*Organization*:
- `monitor_group_id` (integer) - Parent group identifier
- `team_wait` (integer) - Seconds before escalating to team
- `team_name` (string) - Required when using global API tokens

**Example**:
```bash
curl --request POST \
  --url https://uptime.betterstack.com/api/v2/monitors \
  --header "Authorization: Bearer $TOKEN" \
  --header 'Content-Type: application/json' \
  --data '{
    "monitor_type": "status",
    "url": "https://api.example.com/health",
    "pronounceable_name": "API Health Check",
    "email": true,
    "sms": false,
    "call": true,
    "check_frequency": 60,
    "verify_ssl": true,
    "regions": ["us", "eu"]
  }'
```

**Response**: Returns HTTP 201 with complete monitor object including assigned ID.

#### Update Monitor

**Endpoint**: `PATCH https://uptime.betterstack.com/api/v2/monitors/{monitor_id}`

Send only the parameters you wish to change:
```bash
curl --request PATCH \
  --url https://uptime.betterstack.com/api/v2/monitors/225493 \
  --header "Authorization: Bearer $TOKEN" \
  --header 'Content-Type: application/json' \
  --data '{ "check_frequency": 60 }'
```

#### Delete Monitor

**Endpoint**: `DELETE https://uptime.betterstack.com/api/v2/monitors/{monitor_id}`

Permanently deletes an existing monitor.

#### Monitor Response Times

**Endpoint**: `GET https://uptime.betterstack.com/api/v2/monitors/{monitor_id}/response-times`

Returns historical response time data for performance analysis.

#### Monitor Availability Summary

**Endpoint**: `GET https://uptime.betterstack.com/api/v2/monitors/{monitor_id}/sla`

**Query Parameters**:
- `from` (optional) - Start date in YYYY-MM-DD format
- `to` (optional) - End date in YYYY-MM-DD format

When dates are omitted, returns availability since monitor creation.

**Response Metrics**:
```json
{
  "availability": "99.98%",
  "total_downtime": 1234,
  "number_of_incidents": 5,
  "longest_incident": 600,
  "average_incident": 247
}
```

All time values are in seconds.

**Error Handling**: Returns 400 if dates are invalid (e.g., start date in future).

#### Monitor Status Values

The `status` field can have these values:
- `up` - Monitor is healthy
- `down` - Monitor is failing
- `validating` - Checking monitor status
- `paused` - Monitoring is paused
- `pending` - Just created, awaiting first check
- `maintenance` - In maintenance window

### Monitor Groups

Monitor groups allow you to organize related monitors and apply bulk operations.

#### List Monitor Groups

**Endpoint**: `GET https://uptime.betterstack.com/api/v2/monitor-groups`

Returns all monitor groups with pagination support.

**Example**:
```bash
curl --request GET \
  --url https://uptime.betterstack.com/api/v2/monitor-groups \
  --header "Authorization: Bearer $TOKEN"
```

#### Create Monitor Group

**Endpoint**: `POST https://uptime.betterstack.com/api/v2/monitor-groups`

**Required Parameters**:
- `name` (string) - Monitor group name displayed in dashboard

**Optional Parameters**:
- `paused` (boolean) - Pause monitoring for all monitors in group
- `sort_index` (integer) - Determines display order
- `team_name` (string) - Required when using global API tokens

**Example**:
```bash
curl --request POST \
  --url https://uptime.betterstack.com/api/v2/monitor-groups \
  --header "Authorization: Bearer $TOKEN" \
  --header 'Content-Type: application/json' \
  --data '{"name": "Backend services"}'
```

**Response**: Returns HTTP 201 with created group object.

#### Monitor Group Response Fields

- `id` - Unique identifier (string representation of number)
- `type` - Always "monitor_group"
- `name` - Group name visible in dashboard
- `sort_index` - Sorting order (can be null)
- `paused` - Whether monitoring is paused for group
- `team_name` - Associated team
- `created_at` - Creation timestamp (ISO 8601)
- `updated_at` - Last modification timestamp (ISO 8601)

### Heartbeats

Heartbeats monitor periodic tasks (cron jobs, scheduled functions) by expecting regular pings to a unique URL.

#### List Heartbeats

**Endpoint**: `GET https://uptime.betterstack.com/api/v2/heartbeats`

Returns all heartbeats with pagination support.

**Response structure**:
```json
{
  "data": [
    {
      "id": "...",
      "type": "heartbeat",
      "attributes": {
        "url": "...",
        "name": "...",
        "period": 86400,
        "grace": 300,
        "call": true,
        "sms": true,
        "email": true,
        "push": true,
        "team_wait": 0,
        "heartbeat_group_id": null,
        "team_name": "...",
        "sort_index": 0,
        "maintenance_from": null,
        "maintenance_to": null,
        "maintenance_timezone": null,
        "maintenance_days": null,
        "paused_at": null,
        "created_at": "...",
        "updated_at": "...",
        "status": "up"
      }
    }
  ],
  "pagination": {
    "first": "...",
    "last": "...",
    "prev": null,
    "next": "..."
  }
}
```

#### Heartbeat Status Values

- `paused` - The heartbeat was paused
- `pending` - Just created, waiting for first ping
- `up` - Received request on time
- `down` - Did not receive request on time

#### Get Single Heartbeat

**Endpoint**: `GET https://uptime.betterstack.com/api/v2/heartbeats/{heartbeat_id}`

Returns complete heartbeat configuration and current status.

**Example**:
```bash
curl --request GET \
  --url https://uptime.betterstack.com/api/v2/heartbeats/12345 \
  --header "Authorization: Bearer $TOKEN"
```

#### Create Heartbeat

**Endpoint**: `POST https://uptime.betterstack.com/api/v2/heartbeats`

**Required Parameters**:
- `name` (string) - Service name for the heartbeat

**Core Configuration**:
- `period` (integer) - Expected frequency in seconds (minimum: 30)
- `grace` (integer) - Acceptable fluctuation tolerance in seconds (recommended: ~20% of period)

**Notification Settings**:
- `call` (boolean) - Phone notifications
- `sms` (boolean) - Text messages
- `email` (boolean) - Email alerts
- `push` (boolean) - Standard push notifications
- `critical_alert` (boolean) - High-priority notifications (bypass device mute)

**Advanced Options**:
- `team_wait` (integer) - Seconds before escalating to entire team
- `heartbeat_group_id` (integer) - Add to heartbeat group
- `policy_id` (integer) - Set escalation policy
- `sort_index` (integer) - Position within group
- `team_name` (string) - Required when using global API tokens

**Maintenance Windows**:
- `paused` (boolean) - Temporarily disable monitoring
- `maintenance_days` (array) - Days: `['mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun']`
- `maintenance_from` (string) - Time window start (e.g., '01:00:00')
- `maintenance_to` (string) - Time window end (e.g., '05:00:00')
- `maintenance_timezone` (string) - Timezone (defaults to UTC)

**Example**:
```bash
curl --request POST \
  --url https://uptime.betterstack.com/api/v2/heartbeats \
  --header "Authorization: Bearer $TOKEN" \
  --header 'Content-Type: application/json' \
  --data '{
    "name": "Database backup",
    "period": 86400,
    "grace": 1800,
    "email": true,
    "call": true
  }'
```

**Response**: Returns HTTP 201 with heartbeat object including unique heartbeat URL and token.

#### Get Heartbeat Availability

**Endpoint**: `GET https://uptime.betterstack.com/api/v2/heartbeats/{heartbeat_id}/availability`

Returns availability metrics similar to monitor availability endpoint.

#### Send Heartbeat Ping

**Endpoint**: `GET/POST https://uptime.betterstack.com/api/v1/heartbeat/{HEARTBEAT_TOKEN}`

**Note**: This is a v1 endpoint (different from management API which is v2).

Send a request to this URL from your cron job/script to report successful execution.

**Example**:
```bash
#!/bin/bash
# Your backup script
/usr/local/bin/backup.sh

# Report success
curl "https://uptime.betterstack.com/api/v1/heartbeat/abc123def456"
```

#### Report Heartbeat Failure

**Endpoint**: `GET/POST https://uptime.betterstack.com/api/v1/heartbeat/{HEARTBEAT_TOKEN}/fail`

Report an explicit failure with optional exit codes and output data for diagnostics.

**Example**:
```bash
#!/bin/bash
if ! /usr/local/bin/backup.sh; then
  curl "https://uptime.betterstack.com/api/v1/heartbeat/abc123def456/fail"
  exit 1
fi

curl "https://uptime.betterstack.com/api/v1/heartbeat/abc123def456"
```

### Incidents

**List all incidents**
```
GET https://uptime.betterstack.com/api/v3/incidents
```

Features:
- Supports pagination (default: 10 per page, max: 50)
- Filter incidents by monitor
- Automatic incident merging for related failures

### Status Pages

**List all status pages**
```
GET https://uptime.betterstack.com/api/v2/status-pages
```

**List status page reports**
```
GET https://uptime.betterstack.com/api/v2/status-pages/{status_page_id}/reports
```

**Create status update**
```
POST https://uptime.betterstack.com/api/v2/status-updates
```

**Delete status update**
```
DELETE https://uptime.betterstack.com/api/v2/status-updates/{status_update_id}
```

### Pagination

All list endpoints support pagination with links included in responses:
```json
{
  "data": [...],
  "pagination": {
    "first": "https://uptime.betterstack.com/api/v2/monitors?page=1",
    "last": "https://uptime.betterstack.com/api/v2/monitors?page=5",
    "prev": null,
    "next": "https://uptime.betterstack.com/api/v2/monitors?page=2"
  }
}
```

## Heartbeat Monitoring In-Depth

Heartbeat monitoring is designed to track periodic tasks like cron jobs, scheduled backups, and serverless functions.

### How It Works

1. **Create a heartbeat monitor** with:
   - Name (e.g., "Daily database backup")
   - Expected frequency (e.g., every 24 hours)
   - Grace period (additional time before alerting)
   - Escalation settings

2. **Receive unique URL** with secret token

3. **Add ping to your script**:
   ```bash
   #!/bin/bash

   # Your backup script
   /usr/local/bin/backup.sh

   # Report success
   curl "https://uptime.betterstack.com/api/v1/heartbeat/<TOKEN>"
   ```

4. **Handle failures**:
   ```bash
   #!/bin/bash

   if ! /usr/local/bin/backup.sh; then
     # Report failure with details
     curl "https://uptime.betterstack.com/api/v1/heartbeat/<TOKEN>/fail"
     exit 1
   fi

   # Report success
   curl "https://uptime.betterstack.com/api/v1/heartbeat/<TOKEN>"
   ```

5. **Monitor remains in 'Pending' state** until first heartbeat is received

6. **Alert triggers** if no heartbeat received within frequency + grace period

### Use Cases

- **Cron jobs** - Database backups, data exports, cleanup tasks
- **Serverless functions** - Scheduled Lambda/Cloud Functions
- **CI/CD pipelines** - Deployment verification
- **Data sync operations** - ETL processes
- **Health checks** - Application lifecycle monitoring

## Pricing Model

BetterStack uses fixed pricing instead of per-monitor or per-alert charges:

**Free Plan**:
- 10 monitors
- 10 heartbeats
- Status page included
- 3-minute check frequency

**Paid Plans**:
- Example: $269/month includes 60 monitors, 6 team members, 2,000 status page subscribers
- Unlimited alerts ("all-you-can-alert" pricing)
- 30-second check intervals
- No per-incident or per-SMS charges

**Cost Comparison**:
BetterStack positions itself as replacing ~$673/month in combined services:
- PagerDuty (incident management)
- Pingdom (monitoring)
- Statuspage.io (status communication)

## Comparison with SolidPing

### Similarities

Both platforms offer:
- HTTP/HTTPS uptime monitoring
- Heartbeat/cron monitoring
- Multiple notification channels
- REST APIs for programmatic access
- Status tracking and reporting
- API-first design philosophy

### BetterStack Advantages

1. **Broader protocol support** - DNS, SMTP, IMAP, POP3, etc.
2. **Playwright monitoring** - Real browser testing
3. **Built-in incident management** - On-call scheduling, escalations
4. **Status pages** - Public status communication
5. **Advanced diagnostics** - Traceroute, MTR, screenshots
6. **Mature ecosystem** - Terraform provider, extensive integrations
7. **Team collaboration** - Multi-user workflows, Slack integration
8. **AI-native features** - Intelligent incident handling

### SolidPing Advantages

1. **Self-hosted option** - Full data ownership and control
2. **Simpler architecture** - Focused feature set, easier to understand
3. **Open source potential** - Can be customized and extended
4. **Direct database access** - More flexibility for custom queries
5. **No vendor lock-in** - Own your monitoring infrastructure
6. **Privacy-first** - No third-party data sharing
7. **Cost control** - No recurring subscription fees for self-hosted
8. **PostgreSQL-native** - Standard database, familiar tooling

### Feature Gaps in SolidPing

Areas where SolidPing could consider expansion:

1. **Protocol monitoring** - Add DNS, TCP, UDP, SMTP support
2. **Advanced diagnostics** - Traceroute/MTR for failed checks
3. **Browser monitoring** - Playwright/Puppeteer integration
4. **Status pages** - Public status communication API
5. **Incident workflows** - Escalation rules, on-call scheduling
6. **Response time analytics** - Performance degradation alerts
7. **SSL monitoring** - Certificate expiration tracking
8. **Maintenance windows** - Scheduled downtime handling

## Technical Considerations

### Rate Limits

- **Not publicly documented** - BetterStack doesn't publish specific rate limits
- Assume reasonable usage limits apply
- Contact support for high-volume scenarios

### API Versioning

- Currently at v2 for most endpoints
- v3 for incidents API
- v1 for heartbeat ping endpoint (different from management API)
- Breaking changes likely handled through version increments

### Data Retention

- Not explicitly documented in public API docs
- Likely varies by plan tier
- Historical data available through availability/SLA endpoints

### Response Times

- Monitors can check as frequently as every 30 seconds
- API response times not documented
- Pagination limits (max 50 items per page) suggest performance optimization

### Security

- Bearer token authentication (standard)
- Team-scoped tokens for access control
- HTTPS required for all API calls
- Heartbeat tokens are secret and should be protected

## Limitations and Gaps

1. **Rate limits undocumented** - Could be problematic for high-volume integrations
2. **No webhook callbacks** - API is pull-based, not push-based for events
3. **Limited filtering** - List endpoints don't show extensive query parameters
4. **Closed source** - No ability to self-host or inspect code
5. **SaaS only** - No on-premises option for regulated industries
6. **Pricing scales with features** - Not with usage, which may be inefficient for simple needs
7. **Team-centric** - May be over-featured for individual developers
8. **No multi-region monitoring shown** - Unclear if checks run from multiple locations

## API Design Patterns

BetterStack follows several design patterns that could inform SolidPing development:

### Good Patterns to Adopt

1. **JSON:API compliance** - Standardized response format
2. **Nested resource endpoints** - `/monitors/{id}/response-times`
3. **Partial updates** - PATCH with only changed fields
4. **Pagination links** - Include first/last/prev/next in responses
5. **Dual token types** - Global vs scoped access
6. **Status enumeration** - Clear, documented status values
7. **Separate ping vs management APIs** - v1 heartbeat endpoint vs v2 management

### Patterns to Consider

1. **Pronounceable names** - User-friendly identifiers alongside technical ones
2. **Grace periods** - Flexibility in alerting thresholds
3. **Team wait times** - Escalation delays
4. **Maintenance windows** - Built-in scheduled downtime support

### Patterns to Avoid

1. **Version inconsistency** - v1/v2/v3 mixing could be confusing
2. **Undocumented limits** - Rate limits should be transparent
3. **Limited query parameters** - More filtering would improve API usability

## Integration Examples

### Monitoring a Web Service

```bash
# Create a monitor
curl --request POST \
  --url https://uptime.betterstack.com/api/v2/monitors \
  --header "Authorization: Bearer $TOKEN" \
  --header 'Content-Type: application/json' \
  --data '{
    "monitor_type": "status",
    "url": "https://api.example.com/health",
    "pronounceable_name": "API Health Check",
    "email": true,
    "sms": false,
    "call": true,
    "check_frequency": 60
  }'
```

### Monitoring a Cron Job

```bash
# 1. Create heartbeat via UI or API
# 2. Add to your cron script:

#!/bin/bash
HEARTBEAT_URL="https://uptime.betterstack.com/api/v1/heartbeat/abc123"

if /usr/local/bin/my-backup.sh; then
  curl "$HEARTBEAT_URL"
else
  curl "$HEARTBEAT_URL/fail"
  exit 1
fi
```

### Checking System Status

```bash
# List all monitors and their status
curl --request GET \
  --url https://uptime.betterstack.com/api/v2/monitors \
  --header "Authorization: Bearer $TOKEN" \
  | jq '.data[] | {name: .attributes.pronounceable_name, status: .attributes.status}'
```

### Getting Availability Report

```bash
# Get monitor availability/SLA
curl --request GET \
  --url https://uptime.betterstack.com/api/v2/monitors/225493/sla \
  --header "Authorization: Bearer $TOKEN"
```

## Complete API Reference

### Endpoint Summary

Quick reference of all API endpoints:

| Endpoint | Method | Description |
|----------|--------|-------------|
| **Monitors** |
| `/api/v2/monitors` | GET | List all monitors (supports filtering) |
| `/api/v2/monitors` | POST | Create a new monitor |
| `/api/v2/monitors/{id}` | GET | Get single monitor details |
| `/api/v2/monitors/{id}` | PATCH | Update existing monitor |
| `/api/v2/monitors/{id}` | DELETE | Delete monitor |
| `/api/v2/monitors/{id}/response-times` | GET | Get monitor response times |
| `/api/v2/monitors/{id}/sla` | GET | Get monitor availability summary |
| **Monitor Groups** |
| `/api/v2/monitor-groups` | GET | List all monitor groups |
| `/api/v2/monitor-groups` | POST | Create a new monitor group |
| `/api/v2/monitor-groups/{id}` | GET | Get single monitor group |
| `/api/v2/monitor-groups/{id}` | PATCH | Update monitor group |
| `/api/v2/monitor-groups/{id}` | DELETE | Delete monitor group |
| **Heartbeats** |
| `/api/v2/heartbeats` | GET | List all heartbeats |
| `/api/v2/heartbeats` | POST | Create a new heartbeat |
| `/api/v2/heartbeats/{id}` | GET | Get single heartbeat |
| `/api/v2/heartbeats/{id}` | PATCH | Update heartbeat |
| `/api/v2/heartbeats/{id}` | DELETE | Delete heartbeat |
| `/api/v2/heartbeats/{id}/availability` | GET | Get heartbeat availability |
| `/api/v1/heartbeat/{token}` | GET/POST | Send heartbeat ping (success) |
| `/api/v1/heartbeat/{token}/fail` | GET/POST | Report heartbeat failure |
| **Incidents** |
| `/api/v3/incidents` | GET | List all incidents (supports filtering) |
| **Status Pages** |
| `/api/v2/status-pages` | GET | List all status pages |
| `/api/v2/status-pages/{id}/reports` | GET | List status page reports |
| `/api/v2/status-updates` | POST | Create status update |
| `/api/v2/status-updates/{id}` | DELETE | Delete status update |

### Monitor Response Parameters

Complete list of all fields returned in monitor API responses:

| Field | Type | Description |
|-------|------|-------------|
| `id` | String | Monitor identifier (string representation of number) |
| `type` | String | Always returns `monitor` |
| `url` | String | Website or host target for monitoring |
| `pronounceable_name` | String | Human-readable name for voice notifications |
| `monitor_type` | String | Type: `status`, `expected_status_code`, `keyword`, `keyword_absence`, `ping`, `tcp`, `udp`, `smtp`, `pop`, `imap`, `dns`, `playwright` |
| `monitor_group_id` | Integer | Parent group identifier |
| `check_frequency` | Integer | Interval in seconds between checks |
| `request_timeout` | Integer | Timeout in seconds (ms for Server/Port monitors) |
| `confirmation_period` | Integer | Seconds before creating incident after failure |
| `recovery_period` | Integer | Seconds monitor must stay up to auto-resolve |
| `http_method` | String | Request method: `GET`, `HEAD`, `POST`, `PUT`, `PATCH` |
| `request_headers` | Array | Custom headers with `name` and `value` properties |
| `request_body` | String | Payload for POST/PUT/PATCH; required for DNS monitors |
| `expected_status_codes` | Array | Acceptable HTTP response codes |
| `verify_ssl` | Boolean | Enable SSL certificate validation |
| `follow_redirects` | Boolean | Follow HTTP redirects |
| `required_keyword` | String | Content to verify or DNS query domain |
| `port` | String | Port for TCP/UDP/SMTP/POP/IMAP monitors |
| `regions` | Array | Check locations: `us`, `eu`, `as`, `au` |
| `call` | Boolean | Voice notification to on-call |
| `sms` | Boolean | Text message to on-call |
| `email` | Boolean | Email notification to on-call |
| `push` | Boolean | Push notification to on-call |
| `critical_alert` | Boolean | Critical-level push notification |
| `team_wait` | Integer | Seconds before escalating to team |
| `policy_id` | Integer | Escalation policy identifier |
| `expiration_policy_id` | Integer | SSL/domain expiration escalation policy |
| `ssl_expiration` | Integer | Days advance notice for cert expiration (null, 1-60) |
| `domain_expiration` | Integer | Days advance notice for domain expiration (null, 1-60) |
| `maintenance_days` | Array | Days with maintenance: `mon`-`sun` |
| `maintenance_from` | String | Window start time (HH:MM:SS) |
| `maintenance_to` | String | Window end time (HH:MM:SS) |
| `maintenance_timezone` | String | Timezone (defaults to UTC) |
| `playwright_script` | String | JavaScript scenario source code |
| `environment_variables` | Object | Key-value pairs for Playwright scenarios |
| `status` | String | State: `up`, `down`, `validating`, `paused`, `pending`, `maintenance` |
| `last_checked_at` | String (ISO 8601) | Timestamp of most recent check |
| `paused_at` | String (ISO 8601) | Pause timestamp (null if active) |
| `created_at` | String (ISO 8601) | Creation timestamp |
| `updated_at` | String (ISO 8601) | Last modification timestamp |
| `team_name` | String | Team ownership identifier |

### Heartbeat Response Parameters

| Field | Type | Description |
|-------|------|-------------|
| `id` | String | Heartbeat identifier |
| `type` | String | Always returns `heartbeat` |
| `url` | String | Heartbeat endpoint URL for pinging |
| `name` | String | Display name |
| `period` | Integer | Expected check interval in seconds |
| `grace` | Integer | Grace period in seconds before alerting |
| `call` | Boolean | Voice notification enabled |
| `sms` | Boolean | SMS notification enabled |
| `email` | Boolean | Email notification enabled |
| `push` | Boolean | Push notification enabled |
| `team_wait` | Integer | Team notification delay |
| `heartbeat_group_id` | Integer | Associated group (nullable) |
| `team_name` | String | Associated team |
| `sort_index` | Integer | Display ordering |
| `maintenance_from` | String | Window start time |
| `maintenance_to` | String | Window end time |
| `maintenance_timezone` | String | Timezone for maintenance |
| `maintenance_days` | Array | Days with maintenance windows |
| `paused_at` | String (ISO 8601) | Pause timestamp (null if active) |
| `created_at` | String (ISO 8601) | Creation timestamp |
| `updated_at` | String (ISO 8601) | Last modification timestamp |
| `status` | String | State: `paused`, `pending`, `up`, `down` |

### Monitor Group Response Parameters

| Field | Type | Description |
|-------|------|-------------|
| `id` | String | Group identifier (string representation of number) |
| `type` | String | Always returns `monitor_group` |
| `name` | String | Group name visible in dashboard |
| `sort_index` | Integer | Sorting order (can be null) |
| `paused` | Boolean | Whether monitoring is paused for group |
| `team_name` | String | Associated team |
| `created_at` | String (ISO 8601) | Creation timestamp |
| `updated_at` | String (ISO 8601) | Last modification timestamp |

### Availability/SLA Response

| Field | Type | Description |
|-------|------|-------------|
| `availability` | String | Percentage (e.g., "99.98%") |
| `total_downtime` | Integer | Total seconds of downtime |
| `number_of_incidents` | Integer | Count of incidents |
| `longest_incident` | Integer | Duration in seconds |
| `average_incident` | Integer | Average duration in seconds |

### Common Query Parameters

| Parameter | Applicable Endpoints | Description |
|-----------|---------------------|-------------|
| `url` | List Monitors | Filter monitors by URL property |
| `pronounceable_name` | List Monitors | Filter monitors by pronounceable name |
| `from` | Monitor/Heartbeat Availability | Start date (YYYY-MM-DD) |
| `to` | Monitor/Heartbeat Availability | End date (YYYY-MM-DD) |
| `page` | All list endpoints | Page number for pagination |

### HTTP Status Codes

| Code | Meaning | When Returned |
|------|---------|---------------|
| 200 | OK | Successful GET requests |
| 201 | Created | Successful POST (create) requests |
| 204 | No Content | Successful DELETE requests |
| 400 | Bad Request | Invalid parameters (e.g., invalid dates) |
| 401 | Unauthorized | Missing or invalid authentication token |
| 404 | Not Found | Resource doesn't exist |
| 422 | Unprocessable Entity | Validation errors |

## Conclusion

BetterStack Uptime is a mature, feature-rich monitoring platform with a well-designed API following modern standards. It excels at:

- **Comprehensive monitoring** across protocols and technologies
- **Incident management** with intelligent alerting and escalation
- **Developer experience** with clean API design and good documentation
- **Team collaboration** with multi-user support and integrations

However, it's a closed-source SaaS platform with subscription pricing that may be excessive for simple use cases.

For SolidPing, BetterStack serves as an excellent reference for:
- API design patterns and conventions
- Feature completeness in uptime monitoring
- Heartbeat monitoring implementation
- Incident management workflows

While SolidPing may not need all of BetterStack's features, understanding the competitive landscape helps identify where to focus development effort and which features provide the most value to users.

## Sources

### General Documentation
- [BetterStack Uptime API Documentation](https://betterstack.com/docs/uptime/api/)
- [Getting Started with Uptime API](https://betterstack.com/docs/uptime/api/getting-started-with-uptime-api/)
- [BetterStack Uptime Product Page](https://betterstack.com/uptime)

### Monitor API
- [List Monitors](https://betterstack.com/docs/uptime/api/list-all-existing-monitors/)
- [Get Single Monitor](https://betterstack.com/docs/uptime/api/get-a-single-monitor/)
- [Create Monitor](https://betterstack.com/docs/uptime/api/create-a-new-monitor/)
- [Update Monitor](https://betterstack.com/docs/uptime/api/update-an-existing-monitor/)
- [Delete Monitor](https://betterstack.com/docs/uptime/api/delete-an-existing-monitor/)
- [Monitor Response Times](https://betterstack.com/docs/uptime/api/get-monitors-response-times/)
- [Monitor Availability](https://betterstack.com/docs/uptime/api/get-a-monitors-availability-summary/)
- [Monitor Response Parameters](https://betterstack.com/docs/uptime/api/monitors-api-response-params/)

### Monitor Group API
- [List Monitor Groups](https://betterstack.com/docs/uptime/api/list-all-existing-monitor-groups/)
- [Create Monitor Group](https://betterstack.com/docs/uptime/api/create-a-new-monitor-group/)
- [Monitor Group Response Parameters](https://betterstack.com/docs/uptime/api/monitor-groups-api-response-params/)

### Heartbeat API
- [Cron and Heartbeat Monitor Guide](https://betterstack.com/docs/uptime/cron-and-heartbeat-monitor/)
- [List Heartbeats](https://betterstack.com/docs/uptime/api/list-all-existing-hearbeats/)
- [Get Single Heartbeat](https://betterstack.com/docs/uptime/api/get-a-single-hearbeat/)
- [Create Heartbeat](https://betterstack.com/docs/uptime/api/create-a-hearbeat/)
- [Heartbeat Availability](https://betterstack.com/docs/uptime/api/get-a-heartbeats-availability-summary/)

### Incidents & Status Pages
- [List Incidents](https://betterstack.com/docs/uptime/api/list-all-incidents/)
- [List Status Pages](https://betterstack.com/docs/uptime/api/list-all-existing-status-pages/)
- [Status Page Reports](https://betterstack.com/docs/uptime/api/list-existing-reports-on-a-status-page/)
