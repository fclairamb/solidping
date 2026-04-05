# Pingdom - Complete Analysis

## Overview

Pingdom (now owned by SolarWinds) is an established website monitoring service that provides uptime, transaction, page speed, and real user monitoring (RUM). Originally founded in 2007, Pingdom has become a well-known name in the monitoring space, though it has faced increasing competition from newer, more affordable alternatives.

**Base API URL**: `https://api.pingdom.com/api/3.1/`

**API Specification**: RESTful HTTP-based API with Bearer token authentication

**Current Owner**: SolarWinds (acquired in 2014)

## Key Features

### Monitoring Capabilities

Pingdom supports multiple monitoring types across three main product lines:

**Synthetic Monitoring** (Uptime Checks):
- **HTTP/HTTPS monitoring** - Website uptime with GET/POST requests
- **HTTP Custom** - Server-side script integration for custom monitoring
- **Ping (ICMP) monitoring** - Server connectivity checks
- **TCP monitoring** - Port connectivity with custom string verification
- **UDP monitoring** - UDP port connectivity checks
- **DNS monitoring** - DNS server functionality verification
- **SMTP monitoring** - Mail server monitoring (default 220 response code)
- **POP3/POP3S monitoring** - POP3 mail server checks
- **IMAP/IMAPS monitoring** - IMAP mail server verification

**Transaction Monitoring**:
- Real Chrome browser-based testing
- User interaction simulation
- Shopping cart flows
- Login processes
- Registration workflows
- Search functionality testing
- URL hijacking detection
- Complex multi-step scenarios

**Page Speed Monitoring**:
- Load time tracking
- Element-by-element analysis
- Performance bottleneck identification
- Size and load time metrics
- Waterfall charts
- Optimization recommendations

**Real User Monitoring (RUM)**:
- Real-time user experience data
- Geographic location insights
- Browser-based performance
- Device-specific metrics
- Actual visitor interaction tracking
- JavaScript snippet integration

### Advanced Features

- **Multi-location monitoring** - 100+ global probe servers
- **Regional selection** - 5 probe regions (North America, Europe, Asia Pacific, etc.)
- **Minute-by-minute checks** - Up to 60-second intervals (1-minute minimum)
- **30-second timeout** - For HTTP(S), HTTP Custom, DNS, and TCP checks
- **Custom headers** - HTTP request customization
- **POST data support** - Form data in HTTP checks
- **String verification** - Search for specific HTML strings
- **Basic/custom authentication** - HTTP authentication support

### Notification System

Pingdom offers multiple alerting channels:
- **Email** - Standard email notifications
- **SMS** - Text message alerts
- **Push notifications** - iOS and Android mobile apps
- **Slack** - Channel notifications
- **PagerDuty** - Incident management integration
- **Webhooks** - Custom HTTP callbacks
- **VictorOps** (now Splunk On-Call)
- **OpsGenie**
- **HipChat** (legacy)
- **Microsoft Teams** - Via webhooks

### Status Pages

- **Public status pages** - Customer-facing incident communication
- **Custom branding** - Brand customization options
- **Subscriber notifications** - Email notifications for subscribers
- **Incident history** - Historical incident reporting

### Platform Features

- **Mobile apps** - Native iOS and Android applications
- **30-day free trial** - No credit card required
- **100+ probe locations** - Global monitoring network
- **API access** - All plans include API access
- **Reporting** - Customizable performance reports
- **Integrations** - Third-party tool connectivity

## API Architecture

### API Versions

**Current: API 3.1** - Introduced Bearer token authentication

**Previous: API 2.1** (Legacy) - Used Basic Authentication (username/password)

**Key Changes in 3.1**:
- Bearer token authentication instead of username/password
- Improved security (no credential exposure)
- Easier integration (API keys instead of credentials)
- Same endpoints and functionality as 2.1
- Backward compatible migration path

### Authentication

Pingdom API 3.1 uses **Bearer token authentication**.

**Obtaining API Token**:
1. Log in to My Pingdom (dashboard)
2. Navigate to Integrations → The Pingdom API (left menu)
3. Click "API Tokens" or create new token
4. Enter a name for your token
5. Choose access level:
   - **Read access** - GET endpoints only
   - **Read/Write access** - Full CRUD operations
6. Copy the generated token

**Authentication Header**:
```
Authorization: Bearer YOUR_API_TOKEN
```

**Legacy Authentication** (API 2.1):
```
Authorization: Basic BASE64(username:password)
```

### Rate Limiting

Pingdom implements rate limiting to prevent abuse:

**Rate Limit Details**:
- Limits vary by account type and subscription tier
- Exact limits not publicly documented
- Rate limit headers provided in responses
- HTTP 429 status when limit exceeded

**Best Practices**:
- Cache API responses when possible
- Implement exponential backoff
- Monitor rate limit headers
- Contact support for higher limits if needed

### API Standards

- RESTful architecture
- JSON request/response format
- Standard HTTP methods (GET, POST, PUT, DELETE)
- Standard HTTP status codes
- OpenAPI 3.0 Specification documentation
- HTTPS required for all requests

