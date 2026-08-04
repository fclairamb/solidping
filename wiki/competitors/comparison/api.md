# Uptime Monitoring Comparison — APIs

## Authentication

| Service | Method | Token Types | Security |
|---------|--------|-------------|----------|
| **BetterStack** | Bearer token (JWT) | Global, Team-scoped | ⭐⭐⭐⭐⭐ |
| **UptimeRobot** | Bearer token (JWT) | Account, Monitor-specific, Read-only | ⭐⭐⭐⭐⭐ |
| **Pingdom** | Bearer token | Read-only, Read/Write | ⭐⭐⭐⭐ |

**Winner**: Tie (UptimeRobot for granularity, all modern and secure)

## API Design

| Feature | BetterStack | UptimeRobot | Pingdom |
|---------|-------------|-------------|---------|
| **Architecture** | RESTful (JSON:API) | RESTful | RESTful |
| **HTTP Methods** | GET, POST, PATCH, DELETE | GET, POST, PATCH, DELETE | GET, POST, PUT, DELETE |
| **Versioning** | v2, v3 (incidents) | v3 (v2 legacy) | 3.1 (2.1 legacy) |
| **Pagination** | Links (first/last/prev/next) | Cursor-based | Offset-based |
| **Response Format** | JSON only | JSON only | JSON only |
| **Documentation** | Good | Good | JavaScript-required (poor) |
| **CORS Support** | Yes | Yes (v3) | Yes |

**Best API Design**: UptimeRobot (modern v3, cursor pagination, CORS)

**Most Consistent**: BetterStack (JSON:API spec compliance)

**Worst Documentation**: Pingdom (requires JavaScript to view)

## Rate Limits

| Service | Free Plan | Paid Plans | Headers | Documentation |
|---------|-----------|------------|---------|---------------|
| **BetterStack** | Not specified | Not specified | ❌ | ❌ Poor |
| **UptimeRobot** | 10 req/min | monitor_limit × 2 (max 5,000) | ✅ X-RateLimit-* | ✅ Excellent |
| **Pingdom** | Not specified | Not specified | ✅ (mentioned) | ❌ Poor |

**Winner**: UptimeRobot (clear limits, transparent headers)

## Available Endpoints

| Endpoint Category | BetterStack | UptimeRobot | Pingdom |
|-------------------|-------------|-------------|---------|
| **Monitors** | ✅ Full CRUD | ✅ Full CRUD | ✅ Full CRUD |
| **Monitor Groups** | ✅ | ❌ | ❌ |
| **Heartbeats** | ✅ Full CRUD | ✅ Full CRUD | ❌ |
| **Alert Contacts** | ❌ (integrations) | ✅ Full CRUD | ✅ Full CRUD |
| **Integrations** | ✅ | ✅ | ❌ (limited) |
| **Incidents** | ✅ (v3) | ✅ | ✅ (actions) |
| **Status Pages** | ✅ | ✅ (PSPs) | ❌ |
| **Maintenance Windows** | ✅ | ✅ (Pro) | ✅ |
| **Response Times** | ✅ | ❌ | ✅ |
| **Availability/SLA** | ✅ | ✅ | ✅ (summary) |
| **Probe Servers** | ❌ | ❌ | ✅ |
| **User Profile** | ❌ | ✅ (/user/me) | ❌ |

**Most Complete API**: BetterStack (monitor groups, incidents v3)

**Best Developer Experience**: UptimeRobot (user profile, clear docs)
