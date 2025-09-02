# Healthchecks.io - Complete Analysis

## Overview

Healthchecks.io is a specialized cron job and background task monitoring service created by Peteris Caune. Unlike general uptime monitors, it uses a **passive/reverse monitoring** model: your jobs ping Healthchecks.io on a schedule, and it alerts you when pings stop arriving. It's the leading open-source solution for heartbeat/cron monitoring.

**Website**: https://healthchecks.io

**GitHub**: https://github.com/healthchecks/healthchecks

**License**: BSD 3-Clause

**Technology**: Python, Django

**Database**: PostgreSQL

**Current Version**: v4.1 (March 2026)

## Key Statistics

- **GitHub Stars**: ~9,900 (Mar 2026)
- **License**: BSD 3-Clause (fully open source, no feature restrictions)
- **Active Development**: Very active (2015-present)
- **Maintainer**: Primarily single-developer project (Peteris Caune)

## Key Features

### Core Monitoring Model

**Passive/Reverse Monitoring Only**:
- Services ping Healthchecks.io (not the other way around)
- No active probing (no HTTP, TCP, ICMP, DNS, SSL checks)
- Focused entirely on "did my job run?" monitoring
- Complementary to active uptime monitors like SolidPing

### Monitor Types

1. **Cron job monitoring** - Verify scheduled jobs execute on time
2. **Heartbeat monitoring** - Periodic check-in from services
3. **Background task monitoring** - Track async job completion
4. **Backup verification** - Confirm backup jobs complete
5. **Pipeline monitoring** - Track data pipeline runs

### Ping URL Patterns

Unique URL suffix system for job lifecycle tracking:
```
# Signal job started
GET/POST https://hc-ping.com/{uuid}/start

# Signal job completed successfully
GET/POST https://hc-ping.com/{uuid}

# Signal job failed
GET/POST https://hc-ping.com/{uuid}/fail

# Signal exit code
GET/POST https://hc-ping.com/{uuid}/{exit-code}

# Log output (POST body attached to ping)
POST https://hc-ping.com/{uuid} (with body)
```

### Notification System

**25+ notification channels**:
- Email
- Slack
- Microsoft Teams
- Discord
- Telegram
- PagerDuty
- Opsgenie
- VictorOps/Splunk On-Call
- Webhooks
- Pushover
- Pushbullet
- Signal
- Matrix
- Gotify
- Ntfy
- SMS (Twilio)
- Voice calls (Twilio)
- Zulip
- Spike.sh
- Line Notify
- Trello
- Mattermost
- And more

### Unique Features

- **Ping by email**: Send an email to trigger a ping (unique in the space)
- **Exit code tracking**: Report and alert on non-zero exit codes
- **Auto-provisioning**: Create checks on-the-fly via first ping
- **Cron expression support**: Native cron schedule validation
- **Job duration tracking**: Measure /start to completion time
- **Log attachment**: Attach stdout/stderr to pings via POST body
- **Grace periods**: Configurable buffer before alerting

## Pricing

### SaaS Plans

| Plan | Price/Month | Checks | Team Members | Ping Rate | Log Retention |
|------|-------------|--------|--------------|-----------|---------------|
| **Free** | $0 | 20 | - | 100/check/day | Limited |
| **Hobbyist** | $5 | 20 | 3 | 1000/check/day | 1 year |
| **Business** | $20 | 100 | 10 | 1000/check/day | 1 year |
| **Business Plus** | $80 | 1,000 | Unlimited | 1000/check/day | 1 year |

### Self-Hosted

- **Completely free** with no feature restrictions
- Same features as paid SaaS
- Docker deployment available
- Requires PostgreSQL

## Technology Stack

### Backend
- **Language**: Python 3
- **Framework**: Django
- **Database**: PostgreSQL
- **Task Queue**: Not specified (lightweight design)

### Frontend
- **Stack**: Django templates, minimal JavaScript
- **Design**: Simple, functional UI

### Infrastructure
- **Deployment**: Docker support
- **Self-hosted**: Full feature parity with SaaS
- **Dependencies**: PostgreSQL required

## API

### Design
- **Architecture**: RESTful
- **Authentication**: API key (read-only and read-write keys)
- **Format**: JSON
- **Documentation**: Good, clear documentation

### Key Endpoints

```bash
# List all checks
GET /api/v3/checks/

# Create a check
POST /api/v3/checks/

# Get single check
GET /api/v3/checks/{uuid}

# Ping a check
GET/POST https://hc-ping.com/{uuid}

# Get check pings (history)
GET /api/v3/checks/{uuid}/pings/

# Get check flips (state changes)
GET /api/v3/checks/{uuid}/flips/

# Manage notification channels
GET /api/v3/channels/
```