## Core API Endpoints (API 3.1)

### Checks

Checks are the core monitoring units in Pingdom.

#### List Checks

**Endpoint**: `GET /checks`

**Query Parameters**:
- `limit` (integer) - Number of results (default: 25000, max: 25000)
- `offset` (integer) - Offset for pagination
- `include_tags` (boolean) - Include tag information
- `tags` (string) - Filter by tags (comma-separated)

**Example**:
```bash
curl --request GET \
  --url 'https://api.pingdom.com/api/3.1/checks' \
  --header 'Authorization: Bearer YOUR_API_TOKEN'
```

**Response** includes:
- Check ID
- Name
- Type (http, httpcustom, tcp, ping, dns, udp, smtp, pop3, imap)
- Hostname/URL
- Status (up, down, paused, unknown)
- Last test time
- Resolution (check interval in minutes)
- Tags

#### Get Single Check

**Endpoint**: `GET /checks/{checkid}`

Returns detailed information about a specific check.

**Example**:
```bash
curl --request GET \
  --url 'https://api.pingdom.com/api/3.1/checks/12345' \
  --header 'Authorization: Bearer YOUR_API_TOKEN'
```

#### Create Check

**Endpoint**: `POST /checks`

**Common Parameters** (all check types):
- `name` (string, required) - Check name
- `host` (string, required) - Hostname or IP address
- `type` (string, required) - Check type
- `resolution` (integer) - Check interval in minutes (1, 5, 15, 30, 60)
- `paused` (boolean) - Start paused (default: false)
- `tags` (string) - Comma-separated tags
- `probe_filters` (string) - Region: NA, EU, APAC, LATAM, region:World
- `teamids` (string) - Comma-separated team IDs

**HTTP/HTTPS Check Parameters**:
- `type` = "http" or "https"
- `url` (string) - URL path (e.g., "/health")
- `encryption` (boolean) - Use SSL/TLS
- `requestheaders` (object) - Custom HTTP headers
- `postdata` (string) - POST data
- `shouldcontain` (string) - String that must be present in response
- `shouldnotcontain` (string) - String that must NOT be present
- `auth` (string) - Username:password for HTTP auth
- `verify_certificate` (boolean) - Verify SSL certificate (default: true)
- `ssl_down_days_before` (integer) - Days before SSL expiry to alert

**TCP Check Parameters**:
- `type` = "tcp"
- `port` (integer, required) - Port number
- `stringtosend` (string) - String to send to server
- `stringtoexpect` (string) - Expected response string

**Ping Check Parameters**:
- `type` = "ping"
- Uses 5 ICMP packets, considers down if 3 fail
- Each packet has 5-second timeout

**DNS Check Parameters**:
- `type` = "dns"
- `expectedip` (string) - Expected IP address
- `nameserver` (string) - DNS server to query

**SMTP Check Parameters**:
- `type` = "smtp"
- `port` (integer) - SMTP port (default: 25)
- `encryption` (boolean) - Use TLS
- `stringtoexpect` (string) - Expected response (default: "220")

**UDP Check Parameters**:
- `type` = "udp"
- `port` (integer, required)
- `stringtosend` (string)
- `stringtoexpect` (string)

**Example** (HTTP Check):
```bash
curl --request POST \
  --url 'https://api.pingdom.com/api/3.1/checks' \
  --header 'Authorization: Bearer YOUR_API_TOKEN' \
  --header 'Content-Type: application/json' \
  --data '{
    "name": "Production API",
    "host": "api.example.com",
    "type": "http",
    "url": "/health",
    "resolution": 1,
    "shouldcontain": "\"status\":\"ok\"",
    "requestheaders": {
      "X-API-Key": "secret123"
    }
  }'
```

**Example** (TCP Check):
```bash
curl --request POST \
  --url 'https://api.pingdom.com/api/3.1/checks' \
  --header 'Authorization: Bearer YOUR_API_TOKEN' \
  --header 'Content-Type: application/json' \
  --data '{
    "name": "PostgreSQL Database",
    "host": "db.example.com",
    "type": "tcp",
    "port": 5432,
    "resolution": 5
  }'
```

#### Update Check

**Endpoint**: `PUT /checks/{checkid}`

Send only the parameters you wish to change. Same parameters as create.

**Example**:
```bash
curl --request PUT \
  --url 'https://api.pingdom.com/api/3.1/checks/12345' \
  --header 'Authorization: Bearer YOUR_API_TOKEN' \
  --header 'Content-Type: application/json' \
  --data '{
    "resolution": 5,
    "paused": false
  }'
```

#### Delete Check

**Endpoint**: `DELETE /checks/{checkid}`

Permanently deletes a check.

**Example**:
```bash
curl --request DELETE \
  --url 'https://api.pingdom.com/api/3.1/checks/12345' \
  --header 'Authorization: Bearer YOUR_API_TOKEN'
```

#### Pause/Unpause Multiple Checks

