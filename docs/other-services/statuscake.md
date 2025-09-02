# StatusCake - Complete Analysis

## Overview

StatusCake is a UK-based website monitoring platform that provides uptime, page speed, SSL, domain expiration, and server monitoring. Founded in 2010, it has grown to over 120,000 users. It competes primarily in the mid-market segment, positioned between budget options (UptimeRobot) and enterprise solutions (Pingdom).

**Website**: https://www.statuscake.com

**Founded**: 2010, Cambridge, UK

**Owner**: Accel-KKR (acquired 2021)

**Users**: 120,000+

**API URL**: `https://api.statuscake.com/v1`

## Key Features

### Monitor Types

1. **Uptime Monitoring** - HTTP, HEAD, TCP, DNS, SMTP, SSH, PING
2. **Push/Heartbeat Monitoring** - Passive check-in monitoring (all plans including free)
3. **Page Speed Monitoring** - Full page load analysis with global locations
4. **SSL Monitoring** - Certificate expiration and validity checks
5. **Domain Monitoring** - Domain expiration via WHOIS + DNS record changes
6. **Server Monitoring** - CPU, RAM, disk usage (Linux agent, paid plans)

### Monitoring Configuration

**Check intervals by plan**:

| Plan | Uptime | Page Speed | SSL | Domain |
|------|--------|-----------|-----|--------|
| Free | 5 min | 24 hours | 24 hours | 7 days |
| Superior | 1 min | 15 min | 30 min | 7 days |
| Business | 30 sec | 5 min | 10 min | 5 days |
| Enterprise | Constant | Custom | Custom | Custom |

### Probe Locations

- **43 monitoring locations** across **30 countries**
- **200+ monitoring servers** total
- Minimum 4 servers per region for redundancy
- Major concentrations: US (40+), UK/London (30+), Germany/Frankfurt (18+)
- IPv4 and IPv6 supported
- Live IP lists for firewall whitelisting

### Notification Channels (14+)

- Email (default)
- Slack
- Microsoft Teams
- Discord
- Mattermost
- PagerDuty
- Opsgenie
- VictorOps/Splunk On-Call
- Datadog
- Telegram
- Pushover
- Pushbullet
- Webhooks
- Phone calls & SMS (75 free SMS credits/month on free plan)

### Status Pages

- Public-facing with 7-day uptime history
- Customizable branding (logo, colors, CSS)
- Layout options (single/double column)
- Announcements section
- Password protection option
- Social media integration

### Additional Features

- **Maintenance windows**: Recurring (daily, weekly, monthly) - paid plans
- **Contact groups**: Alert routing
- **Sub-accounts**: Team management
- **Check tags**: Organization
- **Reporting**: Dashboards and reports (paid plans)
- **White-label reports**: Business plan+
- **Audit logs**: Business plan+

## Pricing

### Plans (EUR-based, as of March 2026)

| Plan | Monthly | Annual/mo | Uptime Monitors | Page Speed | SSL | Domain | Server |
|------|---------|-----------|-----------------|-----------|-----|--------|--------|
| **Free** | €0 | €0 | 10 | 1 | 1 | 1 | 0 |
| **Superior** | €19.99 | €16.66 | 100 | 15 | 50 | 50 | 3 |
| **Business** | €69.99 | €58.33 | 300 | 30 | 100 | 120 | 10 |
| **Enterprise** | Custom | Custom | Unlimited | Custom | Custom | Custom | Custom |

### Free Tier Details
- 10 uptime monitors (5-min intervals)
- 1 page speed monitor (24-hour intervals)
- 1 domain monitor (7-day intervals)
- 1 SSL monitor (24-hour intervals)
- 0 server monitors
- Email alerts + 75 free SMS credits/month
- 15+ integrations included
- Push/heartbeat monitoring included

## Technology Stack

### Known Infrastructure
- **Cloud**: AWS (Amazon Web Services), Amazon S3
- **CDN/Security**: Cloudflare
- **Frontend**: JavaScript, jQuery, Bootstrap
- **Monitoring**: Datadog (for their own infrastructure)
- **Servers**: 200+ distributed monitoring servers
- Backend language not publicly disclosed

## API

