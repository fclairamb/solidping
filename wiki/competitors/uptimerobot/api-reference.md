# UptimeRobot — Complete API Reference

## Endpoint Summary

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

## Monitor Types Reference

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

## Monitor Status Values

| Status | Code (v2) | Description |
|--------|-----------|-------------|
| Paused | 0 | Monitor is paused |
| Not checked yet | 1 | New monitor, first check pending |
| Up | 2 | Monitor is operational |
| Seems down | 8 | First failure detected |
| Down | 9 | Confirmed down (multiple checks) |

## Alert Contact Types

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

## HTTP Methods Supported

- GET - Retrieve resource
- HEAD - Check resource existence
- POST - Submit data
- PUT - Replace resource
- PATCH - Partial update
- DELETE - Remove resource
- OPTIONS - Check allowed methods

## Common Query Parameters

| Parameter | Endpoints | Type | Description |
|-----------|-----------|------|-------------|
| `limit` | List endpoints | integer | Results per page (default: 50, max: 50) |
| `cursor` | List endpoints | string | Pagination cursor |
| `search` | GET /monitors | string | Filter by name/URL |
| `type` | GET /monitors | string | Filter by monitor type |
| `status` | GET /monitors | string | Filter by status |

## HTTP Status Codes

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

## Rate Limit Response Headers

| Header | Description | Example |
|--------|-------------|---------|
| X-RateLimit-Limit | Total requests allowed | 600 |
| X-RateLimit-Remaining | Requests remaining in window | 587 |
| X-RateLimit-Reset | Unix timestamp when limit resets | 1640995200 |
| Retry-After | Seconds to wait before retry | 60 |