**Endpoint**: `PUT /checks/{checkid1},{checkid2},{checkid3}`

Bulk pause or unpause operation.

**Example**:
```bash
curl --request PUT \
  --url 'https://api.pingdom.com/api/3.1/checks/12345,67890,11111' \
  --header 'Authorization: Bearer YOUR_API_TOKEN' \
  --header 'Content-Type: application/json' \
  --data '{"paused": true}'
```

### Check Results

#### Get Check Results

**Endpoint**: `GET /results/{checkid}`

Returns historical check results.

**Query Parameters**:
- `from` (integer) - Unix timestamp start time
- `to` (integer) - Unix timestamp end time
- `limit` (integer) - Number of results (max: 1000)
- `offset` (integer) - Pagination offset
- `status` (string) - Filter by status (up, down, unconfirmed_down, unknown)
- `includeanalysis` (boolean) - Include root cause analysis

**Example**:
```bash
curl --request GET \
  --url 'https://api.pingdom.com/api/3.1/results/12345?limit=100' \
  --header 'Authorization: Bearer YOUR_API_TOKEN'
```

### Summary & Analysis

#### Get Summary Average

**Endpoint**: `GET /summary.average/{checkid}`

Returns average performance metrics.

**Query Parameters**:
- `from` (integer) - Unix timestamp
- `to` (integer) - Unix timestamp
- `includeuptime` (boolean) - Include uptime percentage
- `bycountry` (boolean) - Break down by country
- `byprobe` (boolean) - Break down by probe server

#### Get Summary Outage

**Endpoint**: `GET /summary.outage/{checkid}`

Returns outage summary for date range.

#### Get Summary Performance

**Endpoint**: `GET /summary.performance/{checkid}`

Returns performance summary with response times.

### Actions (Historical Data)

**Endpoint**: `GET /actions`

Returns list of alerts sent (emails, SMS, etc.).

**Query Parameters**:
- `from` (integer) - Unix timestamp
- `to` (integer) - Unix timestamp
- `limit` (integer) - Number of results
- `offset` (integer) - Pagination offset
- `checkids` (string) - Filter by check IDs (comma-separated)
- `contactids` (string) - Filter by contact IDs
- `status` (string) - Filter by status (sent, delivered, error, not_delivered, no_credits)
- `via` (string) - Filter by channel (email, sms, twitter, iphone, android)

### Contacts

#### List Contacts

**Endpoint**: `GET /alerting/contacts`

Returns all alert contacts.

#### Create Contact

**Endpoint**: `POST /alerting/contacts`

**Parameters**:
- `name` (string, required) - Contact name
- `email` (string) - Email address
- `phone` (string) - Phone number
- `sms_provider` (string) - SMS provider
- `paused` (boolean) - Start paused

**Example**:
```bash
curl --request POST \
  --url 'https://api.pingdom.com/api/3.1/alerting/contacts' \
  --header 'Authorization: Bearer YOUR_API_TOKEN' \
  --header 'Content-Type: application/json' \
  --data '{
    "name": "DevOps Team",
    "email": "devops@example.com"
  }'
```

### Maintenance Windows

#### List Maintenance Windows

**Endpoint**: `GET /maintenance`

#### Create Maintenance Window

**Endpoint**: `POST /maintenance`

**Parameters**:
- `description` (string, required) - Description
- `from` (integer, required) - Unix timestamp start
- `to` (integer, required) - Unix timestamp end
- `effectiveto` (integer) - When maintenance ends (can be after 'to')
- `uptimeids` (string) - Comma-separated check IDs
- `tmsids` (string) - Transaction check IDs

### Probe Servers

#### List Probe Servers

**Endpoint**: `GET /probes`

Returns all probe servers with their locations and IP addresses.

**Example**:
```bash
curl --request GET \
  --url 'https://api.pingdom.com/api/3.1/probes' \
  --header 'Authorization: Bearer YOUR_API_TOKEN'
```

**Response includes**:
- Probe ID
- Country
- City
- Region
- Name
- Hostname
- IPv4 address
- IPv6 address

### Reference Data

#### Get Server Time

**Endpoint**: `GET /reference`

Returns current server time (Unix timestamp).

## Check Types In-Depth

### HTTP/HTTPS Monitoring

**How it works**: Performs HTTP GET or POST requests to specified URL, checking for:
- Successful HTTP status code (200-299 by default)
- Optional: Presence of specific string in response
- Optional: Absence of specific string in response
- SSL certificate validity (for HTTPS)

**Configuration**:
- URL path and query parameters
- HTTP method (GET, POST)
- Custom headers
- POST data
- Basic/Digest authentication
- SSL certificate verification
- String matching (shouldcontain/shouldnotcontain)

**Limitations**:
- Only fetches HTML code and headers
- Does not load dynamic content (JavaScript, images, CSS)
- 30-second timeout
- No JavaScript execution (use Transaction monitoring for that)

**Use cases**:
- Website uptime monitoring
- API health checks
- Server availability
- Content verification

### Transaction Monitoring