### Design
- **Architecture**: RESTful with conventional HTTP semantics
- **Authentication**: Bearer token (`Authorization: Bearer <token>`)
- **Format**: JSON
- **CORS**: Supported

### Key Endpoints
```bash
# Uptime checks
POST /v1/uptime          # Create
GET /v1/uptime           # List
GET /v1/uptime/{id}      # Get
PUT /v1/uptime/{id}      # Update
DELETE /v1/uptime/{id}   # Delete

# Heartbeat checks
POST /v1/heartbeat
GET /v1/heartbeat
GET /v1/heartbeat/{id}

# SSL monitors
POST /v1/ssl
GET /v1/ssl

# Page speed
POST /v1/pagespeed
GET /v1/pagespeed

# Contact groups
GET /v1/contact-groups
POST /v1/contact-groups

# Maintenance windows
GET /v1/maintenance-windows
POST /v1/maintenance-windows
```

### Developer Tools
- **Official SDKs**: Available
- **Terraform provider**: Available
- **Pulumi provider**: Official provider
- **Documentation**: Developer portal at developers.statuscake.com

## Strengths

### Features
1. ✅ **Broad monitor types**: HTTP, TCP, DNS, SMTP, SSH, PING, Push, SSL, Domain, Page Speed, Server
2. ✅ **Push/heartbeat on free tier**: Most competitors gate this behind paid plans
3. ✅ **75 free SMS credits/month**: Unusually generous for free tier
4. ✅ **SSL + Domain monitoring**: Built-in certificate and domain expiration tracking
5. ✅ **Page speed monitoring**: Full page load analysis
6. ✅ **Server monitoring**: CPU/RAM/disk (paid plans)
7. ✅ **43 locations, 200+ servers**: Strong geographic coverage
8. ✅ **Maintenance windows**: With recurrence support
9. ✅ **30-second checks**: On Business plan

### Platform
10. ✅ **14+ notification channels**: Good integration coverage
11. ✅ **Status pages**: Customizable with branding
12. ✅ **REST API**: Modern v1 API with SDKs
13. ✅ **Terraform/Pulumi**: Infrastructure as code support
14. ✅ **Password-protected status pages**: Useful for internal dashboards
15. ✅ **120,000+ users**: Established platform

## Weaknesses

### User Experience
1. ❌ **Dated UI**: Multiple reviewers describe the interface as outdated
2. ❌ **Perception of stagnation**: Limited innovation since Accel-KKR acquisition
3. ❌ **Linux-only server monitoring**: No Windows agent

### Missing Features
4. ❌ **No RUM**: No real user monitoring
5. ❌ **No transaction monitoring**: No browser-based user flow testing
6. ❌ **No incident management**: Requires external tools (PagerDuty, etc.)
7. ❌ **No on-call scheduling**: No rotation management
8. ❌ **No self-hosted option**: SaaS only
9. ❌ **No open source**: Proprietary platform

### Platform
10. ❌ **EUR-only pricing**: May confuse non-EU customers
11. ❌ **Support concerns**: Not 24/7 live chat
12. ❌ **No advanced API checks**: No multistep or scripted checks

## Comparison with SolidPing

### Similarities
- Both support HTTP, TCP, ICMP/Ping, DNS monitoring
- Both support heartbeat/push monitoring
- Both have SSL monitoring (SolidPing planned)
- Both have domain expiration monitoring
- Both have status pages
- Both have REST APIs

