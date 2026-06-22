# Pingdom — API

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