**How it works**: Uses real Chrome browser to simulate user interactions:
- Executes JavaScript
- Loads all page resources
- Simulates clicks, form fills, navigation
- Verifies successful completion of multi-step flows

**Configuration**:
- Recorded browser scenarios
- Custom scripts
- Expected outcomes
- Timeout thresholds
- Alert conditions

**Use cases**:
- Shopping cart checkout flows
- User registration processes
- Login workflows
- Search functionality
- Multi-step form completion
- SPA (Single Page Application) testing

**Limitations**:
- Requires Pro plan (not included in basic tiers)
- More expensive than uptime checks
- Longer execution time
- Complex setup for advanced scenarios

### Ping Monitoring

**How it works**: Sends 5 ICMP packets to target host:
- Each packet has 5-second timeout
- Considers host down if 3 of 5 packets fail
- Measures packet loss and latency

**Use cases**:
- Server connectivity
- Network device monitoring
- Basic host availability

**Limitations**:
- Not suitable for websites (server may respond while website is down)
- ICMP may be blocked by firewalls
- No application-layer verification

### TCP/UDP Port Monitoring

**How it works**:
- Connects to specified port on target host
- Optionally sends string and expects response
- Verifies port is accepting connections

**TCP Configuration**:
- Hostname/IP
- Port number
- String to send (optional)
- Expected response string (optional)

**Use cases**:
- Database monitoring (PostgreSQL, MySQL, MongoDB)
- Custom service monitoring
- Application server health
- FTP server monitoring
- Any TCP-based service

**UDP Configuration**:
- Similar to TCP but using UDP protocol
- Less reliable (connectionless)

### DNS Monitoring

**How it works**:
- Queries specified DNS server
- Verifies expected IP address is returned
- Checks DNS resolution time

**Configuration**:
- DNS server to query
- Domain to resolve
- Expected IP address

**Use cases**:
- DNS server functionality
- DNS record verification
- Authoritative nameserver monitoring
- DNS propagation checking

**Limitations**:
- One check per DNS server
- Does not check recursive DNS
- Limited to simple A record queries

### Email Server Monitoring (SMTP/POP3/IMAP)

**How it works**: Connects to mail server and verifies response code

**SMTP**:
- Connects to port 25 (or custom)
- Expects 220 response code (customizable)
- Does not send actual emails
- Can use TLS encryption

**POP3/POP3S**:
- Connects to POP3 port (110 or 995)
- Verifies server responds correctly
- Supports SSL/TLS

**IMAP/IMAPS**:
- Connects to IMAP port (143 or 993)
- Verifies server availability
- Supports SSL/TLS

**Use cases**:
- Mail server uptime
- Email infrastructure monitoring
- MX server availability

## Monitoring Locations

### Global Probe Network

Pingdom operates **100+ probe servers** distributed globally across five main regions:

**Regions**:
1. **North America** - USA, Canada
2. **Europe** - UK, Germany, France, Netherlands, etc.
3. **Asia Pacific** - Singapore, Japan, Australia, etc.
4. **Latin America** - Brazil, etc.
5. **World** - All regions combined

**Region Selection**:
- Choose specific region for checks
- Default: North America and Europe
- Up to 10 probe servers per check from selected region
- Different probes test from varied locations within region

**Benefits**:
- Detect location-specific issues
- Monitor CDN performance
- Identify regional outages
- Verify geographic redundancy

**Access**:
- View probe servers: Synthetics → Probe Servers
- URL: https://my.pingdom.com/app/probes
- API endpoint: GET /probes

## Pricing Model

Pingdom offers two main product lines with multiple pricing tiers:

### Synthetic Monitoring Plans

**Starter** - $10/month:
- 10 uptime checks
- 1 advanced check (page speed or transaction)
- 50 SMS alerts
- 1-minute check intervals
- Email + SMS + push notifications
- Public status pages
- API access

**Advanced** - $32/month:
- 50 uptime checks
- 5 advanced checks
- 500 SMS alerts
- All Starter features
- Root cause analysis
- Multi-user access

**Professional** - $63/month:
- 150 uptime checks
- 15 advanced checks
- 1,500 SMS alerts
- All Advanced features
- Priority support

**Business** - $120/month:
- 300 uptime checks
- 30 advanced checks
- 3,000 SMS alerts
- All Professional features
- Advanced integrations

**Enterprise** - Custom pricing:
- Up to 30,000 uptime checks
- Custom advanced checks
- Custom SMS alerts
- Dedicated support
- Custom SLA

### Real User Monitoring (RUM) Plans

**Starter** - $10/month:
- 100,000 page views
- Real-time metrics
- Geographic breakdown
- Browser/device analytics

**Advanced** - $32/month:
- 1 million page views
- All Starter features
- Extended data retention

**Professional** - $63/month:
- 10 million page views
- All Advanced features
- Custom dashboards

**Business** - $120/month:
- 100 million page views
- All Professional features
- Priority support

**Enterprise** - Custom pricing:
- Up to 1 billion page views
- Custom features
- Dedicated support