### StatusCake Advantages
1. **Page speed monitoring**: Full page load analysis (SolidPing doesn't have this)
2. **Server monitoring**: CPU/RAM/disk agent (SolidPing doesn't have this)
3. **43 locations, 200+ servers**: More monitoring locations
4. **14+ notification channels**: vs SolidPing's 3 (Slack, Email, Webhooks)
5. **SMTP/SSH protocol support**: Additional protocol coverage
6. **Maintenance windows**: With recurrence support
7. **Mature platform**: 15+ years, 120,000 users
8. **75 free SMS/month**: SMS alerting on free tier
9. **Terraform/Pulumi providers**: IaC ecosystem

### SolidPing Advantages
1. **Self-hosted**: Full control, no vendor lock-in
2. **Open source potential**: Transparency, community contributions
3. **Multi-tenancy**: Organization-scoped with RBAC (admin/user/viewer)
4. **Incident management**: Built-in escalation, relapse detection
5. **Distributed workers**: Deploy your own monitoring locations
6. **PostgreSQL-native**: Enterprise database, direct access
7. **Modern UI**: Reactive dash0 frontend vs StatusCake's dated UI
8. **API-first design**: OpenAPI spec, clean REST API
9. **No recurring costs**: Self-hosted = free unlimited
10. **Privacy-first**: Data stays on your infrastructure
11. **Keyword/regex matching**: Advanced HTTP response validation

### What SolidPing Should Learn

**From StatusCake**:
1. Add maintenance windows with recurrence support
2. Implement SSL certificate monitoring (type already defined)
3. Consider page speed monitoring as a future feature
4. Add more notification channels (Discord, Telegram, PagerDuty)
5. SMTP/SSH protocol support
6. Terraform provider for check management
7. SMS alerting integration

## Use Cases

### Best For
- **Small businesses**: Good free tier with SMS, broad monitoring
- **Agencies**: Multi-site monitoring at reasonable cost
- **DevOps teams**: Multi-protocol monitoring with API
- **Budget-conscious**: More features than UptimeRobot for similar price
- **European companies**: EUR pricing, UK-based company

### Not Ideal For
- Teams needing self-hosted (SaaS only)
- Users wanting modern UI/UX
- Enterprise needing RUM or transaction monitoring
- Teams needing incident management built-in
- Privacy-sensitive deployments

## Competitive Positioning

### vs UptimeRobot

| Aspect | StatusCake | UptimeRobot |
|--------|------------|-------------|
| Free monitors | 10 | 50 |
| Free interval | 5 min | 5 min |
| Monitor types | HTTP, TCP, DNS, SMTP, SSH, PING, Push | HTTP, Ping, Port, Keyword, Heartbeat |
| SSL monitoring | ✅ Built-in | ✅ |
| Domain monitoring | ✅ Built-in | ✅ |
| Page speed | ✅ | ❌ |
| Server monitoring | ✅ | ❌ |
| Free SMS | 75/month | Limited |

**StatusCake wins on**: Feature breadth (more monitor types, page speed, server monitoring)
**UptimeRobot wins on**: Free tier size (50 vs 10), simpler UI

### vs Pingdom

| Aspect | StatusCake | Pingdom |
|--------|------------|--------|
| Free tier | ✅ (10 monitors) | ❌ (trial only) |
| Entry price | ~€17/mo | ~$15/mo |
| RUM | ❌ | ✅ |
| Transaction monitoring | ❌ | ✅ |
| Protocol variety | More | Fewer |

**StatusCake wins on**: Free tier, protocol variety, lower price
**Pingdom wins on**: RUM, transaction monitoring, brand recognition

### vs BetterStack

| Aspect | StatusCake | BetterStack |
|--------|------------|-------------|
| Free monitors | 10 (5-min) | 10 (3-min) |
| Incident management | ❌ | ✅ Built-in |
| Status pages | Basic | Modern |
| Logging/APM | ❌ | ✅ Integrated |
| UI quality | Dated | Modern |

**StatusCake wins on**: More monitor types, longer track record, page speed
**BetterStack wins on**: Modern UI, incident management, integrated logging

## Sources

### Official
- [StatusCake Website](https://www.statuscake.com)
- [StatusCake Pricing](https://www.statuscake.com/pricing/)
- [StatusCake Features](https://www.statuscake.com/features/)
- [StatusCake Locations](https://www.statuscake.com/locations/)
- [StatusCake Integrations](https://www.statuscake.com/integrations/)
- [StatusCake API](https://developers.statuscake.com/api/)
- [StatusCake Status Pages](https://www.statuscake.com/status-page/)

### Reviews
- [G2 StatusCake Reviews](https://www.g2.com/products/statuscake-com/reviews)
- [TechRadar Review](https://www.techradar.com/pro/software-services/statuscake-website-monitoring-review)
- [PeerSpot Pros and Cons](https://www.peerspot.com/products/statuscake-com-pros-and-cons)

**Last Updated**: 2026-03-22