### API Capabilities
- Full CRUD for checks
- Channel management
- Ping history retrieval
- Badge generation
- Project-level API keys
- Read-only and read-write key types

## Strengths

### Core
1. ✅ **Best-in-class cron monitoring**: Purpose-built, not bolted on
2. ✅ **Fully open source (BSD)**: No feature restrictions self-hosted
3. ✅ **25+ notification channels**: One of the broadest in the category
4. ✅ **Ping-by-email**: Unique feature for legacy systems
5. ✅ **Exit code tracking**: Monitor job success/failure granularly
6. ✅ **Auto-provisioning**: Create checks via first ping
7. ✅ **Job duration tracking**: /start to completion timing
8. ✅ **Log attachment**: Attach output to pings
9. ✅ **Simple, reliable**: Does one thing exceptionally well
10. ✅ **PostgreSQL-based**: Enterprise database

### Deployment
11. ✅ **Self-hosted option**: Full feature parity
12. ✅ **Docker support**: Easy deployment
13. ✅ **Lightweight**: Minimal resource requirements
14. ✅ **Active development**: Regular updates

## Weaknesses

### Missing Features
1. ❌ **No active monitoring**: No HTTP, TCP, ICMP, DNS, SSL checks
2. ❌ **No status pages**: No public status page feature
3. ❌ **No distributed workers**: Single-location only
4. ❌ **No response time tracking**: No performance metrics
5. ❌ **No multi-location checks**: No geo-verification
6. ❌ **No incident management**: Basic alerting only
7. ❌ **No SLA reporting**: No uptime percentage tracking

### Architecture
8. ❌ **Single-purpose**: Only cron/heartbeat monitoring
9. ❌ **Single maintainer risk**: Primarily one developer
10. ❌ **No RBAC**: Limited access control (project-level keys)
11. ❌ **Basic UI**: Functional but not modern/reactive

## Comparison with SolidPing

### Similarities
- Both support heartbeat/cron monitoring
- Both use PostgreSQL
- Both are self-hostable
- Both have REST APIs

### Healthchecks.io Advantages
1. **Best-in-class cron monitoring**: Purpose-built with /start, /fail, exit codes
2. **25+ notification channels** vs SolidPing's 3 (Slack, Email, Webhooks)
3. **Ping-by-email**: Unique capability
4. **Log attachment**: Attach job output to pings
5. **Auto-provisioning**: Create checks on first ping
6. **Mature (2015)**: 10+ years of development
7. **Fully open source**: BSD license, no restrictions

### SolidPing Advantages
1. **Active monitoring**: HTTP, TCP, ICMP, DNS, Domain expiration (Healthchecks.io has none)
2. **Status pages**: Public status pages with sections
3. **Distributed workers**: Multi-region monitoring
4. **Incident management**: Sophisticated tracking with escalation, relapse detection
5. **Multi-tenancy**: Organization-scoped data with RBAC
6. **Response time tracking**: Performance metrics (min/max/avg)
7. **Check groups**: Organizational structure
8. **Keyword/body matching**: HTTP response validation
9. **Modern UI**: Reactive dash0 frontend

### Positioning

Healthchecks.io and SolidPing are **complementary rather than directly competitive**:
- Healthchecks.io excels at cron/heartbeat monitoring (passive)
- SolidPing excels at active monitoring (HTTP, TCP, DNS, etc.)
- SolidPing's heartbeat feature competes but lacks Healthchecks.io's depth (exit codes, /start, log attachment)

### What SolidPing Should Learn

**From Healthchecks.io's heartbeat model**:
1. Add `/start` endpoint for job duration tracking
2. Support exit code reporting
3. Allow log/output attachment to heartbeat pings
4. Auto-provision checks on first ping
5. Add grace period configuration
6. Support cron expression validation

## Use Cases

### Best For
- **Cron jobs**: Scheduled task verification
- **Background workers**: Async job monitoring
- **Backup jobs**: Verify backups complete
- **Data pipelines**: Track ETL job runs
- **Serverless functions**: Monitor Lambda/Cloud Functions
- **IoT devices**: Device heartbeat monitoring

### Not Ideal For
- Active website monitoring (no HTTP checks)
- Performance monitoring (no response times)
- SSL/domain monitoring
- Network monitoring (no TCP, DNS, ICMP)
- Status pages
- Enterprise deployments needing RBAC

## Sources

### Official
- [Healthchecks.io Website](https://healthchecks.io)
- [Healthchecks.io GitHub](https://github.com/healthchecks/healthchecks)
- [API Documentation](https://healthchecks.io/docs/api/)
- [Self-Hosted Guide](https://healthchecks.io/docs/self_hosted/)

### Community
- [Healthchecks.io on GitHub](https://github.com/healthchecks/healthchecks)

**Last Updated**: 2026-03-22