### Pricing Insights

**Free Trial**: 30-day free trial, no credit card required

**Billing**: Monthly or annual (annual saves ~10-15%)

**SMS Costs**: Limited SMS included, additional SMS available

**Monitor Limits**: 22 tier options from 10 to 30,000 monitors

**Expensive compared to competitors**:
- UptimeRobot: 50 free monitors vs Pingdom's $10 for 10
- StatusCake: More affordable tiers
- BetterStack: 10 free monitors with better free tier

**Best value**: Enterprise for large deployments, but expensive for small teams

**Pain points**:
- No free tier (only trial)
- Quick cost escalation
- Limited free SMS alerts
- Transaction monitoring requires higher tiers

## Comparison with SolidPing

### Similarities

Both platforms offer:
- HTTP/HTTPS uptime monitoring
- TCP port monitoring
- Ping monitoring
- DNS monitoring
- Email server monitoring (SMTP, POP3, IMAP)
- REST APIs for programmatic access
- Alert management
- Status tracking and reporting
- Multiple notification channels

### Pingdom Advantages

1. **Established brand** - Since 2007, well-known in industry
2. **100+ global locations** - Extensive probe network
3. **Transaction monitoring** - Real Chrome browser testing
4. **Real User Monitoring** - Actual visitor analytics
5. **Page speed monitoring** - Detailed performance analysis
6. **Native mobile apps** - iOS and Android
7. **SolarWinds backing** - Enterprise support and resources
8. **Advanced reporting** - Comprehensive performance reports
9. **Multi-region selection** - Geographic redundancy
10. **Mature ecosystem** - Many integrations and third-party tools

### SolidPing Advantages

1. **Self-hosted option** - Full data ownership and control
2. **No vendor lock-in** - Own your monitoring infrastructure
3. **Cost control** - No recurring fees for self-hosted
4. **Open source potential** - Customizable and extensible
5. **Direct database access** - PostgreSQL for custom queries
6. **Privacy-first** - No third-party data sharing
7. **No monitor limits** - Unlimited monitors on self-hosted
8. **Simpler architecture** - Easier to understand
9. **API-first design** - Built for developers
10. **Better free tier potential** - Can match or beat UptimeRobot's 50 free monitors
11. **No false positives** - Control your own probe infrastructure
12. **Heartbeat monitoring** - Built-in cron job monitoring

### Feature Gaps in SolidPing

Areas where SolidPing could consider expansion to match Pingdom:

1. **Transaction monitoring** - Browser-based scenario testing
2. **Real User Monitoring** - JavaScript snippet for actual user data
3. **Page speed monitoring** - Detailed performance analysis
4. **Multi-location probes** - Geographic redundancy (for SaaS version)
5. **Mobile applications** - iOS and Android apps
6. **Advanced reporting** - Graphical reports and dashboards
7. **Root cause analysis** - Automated issue diagnosis
8. **SSL expiry alerts** - Certificate expiration tracking
9. **Custom headers** - More flexible HTTP configuration
10. **String matching** - shouldcontain/shouldnotcontain verification
11. **Maintenance windows** - Scheduled downtime handling
12. **Probe server API** - List available monitoring locations
13. **Advanced integrations** - Native Slack, PagerDuty, etc.

## Technical Considerations

### Rate Limiting

- **Not publicly documented** - Exact limits vary by plan
- **Monitor limits apply** - Account tier determines API usage
- **Best practices**:
  - Cache responses when possible
  - Implement exponential backoff
  - Monitor response headers for rate limit warnings
  - Contact support for higher limits

### API Versioning

**Current: 3.1**
- Bearer token authentication
- Active development
- Recommended for all integrations

**Legacy: 2.1**
- Basic authentication (username/password)
- Still supported
- No new features
- Security concerns (credential exposure)

**Migration**: Straightforward (change auth method, same endpoints)

### Data Retention

- **Check results**: Varies by plan (typically 30 days to 12+ months)
- **Transaction data**: Limited retention
- **RUM data**: Real-time with limited historical storage
- **Reports**: Can export historical data

### Response Times

- **Check intervals**: 1 minute minimum (60-second checks)
- **API response time**: Typically <500ms
- **Webhook delivery**: Near real-time
- **Status page updates**: Immediate

### Security

- **HTTPS required** - All API calls must use TLS
- **Bearer token auth** - Secure API key authentication
- **Token permissions** - Read-only vs read/write
- **IP whitelisting** - Not available
- **Two-factor authentication** - Available for dashboard login
- **SSL certificate verification** - Configurable per check

### Multi-Location Checking

- **5 regions available** - NA, EU, APAC, LATAM, World
- **10 probes per check** - From selected region
- **Geo-verification** - Multiple locations must confirm outage
- **False positive reduction** - Regional consensus
- **CDN monitoring** - Verify geographic distribution

## Limitations and Gaps

1. **No free tier** - Only 30-day trial (competitors offer free plans)
2. **Expensive pricing** - $10/month for just 10 monitors
3. **Limited SMS alerts** - Quota-based, additional costs
4. **False positives** - Users report frequent false alarms
5. **1-minute minimum interval** - Competitors offer 30-second checks
6. **Transaction monitoring cost** - Expensive for advanced features
7. **No heartbeat monitoring** - Missing cron job monitoring
8. **Complex pricing** - 22 different tier options
9. **Limited free RUM** - Page view quotas restrictive
10. **No self-hosted option** - SaaS-only, vendor lock-in
11. **API documentation** - JavaScript-required docs (accessibility issue)
12. **Limited customization** - Less flexible than open-source alternatives
13. **Rate limits unclear** - Not transparently documented
14. **SolarWinds ownership** - Concerns after SolarWinds security incident
15. **Alert fatigue** - False positives lead to mistrust

## API Design Patterns

### Good Patterns to Adopt

1. **Bearer token authentication** - Modern, secure approach
2. **OpenAPI documentation** - Industry standard
3. **RESTful design** - Standard HTTP methods
4. **Bulk operations** - Pause multiple checks at once
5. **Regional filtering** - probe_filters for location selection
6. **Tag-based filtering** - Organize and filter by tags
7. **Summary endpoints** - Aggregate data (average, outage, performance)
8. **Include flags** - Optional data inclusion (includeuptime, includeanalysis)
9. **Unix timestamps** - Standard time representation
10. **Probe server API** - List available monitoring locations

### Patterns to Consider

1. **Check types** - Comprehensive coverage (HTTP, TCP, UDP, SMTP, etc.)
2. **String verification** - shouldcontain/shouldnotcontain
3. **Custom headers** - HTTP request customization
4. **Maintenance windows** - Scheduled downtime support
5. **Alert actions API** - Historical alert delivery tracking
6. **Multiple check creation** - Bulk check operations
7. **Resolution parameter** - Configurable check intervals

### Patterns to Avoid

1. **No free tier** - Competitors offer generous free tiers
2. **Complex pricing** - Too many tier options (22 variations)
3. **Unclear rate limits** - Should be transparently documented
4. **JavaScript-required docs** - Accessibility and SEO issues
5. **Legacy auth support** - Security risk maintaining Basic auth
6. **Undocumented limits** - SMS quotas, API limits should be clear
7. **Expensive transaction monitoring** - Pricing barrier to entry

## Integration Examples

### Creating HTTP Monitor

```bash
curl --request POST \
  --url 'https://api.pingdom.com/api/3.1/checks' \
  --header 'Authorization: Bearer YOUR_API_TOKEN' \
  --header 'Content-Type: application/json' \
  --data '{
    "name": "API Health Check",
    "host": "api.example.com",
    "type": "http",
    "url": "/v1/health",
    "resolution": 1,
    "shouldcontain": "healthy",
    "requestheaders": {
      "X-API-Key": "secret123",
      "Accept": "application/json"
    }
  }'
```

### Creating TCP Port Monitor

```bash
curl --request POST \
  --url 'https://api.pingdom.com/api/3.1/checks' \
  --header 'Authorization: Bearer YOUR_API_TOKEN' \
  --header 'Content-Type: application/json' \
  --data '{
    "name": "PostgreSQL",
    "host": "db.example.com",
    "type": "tcp",
    "port": 5432,
    "resolution": 5,
    "stringtoexpect": "PostgreSQL"
  }'
```

### Creating DNS Monitor

```bash
curl --request POST \
  --url 'https://api.pingdom.com/api/3.1/checks' \
  --header 'Authorization: Bearer YOUR_API_TOKEN' \
  --header 'Content-Type: application/json' \
  --data '{
    "name": "DNS Check",
    "host": "example.com",
    "type": "dns",
    "expectedip": "93.184.216.34",
    "nameserver": "8.8.8.8"
  }'
```

### Creating SMTP Monitor

```bash
curl --request POST \
  --url 'https://api.pingdom.com/api/3.1/checks' \
  --header 'Authorization: Bearer YOUR_API_TOKEN' \
  --header 'Content-Type: application/json' \
  --data '{
    "name": "Mail Server",
    "host": "smtp.example.com",
    "type": "smtp",
    "port": 587,
    "encryption": true,
    "stringtoexpect": "220"
  }'
```

### Getting Check Results

```bash
# Get last 100 results for a check
curl --request GET \
  --url 'https://api.pingdom.com/api/3.1/results/12345?limit=100&includeanalysis=true' \
  --header 'Authorization: Bearer YOUR_API_TOKEN'
```

### Getting Uptime Summary

```bash
# Get average performance for last 7 days
FROM=$(date -d '7 days ago' +%s)
TO=$(date +%s)

curl --request GET \
  --url "https://api.pingdom.com/api/3.1/summary.average/12345?from=$FROM&to=$TO&includeuptime=true" \
  --header 'Authorization: Bearer YOUR_API_TOKEN'
```

### Pausing Multiple Checks

```bash
# Pause checks for maintenance
curl --request PUT \
  --url 'https://api.pingdom.com/api/3.1/checks/12345,67890,11111' \
  --header 'Authorization: Bearer YOUR_API_TOKEN' \
  --header 'Content-Type: application/json' \
  --data '{"paused": true}'
```

### Creating Maintenance Window

```bash
# Schedule maintenance for 2 hours
START=$(date -d 'tomorrow 2am' +%s)
END=$(date -d 'tomorrow 4am' +%s)

curl --request POST \
  --url 'https://api.pingdom.com/api/3.1/maintenance' \
  --header 'Authorization: Bearer YOUR_API_TOKEN' \
  --header 'Content-Type: application/json' \
  --data "{
    \"description\": \"Database migration\",
    \"from\": $START,
    \"to\": $END,
    \"uptimeids\": \"12345,67890\"
  }"
```

### Listing Probe Servers

```bash
curl --request GET \
  --url 'https://api.pingdom.com/api/3.1/probes' \
  --header 'Authorization: Bearer YOUR_API_TOKEN' \
  | jq '.probes[] | {name: .name, country: .country, city: .city, ip: .ip}'
```

## Complete API Reference

### Endpoint Summary

Quick reference of all API 3.1 endpoints:

| Endpoint | Method | Description |
|----------|--------|-------------|
| **Checks** |
| `/checks` | GET | List all checks |
| `/checks` | POST | Create new check |
| `/checks/{id}` | GET | Get single check |
| `/checks/{id}` | PUT | Update check |
| `/checks/{id}` | DELETE | Delete check |
| `/checks/{ids}` | PUT | Bulk pause/unpause checks |
| **Results** |
| `/results/{id}` | GET | Get check results |
| **Summary** |
| `/summary.average/{id}` | GET | Get average performance |
| `/summary.outage/{id}` | GET | Get outage summary |
| `/summary.performance/{id}` | GET | Get performance summary |
| **Actions** |
| `/actions` | GET | List alert actions (sent alerts) |
| **Contacts** |
| `/alerting/contacts` | GET | List alert contacts |
| `/alerting/contacts` | POST | Create alert contact |
| `/alerting/contacts/{id}` | PUT | Update alert contact |
| `/alerting/contacts/{id}` | DELETE | Delete alert contact |
| **Maintenance** |
| `/maintenance` | GET | List maintenance windows |
| `/maintenance` | POST | Create maintenance window |
| `/maintenance/{id}` | PUT | Update maintenance window |
| `/maintenance/{id}` | DELETE | Delete maintenance window |
| **Probes** |
| `/probes` | GET | List probe servers |
| **Reference** |
| `/reference` | GET | Get server time |

### Check Types Reference

| Type | String Value | Description | Key Parameters |
|------|--------------|-------------|----------------|
| HTTP | "http" | HTTP/HTTPS monitoring | url, requestheaders, postdata, shouldcontain |
| HTTP Custom | "httpcustom" | Custom server-side script | url, additionalurls, postdata |
| TCP | "tcp" | TCP port monitoring | port, stringtosend, stringtoexpect |
| Ping | "ping" | ICMP ping | (none) |
| DNS | "dns" | DNS resolution | expectedip, nameserver |
| UDP | "udp" | UDP port monitoring | port, stringtosend, stringtoexpect |
| SMTP | "smtp" | SMTP mail server | port, encryption, stringtoexpect |
| POP3 | "pop3" | POP3 mail server | port, encryption |
| IMAP | "imap" | IMAP mail server | port, encryption |

### Check Status Values

| Status | Description |
|--------|-------------|
| up | Check is passing |
| down | Check is failing |
| unconfirmed_down | First failure detected, awaiting confirmation |
| unknown | Status unknown (new check or error) |
| paused | Check is paused |

### Check Resolution (Intervals)

| Value | Interval |
|-------|----------|
| 1 | 1 minute |
| 5 | 5 minutes |
| 15 | 15 minutes |
| 30 | 30 minutes |
| 60 | 60 minutes (1 hour) |

### Probe Regions

| Region | Value | Coverage |
|--------|-------|----------|
| North America | "region:NA" | USA, Canada |
| Europe | "region:EU" | UK, Germany, France, Netherlands, etc. |
| Asia Pacific | "region:APAC" | Singapore, Japan, Australia, etc. |
| Latin America | "region:LATAM" | Brazil, etc. |
| World | "region:World" | All regions |

### HTTP Status Codes

| Code | Meaning | When Returned |
|------|---------|---------------|
| 200 | OK | Successful requests |
| 201 | Created | Successful check creation |
| 204 | No Content | Successful deletion |
| 400 | Bad Request | Invalid parameters |
| 401 | Unauthorized | Missing or invalid API token |
| 403 | Forbidden | Insufficient permissions |
| 404 | Not Found | Check/resource doesn't exist |
| 429 | Too Many Requests | Rate limit exceeded |
| 500 | Internal Server Error | Pingdom server error |

## Conclusion

Pingdom is a mature, feature-rich monitoring platform with comprehensive capabilities across uptime, transaction, page speed, and real user monitoring. It excels at:

- **Global monitoring** - 100+ probe locations worldwide
- **Enterprise features** - Transaction monitoring, RUM, advanced reporting
- **Comprehensive protocols** - HTTP, TCP, UDP, SMTP, DNS, IMAP, POP3
- **Established platform** - 17+ years in business, SolarWinds backing
- **Mobile experience** - Native iOS and Android apps

However, Pingdom has significant drawbacks:

- **Expensive** - $10/month for only 10 monitors (vs 50 free elsewhere)
- **No free tier** - Only 30-day trial
- **False positives** - Frequent user complaints about false alarms
- **Complex pricing** - 22 different tier options
- **Alert fatigue** - Unreliable alerts lead to mistrust
- **Vendor lock-in** - SaaS-only, no self-hosted option

For SolidPing, Pingdom serves as a reference for:
- **What to avoid** - Expensive pricing, no free tier, false positives
- **Enterprise features** - Transaction monitoring, RUM (if expanding to SaaS)
- **Multi-location** - Geographic redundancy approach
- **Protocol coverage** - Comprehensive check types
- **API design** - Bearer auth, bulk operations, summary endpoints

**Key Takeaway**: Pingdom's decline in popularity (users switching to UptimeRobot, StatusCake, BetterStack) demonstrates that **pricing, reliability, and simplicity** matter more than brand recognition. SolidPing's self-hosted, open-source approach addresses Pingdom's main weaknesses: cost, vendor lock-in, and lack of control.

## Sources

### General Documentation
- [Pingdom Website Monitoring Service](https://www.pingdom.com/)
- [SolarWinds Pingdom](https://www.solarwinds.com/pingdom)
- [Pingdom Pricing](https://www.pingdom.com/pricing/)

### API Documentation
- [Pingdom API](https://docs.pingdom.com/api/)
- [The Pingdom API (SolarWinds)](https://documentation.solarwinds.com/en/success_center/pingdom/content/topics/the-pingdom-api.htm)
- [API - Pingdom Resources](https://www.pingdom.com/api/)
- [Announcing Pingdom API 3.1](https://www.pingdom.com/blog/announcing-the-pingdom-api-3-1/)

### Feature Documentation
- [What is a check?](https://documentation.solarwinds.com/en/success_center/pingdom/content/topics/what-is-a-check-.htm)
- [Synthetic Monitoring](https://www.pingdom.com/synthetic-monitoring/)
- [Real User Monitoring (RUM)](https://www.pingdom.com/real-user-monitoring/)
- [Uptime Monitoring](https://www.pingdom.com/product/uptime-monitoring/)
- [API Monitoring](https://www.pingdom.com/solution/api-monitoring/)
- [Website Status Alerts](https://www.pingdom.com/product/alerting/)

### Monitoring Types
- [Mail Server Monitoring](https://www.pingdom.com/blog/new-pingdom-feature-mail-server-monitoring/)
- [DNS Monitoring](https://www.pingdom.com/blog/new-pingdom-feature-dns-monitoring/)
- [How to Delete Checks](https://documentation.solarwinds.com/en/success_center/pingdom/content/topics/how-do-i-delete-uptime-transactions-page-speed-checks.htm)

### Probe Servers
- [Select Probe Location Feature](https://www.pingdom.com/blog/new-feature-select-probe-location-use/)
- [How Pingdom Performs Uptime Checks](https://documentation.solarwinds.com/en/success_center/pingdom/content/topics/how-does-pingdom-perform-its-uptime-checks-and-choose-locations-to-test-from-.htm)
- [Pingdom Probe Servers IP Addresses](https://documentation.solarwinds.com/en/success_center/pingdom/content/topics/pingdom-probe-servers-ip-addresses.htm)

### Integrations
- [Set Up Alerting Settings](https://documentation.solarwinds.com/en/success_center/pingdom/content/gsg/set-up-alerting.htm)
- [Integrations with Support for Pingdom](https://documentation.solarwinds.com/en/success_center/pingdom/content/topics/integrations-with-support-for-pingdom.htm)
- [Webhooks](https://www.pingdom.com/resources/webhooks/)
- [Pingdom PagerDuty Integration](https://www.pagerduty.com/docs/guides/pingdom-integration-guide/)
- [Using Pingdom and Slack](https://medium.com/engineering-tyroo/using-pingdom-and-slack-for-real-time-monitoring-of-production-systems-950d9eb7417a)

### Comparisons & Reviews
- [Top 10 Pingdom Alternatives 2025](https://middleware.io/blog/pingdom-alternatives/)
- [Best Pingdom Alternatives](https://hyperping.com/blog/best-pingdom-alternatives)
- [7 Best Pingdom Alternatives (Better Stack)](https://betterstack.com/community/comparisons/pingdom-alternatives/)
- [UptimeRobot as Pingdom Alternative](https://uptimerobot.com/alternative-to-pingdom/)
- [Pingdom Review (TechRadar)](https://www.techradar.com/pro/pingdom-website-monitoring-review)
